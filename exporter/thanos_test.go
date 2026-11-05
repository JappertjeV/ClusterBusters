package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/runtime"
)

type fakeQuerier struct {
	series []map[string]string
	noUp   bool
	err    error
}

func (f fakeQuerier) Query(_ context.Context, expr string) ([]map[string]string, error) {
	if f.err != nil {
		return nil, f.err
	}
	if expr == apiserverUpQuery {
		if f.noUp {
			return nil, nil
		}
		return []map[string]string{{"job": "apiserver"}}, nil
	}
	return f.series, nil
}

func deprecatedFindings(res Result) []Finding {
	var out []Finding
	for _, f := range res.Findings {
		if f.Check == "deprecated_apis" {
			out = append(out, f)
		}
	}
	return out
}

// Regressie: op crc03 is APIRequestCount leeg, terwijl het platform 6 verouderde API's zag. De
// check stond op OK met 0 bevindingen. Label 6.0 is geen Kubernetes-versie: kubeToOCP maakte daar
// 4.-13 van, en dat zou een blocker zijn.
func TestDeprecatedFromMetricsWithoutRequestCounts(t *testing.T) {
	s := newScanner(t, healthyCluster()...)
	s.metrics = fakeQuerier{series: []map[string]string{
		{"group": "apps.openshift.io", "version": "v1", "resource": "deploymentconfigs", "removed_release": "6.0"},
		{"version": "v1", "resource": "endpoints"},
		{"group": "perses.dev", "version": "v1alpha1", "resource": "perses"},
	}}
	res := s.Scan(context.Background())

	got := map[string]string{}
	for _, f := range deprecatedFindings(res) {
		got[f.Resource] = f.Severity
	}
	want := map[string]string{
		"API deploymentconfigs.v1.apps.openshift.io": Warning,
		"API endpoints.v1":                           Info,
		"API perses.v1alpha1.perses.dev":             Info,
	}
	if len(got) != len(want) {
		t.Fatalf("bevindingen %v, verwacht %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s: %q, verwacht %q", k, got[k], v)
		}
	}
	if !res.CheckUp["deprecated_apis"] {
		t.Error("check zou moeten slagen")
	}
	if res.Score != 95 {
		t.Errorf("score %d, verwacht 95: alleen de warning telt mee", res.Score)
	}
}

func TestDeprecatedMetricRemovedInTargetIsBlocker(t *testing.T) {
	s := newScanner(t, healthyCluster()...)
	s.targetOverride = "4.19"
	s.metrics = fakeQuerier{series: []map[string]string{
		{"group": "example.io", "version": "v1beta1", "resource": "dingen", "removed_release": "1.32"},
	}}
	got := deprecatedFindings(s.Scan(context.Background()))
	if len(got) != 1 || got[0].Severity != Blocker {
		t.Fatalf("verwacht 1 blocker, kreeg %+v", got)
	}
}

// Dezelfde API uit beide bronnen is één bevinding, die van APIRequestCount: alleen die noemt gebruikers.
func TestDeprecatedDedupesAcrossSources(t *testing.T) {
	s := newScanner(t, append(healthyCluster(),
		obj("apiserver.openshift.io/v1", "APIRequestCount", "", "dingen.v1beta1.example.io", map[string]interface{}{
			"status": map[string]interface{}{"removedInRelease": "1.40", "requestCount": int64(5)}}))...)
	s.metrics = fakeQuerier{series: []map[string]string{
		{"group": "example.io", "version": "v1beta1", "resource": "dingen", "removed_release": "1.40"},
	}}
	got := deprecatedFindings(s.Scan(context.Background()))
	if len(got) != 1 || !strings.HasPrefix(got[0].Resource, "APIRequestCount ") {
		t.Fatalf("verwacht 1 bevinding uit APIRequestCount, kreeg %+v", got)
	}
}

// Regressie voor dezelfde 4.-13: ook een APIRequestCount kan een niet-Kubernetes-release noemen.
func TestRequestCountWithOpenShiftReleaseIsNotBlocker(t *testing.T) {
	s := newScanner(t, append(healthyCluster(),
		obj("apiserver.openshift.io/v1", "APIRequestCount", "", "deploymentconfigs.v1.apps.openshift.io", map[string]interface{}{
			"status": map[string]interface{}{"removedInRelease": "6.0", "requestCount": int64(5)}}))...)
	got := deprecatedFindings(s.Scan(context.Background()))
	if len(got) != 1 || got[0].Severity != Warning {
		t.Fatalf("verwacht 1 warning, kreeg %+v", got)
	}
}

// Zonder oordeel mag de check niet op OK staan: dat was precies de fout op crc03.
func TestDeprecatedFailsWithoutMetricData(t *testing.T) {
	for name, q := range map[string]fakeQuerier{
		"thanos onbereikbaar":   {err: errors.New("connection refused")},
		"geen apiserver-scrape": {noUp: true},
	} {
		t.Run(name, func(t *testing.T) {
			s := newScanner(t, healthyCluster()...)
			s.metrics = q
			res := s.Scan(context.Background())
			if res.CheckUp["deprecated_apis"] || res.Ready {
				t.Errorf("up=%v ready=%v, verwacht beide false", res.CheckUp["deprecated_apis"], res.Ready)
			}
		})
	}
}

// Faalt Thanos, dan blijft wat APIRequestCount vond zichtbaar.
func TestDeprecatedKeepsRequestCountsWhenThanosFails(t *testing.T) {
	var objs []runtime.Object
	objs = append(objs, healthyCluster()...)
	objs = append(objs, obj("apiserver.openshift.io/v1", "APIRequestCount", "", "x.v1beta1.example.io", map[string]interface{}{
		"status": map[string]interface{}{"removedInRelease": "1.40", "requestCount": int64(5)}}))
	s := newScanner(t, objs...)
	s.metrics = fakeQuerier{err: errors.New("connection refused")}
	res := s.Scan(context.Background())
	if len(deprecatedFindings(res)) != 1 || res.CheckUp["deprecated_apis"] {
		t.Errorf("findings=%+v up=%v", deprecatedFindings(res), res.CheckUp["deprecated_apis"])
	}
}

func TestThanosClientQuery(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/query" || r.URL.Query().Get("partial_response") != "false" || r.URL.Query().Get("query") != "up" {
			t.Errorf("verzoek %s", r.URL)
		}
		_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[{"metric":{"job":"api"},"value":[1,"1"]}]}}`))
	}))
	defer srv.Close()
	got, err := (&thanosClient{baseURL: srv.URL, http: srv.Client()}).Query(context.Background(), "up")
	if err != nil || len(got) != 1 || got[0]["job"] != "api" {
		t.Fatalf("got=%v err=%v", got, err)
	}
}

func TestThanosClientErrors(t *testing.T) {
	for name, h := range map[string]http.HandlerFunc{
		"http 403": func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "Forbidden", http.StatusForbidden) },
		"status":   func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{"status":"error","error":"kapot"}`)) },
		"json":     func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`<html>`)) },
	} {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(h)
			defer srv.Close()
			if _, err := (&thanosClient{baseURL: srv.URL, http: srv.Client()}).Query(context.Background(), "up"); err == nil {
				t.Error("verwacht een fout")
			}
		})
	}
}
