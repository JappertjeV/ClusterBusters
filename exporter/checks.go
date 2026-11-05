package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

var (
	gvrClusterVersion  = schema.GroupVersionResource{Group: "config.openshift.io", Version: "v1", Resource: "clusterversions"}
	gvrClusterOperator = schema.GroupVersionResource{Group: "config.openshift.io", Version: "v1", Resource: "clusteroperators"}
	gvrCSV             = schema.GroupVersionResource{Group: "operators.coreos.com", Version: "v1alpha1", Resource: "clusterserviceversions"}
	gvrNode            = schema.GroupVersionResource{Version: "v1", Resource: "nodes"}
	gvrMCP             = schema.GroupVersionResource{Group: "machineconfiguration.openshift.io", Version: "v1", Resource: "machineconfigpools"}
	gvrPDB             = schema.GroupVersionResource{Group: "policy", Version: "v1", Resource: "poddisruptionbudgets"}
	gvrAPIRequestCount = schema.GroupVersionResource{Group: "apiserver.openshift.io", Version: "v1", Resource: "apirequestcounts"}
	gvrConfigMap       = schema.GroupVersionResource{Version: "v1", Resource: "configmaps"}
	gvrPod             = schema.GroupVersionResource{Version: "v1", Resource: "pods"}
)

// Operator-configuraties (naam "cluster") die spec.unsupportedConfigOverrides kennen.
// Houd dit gelijk met de ClusterRole in base/exporter-rbac.yaml.
var unsupportedOperatorResources = []string{
	"kubeapiservers", "kubecontrollermanagers", "kubeschedulers", "etcds",
	"openshiftapiservers", "authentications", "networks", "consoles",
}

// Prometheus-labels worden onleesbaar in een tabel als ze te lang zijn.
func clip(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	const max = 240
	if len(s) > max {
		return s[:max] + "…"
	}
	return s
}

func isPlatformNamespace(ns string) bool {
	return ns == "openshift" || strings.HasPrefix(ns, "openshift-") || strings.HasPrefix(ns, "kube-")
}

type condition struct {
	Status  string
	Reason  string
	Message string
}

func conditions(obj map[string]interface{}) map[string]condition {
	out := map[string]condition{}
	list, _, _ := unstructured.NestedSlice(obj, "status", "conditions")
	for _, item := range list {
		m, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		t, _ := m["type"].(string)
		st, _ := m["status"].(string)
		r, _ := m["reason"].(string)
		msg, _ := m["message"].(string)
		out[t] = condition{st, r, msg}
	}
	return out
}

func withMessage(title string, c condition) string {
	if c.Message != "" {
		return clip(title + ": " + c.Message)
	}
	if c.Reason != "" {
		return clip(title + " (" + c.Reason + ")")
	}
	return title
}

// ---------------------------------------------------------------- clusterversion

