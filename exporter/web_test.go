package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestWebPageAndStatus(t *testing.T) {
	coll := newCollector()
	mux := http.NewServeMux()
	registerWeb(mux, coll)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	// Pagina: het ingebouwde ontwerp, met het live script.
	resp, err := http.Get(srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	var page strings.Builder
	buf := make([]byte, 1<<16)
	for {
		n, err := resp.Body.Read(buf)
		page.Write(buf[:n])
		if err != nil {
			break
		}
	}
	if !strings.Contains(page.String(), "ClusterBusters Dashboard") || !strings.Contains(page.String(), "api/status") {
		t.Error("pagina mist titel of live script")
	}
	if r, _ := http.Get(srv.URL + "/bestaat-niet"); r.StatusCode != http.StatusNotFound {
		t.Errorf("onbekend pad gaf %d", r.StatusCode)
	}

	// Voor de eerste scan: scanned=false, maar wel alle checks en lege lijsten.
	st := getStatus(t, srv.URL)
	if st.Scanned || len(st.Checks) != len(checks) || st.Findings == nil || st.History == nil {
		t.Errorf("status voor de eerste scan klopt niet: %+v", st)
	}

	coll.set(Result{
		Score: 70, CheckUp: map[string]bool{"pdb": true}, Timestamp: time.Now(),
		CurrentVersion: "4.17.3", TargetVersion: "4.18",
		Findings: []Finding{
			{Check: "unsupported", Category: "unsupported", Severity: Warning, Resource: "Pod a/b"},
			{Check: "pdb", Category: "pdb", Severity: Blocker, Resource: "PodDisruptionBudget a/x",
				Title: "english", Fix: "fix", TitleNL: "nederlands", FixNL: "oplossing"},
		},
	})
	st = getStatus(t, srv.URL)
	if !st.Scanned || st.Score != 70 || st.TargetVersion != "4.18" {
		t.Errorf("status na scan: %+v", st)
	}
	if f := st.Findings[0]; f.Title != "english" || f.TitleNL != "nederlands" || f.FixNL != "oplossing" {
		t.Errorf("beide talen horen in de status te staan: %+v", f)
	}
	if st.Findings[0].Severity != Blocker {
		t.Errorf("blocker hoort bovenaan: %+v", st.Findings)
	}
	if st.Categories["pdb"][Blocker] != 1 || st.Categories["unsupported"][Warning] != 1 {
		t.Errorf("categorieën: %+v", st.Categories)
	}
	if len(st.History) != 1 || st.History[0].Blockers != 1 || st.History[0].Warnings != 1 {
		t.Errorf("historie: %+v", st.History)
	}
}

func getStatus(t *testing.T, base string) apiStatus {
	t.Helper()
	resp, err := http.Get(base + "/api/status")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var st apiStatus
	if err := json.NewDecoder(resp.Body).Decode(&st); err != nil {
		t.Fatal(err)
	}
	return st
}

func TestHistoryIsCapped(t *testing.T) {
	c := newCollector()
	for i := 0; i < maxHistory+20; i++ {
		c.set(Result{Score: i, Timestamp: time.Unix(int64(i), 0)})
	}
	_, h := c.snapshot()
	if len(h) != maxHistory || h[len(h)-1].Score != maxHistory+19 {
		t.Errorf("historie: %d punten, laatste %d", len(h), h[len(h)-1].Score)
	}
}

type fakeScalar struct {
	fakeQuerier
	vals map[string]float64
}

func (f fakeScalar) QueryScalar(_ context.Context, expr string) (float64, error) {
	return f.vals[expr], nil
}

func TestPlatformStatus(t *testing.T) {
	s := newScanner(t)
	if s.platformStatus(context.Background()) != nil {
		t.Error("zonder Thanos hoort er geen platformstatus te zijn")
	}
	s.metrics = fakeScalar{
		fakeQuerier: fakeQuerier{series: []map[string]string{
			{"group": "apps.openshift.io", "version": "v1", "resource": "deploymentconfigs", "removed_release": "6.0"},
		}},
		vals: map[string]float64{platformPDBQuery: 2, platformOperatorsQuery: 1},
	}
	p := s.platformStatus(context.Background())
	if p.BlockingPDBs != 2 || p.UnreadyNodes != 0 || p.UnhealthyOperators != 1 || len(p.DeprecatedAPIs) != 1 || p.Error != "" {
		t.Errorf("platform: %+v", p)
	}
}

func TestQueryScalar(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[{"metric":{},"value":[1730800000.1,"3"]}]}}`))
	}))
	defer srv.Close()
	c := &thanosClient{baseURL: srv.URL, http: srv.Client()}
	v, err := c.QueryScalar(context.Background(), "count(x)")
	if err != nil || v != 3 {
		t.Errorf("QueryScalar = %v, %v", v, err)
	}
}

// De opname moet kloppen met zichzelf, en de pagina mag geen startcommando noemen: de Route heeft geen login.
func TestResilienceRecording(t *testing.T) {
	mux := http.NewServeMux()
	registerWeb(mux, newCollector())
	srv := httptest.NewServer(mux)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/resilience")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var rec struct {
		TotalRuns int      `json:"totalRuns"`
		Scenarios []string `json:"scenarios"`
		Summary   []struct {
			Runs int `json:"runs"`
		} `json:"summary"`
		Replay []struct {
			Scenario string `json:"scenario"`
			Arm      string `json:"arm"`
		} `json:"replay"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&rec); err != nil {
		t.Fatal(err)
	}
	total := 0
	for _, s := range rec.Summary {
		total += s.Runs
	}
	if total != rec.TotalRuns || rec.TotalRuns == 0 {
		t.Errorf("samenvatting telt %d runs, totaal zegt %d", total, rec.TotalRuns)
	}
	if len(rec.Replay) != 2*len(rec.Scenarios) {
		t.Errorf("%d opnames voor %d scenario's, verwacht twee per scenario", len(rec.Replay), len(rec.Scenarios))
	}
	if strings.Contains(string(indexHTML), "chaos-run") {
		t.Error("de pagina noemt een startcommando; deze Route heeft geen login")
	}
}
