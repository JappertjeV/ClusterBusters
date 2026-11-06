package main

import (
	"context"
	"io"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynfake "k8s.io/client-go/dynamic/fake"
)

func obj(apiVersion, kind, ns, name string, fields map[string]interface{}) *unstructured.Unstructured {
	meta := map[string]interface{}{"name": name}
	if ns != "" {
		meta["namespace"] = ns
	}
	o := map[string]interface{}{"apiVersion": apiVersion, "kind": kind, "metadata": meta}
	for k, v := range fields {
		if k == "metadata" {
			for mk, mv := range v.(map[string]interface{}) {
				meta[mk] = mv
			}
			continue
		}
		o[k] = v
	}
	return &unstructured.Unstructured{Object: o}
}

func conds(pairs ...string) map[string]interface{} {
	var list []interface{}
	for i := 0; i+1 < len(pairs); i += 2 {
		list = append(list, map[string]interface{}{"type": pairs[i], "status": pairs[i+1], "message": pairs[i] + " msg"})
	}
	return map[string]interface{}{"conditions": list}
}

func newScanner(t *testing.T, objs ...runtime.Object) *Scanner {
	t.Helper()
	listKinds := map[schema.GroupVersionResource]string{
		gvrClusterVersion: "ClusterVersionList", gvrClusterOperator: "ClusterOperatorList",
		gvrCSV: "ClusterServiceVersionList", gvrNode: "NodeList", gvrMCP: "MachineConfigPoolList",
		gvrPDB: "PodDisruptionBudgetList", gvrAPIRequestCount: "APIRequestCountList",
		gvrConfigMap: "ConfigMapList", gvrPod: "PodList",
		gvrSubscription: "SubscriptionList", gvrInstallPlan: "InstallPlanList",
	}
	for _, r := range unsupportedOperatorResources {
		listKinds[schema.GroupVersionResource{Group: "operator.openshift.io", Version: "v1", Resource: r}] = r + "List"
	}
	client := dynfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), listKinds, objs...)
	return &Scanner{client: client}
}

// Een gezond cluster: alles groen, score 100.
func healthyCluster() []runtime.Object {
	return []runtime.Object{
		obj("config.openshift.io/v1", "ClusterVersion", "", "version", map[string]interface{}{
			"status": merge(conds("Available", "True", "Failing", "False", "Progressing", "False"),
				map[string]interface{}{"desired": map[string]interface{}{"version": "4.17.3"}}),
		}),
		obj("config.openshift.io/v1", "ClusterOperator", "", "dns", map[string]interface{}{
			"status": conds("Available", "True", "Degraded", "False", "Progressing", "False", "Upgradeable", "True"),
		}),
		obj("v1", "Node", "", "crc", map[string]interface{}{"status": conds("Ready", "True", "MemoryPressure", "False")}),
		obj("machineconfiguration.openshift.io/v1", "MachineConfigPool", "", "master", map[string]interface{}{
			"status": conds("Updated", "True", "Degraded", "False"),
		}),
	}
}

func merge(a, b map[string]interface{}) map[string]interface{} {
	for k, v := range b {
		a[k] = v
	}
	return a
}

func TestHealthyCluster(t *testing.T) {
	s := newScanner(t, healthyCluster()...)
	res := s.Scan(context.Background())
	for c, up := range res.CheckUp {
		if !up {
			t.Errorf("check %s faalt", c)
		}
	}
	if len(res.Findings) != 0 {
		t.Errorf("verwacht geen bevindingen, kreeg %+v", res.Findings)
	}
	if res.Score != 100 || !res.Ready {
		t.Errorf("score=%d ready=%v", res.Score, res.Ready)
	}
	if s.target == nil || s.target.String() != "4.18" {
		t.Errorf("doelversie = %v, verwacht 4.18", s.target)
	}
}