func checkClusterVersion(ctx context.Context, s *Scanner) ([]Finding, error) {
	cv, err := s.client.Resource(gvrClusterVersion).Get(ctx, "version", metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	res := "ClusterVersion version"
	var out []Finding
	conds := conditions(cv.Object)

	if c := conds["Failing"]; c.Status == "True" {
		out = append(out, Finding{Severity: Blocker, Resource: res,
			Title: withMessage("Cluster-versie staat op Failing", c),
			Fix:   "Los eerst de huidige fout op: zie `oc adm upgrade` en `oc get clusterversion version -o yaml`."})
	}
	if c := conds["Upgradeable"]; c.Status == "False" {
		out = append(out, Finding{Severity: Blocker, Resource: res,
			Title: withMessage("Cluster is niet upgradeable", c),
			Fix:   "Los de oorzaak uit de Upgradeable-condition op. Meestal is dat een Operator met Upgradeable=False, zie de operators-bevindingen."})
	}
	if c := conds["Progressing"]; c.Status == "True" {
		out = append(out, Finding{Severity: Warning, Resource: res,
			Title: withMessage("Er loopt al een update", c),
			Fix:   "Wacht tot de lopende update klaar is voordat je een nieuwe start."})
	}

	// Bekende risico's van beschikbare updates.
	updates, _, _ := unstructured.NestedSlice(cv.Object, "status", "conditionalUpdates")
	seenRisk := map[string]bool{}
	for _, u := range updates {
		um, _ := u.(map[string]interface{})
		version, _, _ := unstructured.NestedString(um, "release", "version")
		risks, _, _ := unstructured.NestedSlice(um, "risks")
		for _, r := range risks {
			rm, _ := r.(map[string]interface{})
			name, _ := rm["name"].(string)
			if name == "" || seenRisk[name] {
				continue
			}
			seenRisk[name] = true
			url, _ := rm["url"].(string)
			msg, _ := rm["message"].(string)
			fix := "Beoordeel of dit risico voor dit cluster geldt voordat je naar " + version + " gaat."
			if url != "" {
				fix += " Zie " + url
			}
			out = append(out, Finding{Severity: Info, Resource: res,
				Title: clip(fmt.Sprintf("Bekend risico %s voor update naar %s: %s", name, version, msg)),
				Fix:   clip(fix)})
		}
	}

	// Admin-gates: handmatige bevestigingen die nodig zijn voor de volgende minor.
	gates, err := s.client.Resource(gvrConfigMap).Namespace("openshift-config-managed").Get(ctx, "admin-gates", metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return out, nil
		}
		return out, fmt.Errorf("admin-gates: %w", err)
	}
	acks := map[string]string{}
	ackCM, err := s.client.Resource(gvrConfigMap).Namespace("openshift-config").Get(ctx, "admin-acks", metav1.GetOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		return out, fmt.Errorf("admin-acks: %w", err)
	}
	if err == nil {
		acks, _, _ = unstructured.NestedStringMap(ackCM.Object, "data")
	}
	gateData, _, _ := unstructured.NestedStringMap(gates.Object, "data")
	for _, key := range sortedKeys(gateData) {
		if acks[key] == "true" {
			continue
		}
		out = append(out, Finding{Severity: Warning, Resource: "ConfigMap openshift-config/admin-acks",
			Title: clip("Admin-gate niet bevestigd: " + key + ": " + gateData[key]),
			Fix: clip(fmt.Sprintf(`Lees de gate, los op wat erin staat en bevestig: oc -n openshift-config patch cm admin-acks --type=merge -p '{"data":{"%s":"true"}}'`, key))})
	}
	return out, nil
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// ---------------------------------------------------------------- operators

func checkOperators(ctx context.Context, s *Scanner) ([]Finding, error) {
	list, err := s.client.Resource(gvrClusterOperator).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	var out []Finding
	for _, co := range list.Items {
		name := co.GetName()
		res := "ClusterOperator " + name
		fix := fmt.Sprintf("Bekijk `oc get co %s -o yaml` en de pods van deze Operator; los de oorzaak op voordat je upgradet.", name)
		conds := conditions(co.Object)
		if c := conds["Available"]; c.Status != "True" {
			out = append(out, Finding{Severity: Blocker, Resource: res, Title: withMessage("Operator is niet Available", c), Fix: fix})
		}
		if c := conds["Degraded"]; c.Status == "True" {
			out = append(out, Finding{Severity: Blocker, Resource: res, Title: withMessage("Operator is Degraded", c), Fix: fix})
		}
		if c := conds["Upgradeable"]; c.Status == "False" {
			out = append(out, Finding{Severity: Blocker, Resource: res, Title: withMessage("Operator blokkeert upgrades (Upgradeable=False)", c), Fix: fix})
		}
		if c := conds["Progressing"]; c.Status == "True" {
			out = append(out, Finding{Severity: Warning, Resource: res, Title: withMessage("Operator is nog bezig (Progressing)", c),
				Fix: "Wacht tot de Operator klaar is met uitrollen."})
		}
	}
	return out, nil
}

// ---------------------------------------------------------------- olm

func checkOLM(ctx context.Context, s *Scanner) ([]Finding, error) {
	list, err := s.client.Resource(gvrCSV).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	var out []Finding
	for _, csv := range list.Items {
		// OLM kopieert CSV's van globale Operators naar elke namespace; die slaan we over.
		if reason, _, _ := unstructured.NestedString(csv.Object, "status", "reason"); reason == "Copied" {
			continue
		}
		res := fmt.Sprintf("ClusterServiceVersion %s/%s", csv.GetNamespace(), csv.GetName())
		phase, _, _ := unstructured.NestedString(csv.Object, "status", "phase")
		if phase != "Succeeded" {
			msg, _, _ := unstructured.NestedString(csv.Object, "status", "message")
			out = append(out, Finding{Severity: Blocker, Resource: res,
				Title: withMessage("Operator-installatie staat op phase "+orUnknown(phase), condition{Message: msg}),
				Fix:   fmt.Sprintf("Bekijk `oc -n %s get csv %s -o yaml` en de InstallPlan; herstel of herinstalleer de Operator.", csv.GetNamespace(), csv.GetName())})
		}
		if s.target == nil {
			continue
		}
		maxV, ok := maxOpenShiftVersion(csv.GetAnnotations()["operatorframework.io/properties"])
		if ok && maxV.Less(*s.target) {
			out = append(out, Finding{Severity: Blocker, Resource: res,
				Title: fmt.Sprintf("Operator ondersteunt maximaal OpenShift %s, doel is %s", maxV, s.target),
				Fix:   "Update deze Operator naar een versie die " + s.target.String() + " ondersteunt (wissel zo nodig van channel), of verwijder hem."})
		}
	}
	return out, nil
}

func orUnknown(s string) string {
	if s == "" {
		return "onbekend"
	}
	return s
}

// maxOpenShiftVersion leest olm.maxOpenShiftVersion uit de properties-annotatie,
// bijvoorbeeld {"properties":[{"type":"olm.maxOpenShiftVersion","value":"4.15"}]}.
func maxOpenShiftVersion(annotation string) (minorVersion, bool) {
	if annotation == "" {
		return minorVersion{}, false
	}
	var props struct {
		Properties []struct {
			Type  string          `json:"type"`
			Value json.RawMessage `json:"value"`
		} `json:"properties"`
	}
	if json.Unmarshal([]byte(annotation), &props) != nil {
		return minorVersion{}, false
	}
	for _, p := range props.Properties {
		if p.Type != "olm.maxOpenShiftVersion" {
			continue
		}
		// De waarde staat er als string ("4.15") of als getal (4.15).
		raw := strings.Trim(string(p.Value), `"`)
		v, err := parseMinor(raw)
		if err != nil {
			return minorVersion{}, false
		}
		return v, true
	}
	return minorVersion{}, false
}

// ---------------------------------------------------------------- nodes

func checkNodes(ctx context.Context, s *Scanner) ([]Finding, error) {
	nodes, err := s.client.Resource(gvrNode).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	var out []Finding
	for _, n := range nodes.Items {
		name := n.GetName()
		res := "Node " + name
		conds := conditions(n.Object)
		if c := conds["Ready"]; c.Status != "True" {
			out = append(out, Finding{Severity: Blocker, Resource: res, Title: withMessage("Node is niet Ready", c),
				Fix: fmt.Sprintf("Onderzoek de node met `oc describe node %s` en `oc adm node-logs %s -u kubelet`.", name, name)})
		}
		for _, p := range []string{"MemoryPressure", "DiskPressure", "PIDPressure"} {
			if c := conds[p]; c.Status == "True" {
				out = append(out, Finding{Severity: Warning, Resource: res, Title: withMessage("Node heeft "+p, c),
					Fix: "Maak ruimte vrij of verplaats workloads; een drain tijdens de upgrade kan anders vastlopen."})
			}
		}
		if u, _, _ := unstructured.NestedBool(n.Object, "spec", "unschedulable"); u {
			out = append(out, Finding{Severity: Warning, Resource: res, Title: "Node staat op unschedulable (cordoned)",
				Fix: fmt.Sprintf("Zet hem terug met `oc adm uncordon %s` als het onderhoud klaar is.", name)})
		}
	}

	pools, err := s.client.Resource(gvrMCP).List(ctx, metav1.ListOptions{})
	if err != nil {
		return out, fmt.Errorf("machineconfigpools: %w", err)
	}
	for _, p := range pools.Items {
		name := p.GetName()
		res := "MachineConfigPool " + name
		conds := conditions(p.Object)
		if c := conds["Degraded"]; c.Status == "True" {
			out = append(out, Finding{Severity: Blocker, Resource: res, Title: withMessage("MachineConfigPool is Degraded", c),
				Fix: fmt.Sprintf("Bekijk `oc get mcp %s -o yaml` en de logs van machine-config-daemon op de betrokken node.", name)})
		}
		if paused, _, _ := unstructured.NestedBool(p.Object, "spec", "paused"); paused {
			out = append(out, Finding{Severity: Warning, Resource: res, Title: "MachineConfigPool staat op paused",
				Fix: fmt.Sprintf(`Nodes in deze pool krijgen de upgrade niet. Zet hem aan met: oc patch mcp %s --type=merge -p '{"spec":{"paused":false}}'`, name)})
		}
		if c := conds["Updated"]; c.Status != "True" {
			out = append(out, Finding{Severity: Warning, Resource: res, Title: withMessage("MachineConfigPool is nog niet Updated", c),
				Fix: "Wacht tot alle nodes in de pool de huidige configuratie hebben voordat je upgradet."})
		}
	}
	return out, nil
}

// ---------------------------------------------------------------- pdb

func checkPDBs(ctx context.Context, s *Scanner) ([]Finding, error) {
	list, err := s.client.Resource(gvrPDB).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	var out []Finding
	for _, pdb := range list.Items {
		ns := pdb.GetNamespace()
		if !s.includePlatform && isPlatformNamespace(ns) {
			continue
		}
		res := fmt.Sprintf("PodDisruptionBudget %s/%s", ns, pdb.GetName())
		expected, _, _ := unstructured.NestedInt64(pdb.Object, "status", "expectedPods")
		allowed, _, _ := unstructured.NestedInt64(pdb.Object, "status", "disruptionsAllowed")
		switch {
		case expected == 0:
			out = append(out, Finding{Severity: Warning, Resource: res, Title: "PDB matcht geen enkele pod",
				Fix: "Controleer de selector van de PDB, of verwijder de PDB als de workload niet meer bestaat."})
		case allowed == 0:
			out = append(out, Finding{Severity: Blocker, Resource: res,
				Title: fmt.Sprintf("PDB staat 0 disruptions toe (%d pods) en blokkeert node-drain", expected),
				Fix:   "Zet maxUnavailable: 1 in de PDB, of schaal de workload op naar meer replicas dan minAvailable."})
		}
	}
	return out, nil
}

// ---------------------------------------------------------------- deprecated_apis

func checkDeprecatedAPIs(ctx context.Context, s *Scanner) ([]Finding, error) {
	list, err := s.client.Resource(gvrAPIRequestCount).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	var out []Finding
	for _, arc := range list.Items {
		removed, _, _ := unstructured.NestedString(arc.Object, "status", "removedInRelease")
		count, _, _ := unstructured.NestedInt64(arc.Object, "status", "requestCount")
		if removed == "" || count == 0 {
			continue
		}
		kube, err := parseMinor(removed)
		if err != nil {
			continue
		}
		ocp := kubeToOCP(kube)
		sev := Warning
		// Onbekend doel: voorzichtig zijn en als blocker melden.
		if s.target == nil || !s.target.Less(ocp) {
			sev = Blocker
		}
		fix := "Migreer naar een nieuwere API-versie voordat je naar OpenShift " + ocp.String() + " gaat."
		if users := topUsers(arc.Object, 3); len(users) > 0 {
			fix += " Grootste gebruikers: " + strings.Join(users, ", ") + "."
		}
		out = append(out, Finding{Severity: sev, Resource: "APIRequestCount " + arc.GetName(),
			Title: fmt.Sprintf("Deprecated API in gebruik, verdwijnt in Kubernetes %s (OpenShift %s)", kube, ocp),
			Fix:   clip(fix)})
	}
	return out, nil
}

// topUsers telt status.last24h[].byNode[].byUser[] op per gebruiker.
func topUsers(obj map[string]interface{}, n int) []string {
	totals := map[string]int64{}
	hours, _, _ := unstructured.NestedSlice(obj, "status", "last24h")
	for _, h := range hours {
		hm, _ := h.(map[string]interface{})
		nodes, _, _ := unstructured.NestedSlice(hm, "byNode")
		for _, nd := range nodes {
			nm, _ := nd.(map[string]interface{})
			users, _, _ := unstructured.NestedSlice(nm, "byUser")
			for _, u := range users {
				um, _ := u.(map[string]interface{})
				name, _ := um["username"].(string)
				c, _, _ := unstructured.NestedInt64(um, "requestCount")
				if name != "" {
					totals[name] += c
				}
			}
		}
	}
	names := make([]string, 0, len(totals))
	for k := range totals {
		names = append(names, k)
	}
	sort.Slice(names, func(i, j int) bool {
		if totals[names[i]] != totals[names[j]] {
			return totals[names[i]] > totals[names[j]]
		}
		return names[i] < names[j]
	})
	if len(names) > n {
		names = names[:n]
	}
	return names
}

// ---------------------------------------------------------------- unsupported

func checkUnsupported(ctx context.Context, s *Scanner) ([]Finding, error) {
	var out []Finding
	var errs []error
	for _, r := range unsupportedOperatorResources {
		gvr := schema.GroupVersionResource{Group: "operator.openshift.io", Version: "v1", Resource: r}
		obj, err := s.client.Resource(gvr).Get(ctx, "cluster", metav1.GetOptions{})
		if err != nil {
			if !apierrors.IsNotFound(err) {
				errs = append(errs, fmt.Errorf("%s: %w", r, err))
			}
			continue
		}
		v, found, _ := unstructured.NestedFieldNoCopy(obj.Object, "spec", "unsupportedConfigOverrides")
		if !found || isEmpty(v) {
			continue
		}
		out = append(out, Finding{Severity: Blocker, Resource: obj.GetKind() + " cluster",
			Title: "unsupportedConfigOverrides is gezet",
			Fix:   fmt.Sprintf(`Niet ondersteund en blokkeert upgrades. Verwijder het: oc patch %s.operator.openshift.io cluster --type=merge -p '{"spec":{"unsupportedConfigOverrides":null}}'`, r)})
	}

	pods, err := s.client.Resource(gvrPod).List(ctx, metav1.ListOptions{FieldSelector: "status.phase=Running"})
	if err != nil {
		errs = append(errs, fmt.Errorf("pods: %w", err))
		return out, errors.Join(errs...)
	}
	for _, p := range pods.Items {
		if !s.includePlatform && isPlatformNamespace(p.GetNamespace()) {
			continue
		}
		// De fieldSelector filtert al, maar de fake client in de tests niet.
		if phase, _, _ := unstructured.NestedString(p.Object, "status", "phase"); phase != "Running" {
			continue
		}
		if len(p.GetOwnerReferences()) > 0 {
			continue
		}
		out = append(out, Finding{Severity: Warning, Resource: fmt.Sprintf("Pod %s/%s", p.GetNamespace(), p.GetName()),
			Title: "Losse pod zonder controller",
			Fix:   "Een node-drain verwijdert deze pod definitief. Beheer hem via een Deployment of StatefulSet."})
	}
	return out, errors.Join(errs...)
}

func isEmpty(v interface{}) bool {
	switch t := v.(type) {
	case nil:
		return true
	case map[string]interface{}:
		return len(t) == 0
	case []interface{}:
		return len(t) == 0
	case string:
		return t == ""
	}
	return false
}