// Het demo-scenario uit CLAUDE.md.
func TestDemoScenario(t *testing.T) {
	objs := append(healthyCluster(),
		obj("policy/v1", "PodDisruptionBudget", "demo", "blocking", map[string]interface{}{
			"status": map[string]interface{}{"expectedPods": int64(1), "disruptionsAllowed": int64(0)},
		}),
		obj("policy/v1", "PodDisruptionBudget", "demo", "orphan", map[string]interface{}{
			"status": map[string]interface{}{"expectedPods": int64(0), "disruptionsAllowed": int64(0)},
		}),
		obj("policy/v1", "PodDisruptionBudget", "openshift-etcd", "platform", map[string]interface{}{
			"status": map[string]interface{}{"expectedPods": int64(1), "disruptionsAllowed": int64(0)},
		}),
		obj("v1", "Pod", "demo", "naked", map[string]interface{}{"status": map[string]interface{}{"phase": "Running"}}),
		obj("v1", "Pod", "demo", "owned", map[string]interface{}{
			"metadata": map[string]interface{}{"ownerReferences": []interface{}{
				map[string]interface{}{"apiVersion": "apps/v1", "kind": "ReplicaSet", "name": "x", "uid": "1"}}},
			"status": map[string]interface{}{"phase": "Running"},
		}),
	)
	res := newScanner(t, objs...).Scan(context.Background())

	got := map[string]string{}
	for _, f := range res.Findings {
		got[f.Resource] = f.Severity
	}
	want := map[string]string{
		"PodDisruptionBudget demo/blocking": Blocker,
		"PodDisruptionBudget demo/orphan":   Warning,
		"Pod demo/naked":                    Warning,
	}
	for r, sev := range want {
		if got[r] != sev {
			t.Errorf("%s: severity %q, verwacht %q", r, got[r], sev)
		}
	}
	if len(got) != len(want) {
		t.Errorf("onverwachte bevindingen: %v", got)
	}
	// 100 - 25 - 5 - 5
	if res.Score != 65 || res.Ready {
		t.Errorf("score=%d ready=%v, verwacht 65 en false", res.Score, res.Ready)
	}
}

func TestBrokenPlatform(t *testing.T) {
	objs := []runtime.Object{
		obj("config.openshift.io/v1", "ClusterVersion", "", "version", map[string]interface{}{
			"status": merge(conds("Failing", "True", "Upgradeable", "False"), map[string]interface{}{
				"desired": map[string]interface{}{"version": "4.17.3"},
				"conditionalUpdates": []interface{}{map[string]interface{}{
					"release": map[string]interface{}{"version": "4.18.1"},
					"risks":   []interface{}{map[string]interface{}{"name": "SomeBug", "message": "kapot", "url": "https://example.com"}},
				}},
			}),
		}),
		obj("v1", "ConfigMap", "openshift-config-managed", "admin-gates", map[string]interface{}{
			"data": map[string]interface{}{"ack-4.17-kube-1.31-api-removals-in-4.18": "lees dit", "ack-old": "oud"},
		}),
		obj("v1", "ConfigMap", "openshift-config", "admin-acks", map[string]interface{}{
			"data": map[string]interface{}{"ack-old": "true"},
		}),
		obj("config.openshift.io/v1", "ClusterOperator", "", "etcd", map[string]interface{}{
			"status": conds("Available", "False", "Degraded", "True", "Progressing", "True"),
		}),
		obj("operators.coreos.com/v1alpha1", "ClusterServiceVersion", "ops", "old.v1", map[string]interface{}{
			"metadata": map[string]interface{}{"annotations": map[string]interface{}{
				"operatorframework.io/properties": `{"properties":[{"type":"olm.maxOpenShiftVersion","value":"4.17"}]}`}},
			"status": map[string]interface{}{"phase": "Succeeded"},
		}),
		obj("operators.coreos.com/v1alpha1", "ClusterServiceVersion", "other", "old.v1", map[string]interface{}{
			"status": map[string]interface{}{"phase": "Failed", "reason": "Copied"},
		}),
		obj("operators.coreos.com/v1alpha1", "ClusterServiceVersion", "ops", "broken.v2", map[string]interface{}{
			"status": map[string]interface{}{"phase": "Failed", "message": "install timeout"},
		}),
		obj("v1", "Node", "", "crc", map[string]interface{}{
			"spec":   map[string]interface{}{"unschedulable": true},
			"status": conds("Ready", "False", "DiskPressure", "True"),
		}),
		obj("machineconfiguration.openshift.io/v1", "MachineConfigPool", "", "worker", map[string]interface{}{
			"spec":   map[string]interface{}{"paused": true},
			"status": conds("Updated", "False", "Degraded", "True"),
		}),
		obj("apiserver.openshift.io/v1", "APIRequestCount", "", "flowschemas.v1beta3.flowcontrol.apiserver.k8s.io", map[string]interface{}{
			"status": map[string]interface{}{"removedInRelease": "1.32", "requestCount": int64(10),
				"last24h": []interface{}{map[string]interface{}{"byNode": []interface{}{map[string]interface{}{
					"byUser": []interface{}{
						map[string]interface{}{"username": "alice", "requestCount": int64(3)},
						map[string]interface{}{"username": "bob", "requestCount": int64(7)},
					}}}}}}}),
		obj("apiserver.openshift.io/v1", "APIRequestCount", "", "later.v1beta1.example.io", map[string]interface{}{
			"status": map[string]interface{}{"removedInRelease": "1.40", "requestCount": int64(1)}}),
		obj("apiserver.openshift.io/v1", "APIRequestCount", "", "unused.v1beta1.example.io", map[string]interface{}{
			"status": map[string]interface{}{"removedInRelease": "1.25", "requestCount": int64(0)}}),
		obj("operator.openshift.io/v1", "KubeAPIServer", "", "cluster", map[string]interface{}{
			"spec": map[string]interface{}{"unsupportedConfigOverrides": map[string]interface{}{"foo": "bar"}}}),
		obj("operator.openshift.io/v1", "Etcd", "", "cluster", map[string]interface{}{
			"spec": map[string]interface{}{"unsupportedConfigOverrides": nil}}),
	}
	res := newScanner(t, objs...).Scan(context.Background())

	type key struct{ check, sev string }
	count := map[key]int{}
	for _, f := range res.Findings {
		count[key{f.Check, f.Severity}]++
	}
	want := map[key]int{
		{"clusterversion", Blocker}: 2, // Failing + Upgradeable
		{"clusterversion", Info}:    1, // risk
		{"clusterversion", Warning}: 1, // alleen de onbevestigde gate
		{"operators", Blocker}:      2,
		{"operators", Warning}:      1,
		{"olm", Blocker}:            2, // max 4.17 < 4.18, en phase Failed
		{"nodes", Blocker}:          2, // NotReady + MCP Degraded
		{"nodes", Warning}:          4, // DiskPressure, unschedulable, paused, niet Updated
		// Kubernetes 1.32 hoort bij OpenShift 4.19, later dan doel 4.18: warning. 1.40 ook.
		// De ongebruikte API (requestCount 0) telt niet mee.
		{"deprecated_apis", Blocker}: 0,
		{"deprecated_apis", Warning}: 2,
		{"unsupported", Blocker}:     1, // alleen KubeAPIServer, Etcd heeft null
	}
	for k, n := range want {
		if count[k] != n {
			t.Errorf("%v: %d bevindingen, verwacht %d", k, count[k], n)
		}
	}
	for _, f := range res.Findings {
		if f.Check == "deprecated_apis" && strings.HasPrefix(f.Resource, "APIRequestCount flowschemas") &&
			!strings.Contains(f.Fix, "bob, alice") {
			t.Errorf("topgebruikers niet in fix: %q", f.Fix)
		}
		// De taalknop op de webpagina heeft van elke bevinding beide talen nodig.
		if f.Title == "" || f.Fix == "" || f.TitleNL == "" || f.FixNL == "" {
			t.Errorf("%s: tekst ontbreekt in een taal: %+v", f.Resource, f)
		}
		if f.Title == f.TitleNL && f.Fix == f.FixNL {
			t.Errorf("%s: Engels en Nederlands zijn gelijk: %q", f.Resource, f.Fix)
		}
	}
	if res.Score != 0 || res.Ready {
		t.Errorf("score=%d ready=%v", res.Score, res.Ready)
	}
}

func TestTargetOverrideMakesDeprecatedBlocker(t *testing.T) {
	s := newScanner(t, append(healthyCluster(),
		obj("apiserver.openshift.io/v1", "APIRequestCount", "", "x.v1beta1.example.io", map[string]interface{}{
			"status": map[string]interface{}{"removedInRelease": "1.32", "requestCount": int64(5)}}))...)
	s.targetOverride = "4.19"
	res := s.Scan(context.Background())
	if len(res.Findings) != 1 || res.Findings[0].Severity != Blocker {
		t.Fatalf("verwacht 1 blocker, kreeg %+v", res.Findings)
	}
}

func TestFailingCheckDoesNotStopScan(t *testing.T) {
	// Zonder ClusterVersion faalt die check, de rest moet doorlopen.
	objs := []runtime.Object{
		obj("policy/v1", "PodDisruptionBudget", "demo", "blocking", map[string]interface{}{
			"status": map[string]interface{}{"expectedPods": int64(2), "disruptionsAllowed": int64(0)},
		}),
	}
	res := newScanner(t, objs...).Scan(context.Background())
	if res.CheckUp["clusterversion"] {
		t.Error("clusterversion zou moeten falen")
	}
	if !res.CheckUp["pdb"] || len(res.Findings) != 1 {
		t.Errorf("pdb-check moet doorlopen: up=%v findings=%+v", res.CheckUp["pdb"], res.Findings)
	}
	if res.Ready {
		t.Error("ready moet 0 zijn bij een falende check")
	}
}

func TestMetricsOutput(t *testing.T) {
	c := newCollector()
	reg := prometheus.NewRegistry()
	reg.MustRegister(c)
	c.set(Result{
		CheckUp: map[string]bool{"pdb": true},
		Score:   75,
		Findings: []Finding{{Check: "pdb", Category: "pdb", Severity: Blocker,
			Resource: "PodDisruptionBudget demo/x", Title: "t", Fix: "f"}},
	})
	srv := httptest.NewServer(promhttp.HandlerFor(reg, promhttp.HandlerOpts{}))
	defer srv.Close()
	resp, err := srv.Client().Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	out := string(body)
	for _, want := range []string{
		"upgrade_readiness_score 75",
		"upgrade_readiness_ready 0",
		`upgrade_readiness_findings{category="pdb",severity="blocker"} 1`,
		`upgrade_readiness_findings{category="nodes",severity="blocker"} 0`,
		`upgrade_readiness_finding{category="pdb",check="pdb",fix="f",resource="PodDisruptionBudget demo/x",severity="blocker",title="t"} 1`,
		`upgrade_readiness_check_up{check="pdb"} 1`,
		`upgrade_readiness_check_up{check="olm"} 0`,
		"upgrade_readiness_last_scan_timestamp_seconds",
		`upgrade_readiness_version_info{current_version="unknown",target_version="unknown"} 1`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("ontbreekt in /metrics: %s", want)
		}
	}
}

// De Nederlandse tekst staat niet in de labels. Twee bevindingen die alleen daarin verschillen,
// zouden anders dezelfde labels houden en de scrape laten falen.
func TestDedupeIgnoresDutchText(t *testing.T) {
	a := Finding{Check: "pdb", Severity: Warning, Resource: "x", Title: "t", Fix: "f", TitleNL: "een"}
	b := a
	b.TitleNL = "twee"
	if got := dedupe([]Finding{a, b}); len(got) != 1 {
		t.Errorf("verwacht 1 bevinding, kreeg %+v", got)
	}
}

func TestMaxOpenShiftVersion(t *testing.T) {
	for in, want := range map[string]string{
		`{"properties":[{"type":"olm.maxOpenShiftVersion","value":"4.15"}]}`: "4.15",
		`{"properties":[{"type":"olm.maxOpenShiftVersion","value":4.16}]}`:   "4.16",
	} {
		v, ok := maxOpenShiftVersion(in)
		if !ok || v.String() != want {
			t.Errorf("%s: %v %v", in, v, ok)
		}
	}
	if _, ok := maxOpenShiftVersion(`{"properties":[{"type":"olm.package","value":{}}]}`); ok {
		t.Error("geen maxOpenShiftVersion verwacht")
	}
}

func TestOperatorUpdatesStuck(t *testing.T) {
	objs := append(healthyCluster(),
		obj("operators.coreos.com/v1alpha1", "Subscription", "test", "bestaat-niet", map[string]interface{}{
			"status": conds("ResolutionFailed", "True", "CatalogSourcesUnhealthy", "False"),
		}),
		obj("operators.coreos.com/v1alpha1", "Subscription", "test", "goed", map[string]interface{}{
			"status": conds("ResolutionFailed", "False"),
		}),
		obj("operators.coreos.com/v1alpha1", "InstallPlan", "test", "install-abc", map[string]interface{}{
			"spec":   map[string]interface{}{"approved": false, "clusterServiceVersionNames": []interface{}{"foo.v2"}},
			"status": map[string]interface{}{"phase": "RequiresApproval"},
		}),
		obj("operators.coreos.com/v1alpha1", "InstallPlan", "test", "install-done", map[string]interface{}{
			"spec":   map[string]interface{}{"approved": true},
			"status": map[string]interface{}{"phase": "Complete"},
		}),
	)
	res := newScanner(t, objs...).Scan(context.Background())
	got := map[string]string{}
	for _, f := range res.Findings {
		got[f.Resource] = f.Severity
	}
	want := map[string]string{
		"Subscription test/bestaat-niet": Warning,
		"InstallPlan test/install-abc":   Warning,
	}
	for r, sev := range want {
		if got[r] != sev {
			t.Errorf("%s: %q, verwacht %q", r, got[r], sev)
		}
	}
	if len(got) != len(want) {
		t.Errorf("onverwachte bevindingen: %v", got)
	}
	if !res.CheckUp["olm"] {
		t.Error("olm-check hoort te slagen")
	}
}

func TestRetrievedUpdatesIsInfo(t *testing.T) {
	objs := healthyCluster()
	cv := objs[0].(*unstructured.Unstructured)
	list, _, _ := unstructured.NestedSlice(cv.Object, "status", "conditions")
	list = append(list, map[string]interface{}{"type": "RetrievedUpdates", "status": "False", "reason": "NoChannel"})
	_ = unstructured.SetNestedSlice(cv.Object, list, "status", "conditions")
	res := newScanner(t, objs...).Scan(context.Background())
	if len(res.Findings) != 1 || res.Findings[0].Severity != Info {
		t.Fatalf("verwacht 1 info, kreeg %+v", res.Findings)
	}
	if res.Score != 100 || !res.Ready {
		t.Errorf("info mag score en ready niet raken: %d %v", res.Score, res.Ready)
	}
}

// Elk commando in een fix moet in cmd, PowerShell en bash werken: geen JSON met aanhalingstekens
// erin, want die breken in cmd (enkele quotes) of PowerShell (\").
func TestFixCommandsAreShellSafe(t *testing.T) {
	objs := append(healthyCluster(),
		obj("policy/v1", "PodDisruptionBudget", "demo", "blocking", map[string]interface{}{
			"status": map[string]interface{}{"expectedPods": int64(1), "disruptionsAllowed": int64(0)}}),
		obj("policy/v1", "PodDisruptionBudget", "demo", "orphan", map[string]interface{}{
			"status": map[string]interface{}{"expectedPods": int64(0)}}),
		obj("v1", "ConfigMap", "openshift-config-managed", "admin-gates", map[string]interface{}{
			"data": map[string]interface{}{"ack-4.17-kube-1.31-api-removals-in-4.18": "lees dit"}}),
		obj("machineconfiguration.openshift.io/v1", "MachineConfigPool", "", "worker", map[string]interface{}{
			"spec": map[string]interface{}{"paused": true}, "status": conds("Updated", "True")}),
		obj("operator.openshift.io/v1", "KubeAPIServer", "", "cluster", map[string]interface{}{
			"spec": map[string]interface{}{"unsupportedConfigOverrides": map[string]interface{}{"a": "b"}}}),
		obj("operators.coreos.com/v1alpha1", "InstallPlan", "ops", "install-1", map[string]interface{}{
			"spec": map[string]interface{}{"approved": false}, "status": map[string]interface{}{"phase": "RequiresApproval"}}),
	)
	cv := objs[0].(*unstructured.Unstructured)
	list, _, _ := unstructured.NestedSlice(cv.Object, "status", "conditions")
	list = append(list, map[string]interface{}{"type": "RetrievedUpdates", "status": "False"})
	_ = unstructured.SetNestedSlice(cv.Object, list, "status", "conditions")

	res := newScanner(t, objs...).Scan(context.Background())
	patches, patchesNL := 0, 0
	hasPatch := func(fix string) bool {
		return strings.Contains(fix, "oc patch") || strings.Contains(fix, "patch cm") || strings.Contains(fix, "patch installplan")
	}
	for _, f := range res.Findings {
		for _, fix := range []string{f.Fix, f.FixNL} {
			if strings.Contains(fix, "'{") || strings.Contains(fix, `\"`) || strings.Contains(fix, "…") {
				t.Errorf("%s: fix niet shell-veilig of afgekapt: %s", f.Resource, fix)
			}
		}
		if hasPatch(f.Fix) {
			patches++
		}
		if hasPatch(f.FixNL) {
			patchesNL++
		}
	}
	if patches != 5 || patchesNL != 5 {
		t.Errorf("verwacht 5 patch-commando's per taal, kreeg %d en %d", patches, patchesNL)
	}
	for _, f := range res.Findings {
		switch f.Resource {
		case "PodDisruptionBudget demo/blocking":
			if !strings.Contains(f.Fix, "oc patch pdb blocking -n demo") {
				t.Errorf("PDB-fix zonder commando: %s", f.Fix)
			}
		case "ClusterVersion version":
			if f.Severity == Info && !strings.Contains(f.Fix, "stable-4.17") {
				t.Errorf("channel hoort de huidige minor te volgen: %s", f.Fix)
			}
		}
	}
	if res.CurrentVersion != "4.17.3" || res.TargetVersion != "4.18" {
		t.Errorf("versies: %q -> %q", res.CurrentVersion, res.TargetVersion)
	}
}
