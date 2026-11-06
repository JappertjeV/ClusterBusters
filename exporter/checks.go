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
	gvrSubscription    = schema.GroupVersionResource{Group: "operators.coreos.com", Version: "v1alpha1", Resource: "subscriptions"}
	gvrInstallPlan     = schema.GroupVersionResource{Group: "operators.coreos.com", Version: "v1alpha1", Resource: "installplans"}
)

// Operator-configuraties (naam "cluster") die spec.unsupportedConfigOverrides kennen.
// Houd dit gelijk met de ClusterRole in base/exporter-rbac.yaml.
var unsupportedOperatorResources = []string{
	"kubeapiservers", "kubecontrollermanagers", "kubeschedulers", "etcds",
	"openshiftapiservers", "authentications", "networks", "consoles",
}

// Prometheus-labels worden onleesbaar in een tabel als ze te lang zijn. Ruim genoeg dat een
// commando aan het eind van een fix-tekst niet wordt afgekapt.
func clip(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	const max = 400
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
			Title:   withMessage("Cluster version is Failing", c),
			Fix:     "Resolve the current error first: see `oc adm upgrade` and `oc get clusterversion version -o yaml`.",
			TitleNL: withMessage("Cluster-versie staat op Failing", c),
			FixNL:   "Los eerst de huidige fout op: zie `oc adm upgrade` en `oc get clusterversion version -o yaml`."})
	}
	if c := conds["Upgradeable"]; c.Status == "False" {
		out = append(out, Finding{Severity: Blocker, Resource: res,
			Title:   withMessage("Cluster is not upgradeable", c),
			Fix:     "Resolve the cause in the Upgradeable condition. This is usually an Operator with Upgradeable=False; see the operators findings.",
			TitleNL: withMessage("Cluster is niet upgradeable", c),
			FixNL:   "Los de oorzaak uit de Upgradeable-condition op. Meestal is dat een Operator met Upgradeable=False, zie de operators-bevindingen."})
	}
	if c := conds["Progressing"]; c.Status == "True" {
		out = append(out, Finding{Severity: Warning, Resource: res,
			Title:   withMessage("An update is already in progress", c),
			Fix:     "Wait until the running update has finished before you start a new one.",
			TitleNL: withMessage("Er loopt al een update", c),
			FixNL:   "Wacht tot de lopende update klaar is voordat je een nieuwe start."})
	}
	// Info en geen warning: op een afgeschermd cluster staat dit altijd op False.
	if c := conds["RetrievedUpdates"]; c.Status == "False" {
		out = append(out, Finding{Severity: Info, Resource: res,
			Title:   withMessage("Cluster cannot retrieve available updates", c),
			Fix:     "Set an update channel (" + channelCommand(s.current) + ") and check that the update service is reachable. Without this, the cluster sees no updates.",
			TitleNL: withMessage("Cluster kan geen beschikbare updates ophalen", c),
			FixNL:   "Stel een update-channel in (" + channelCommand(s.current) + ") en controleer of de update-service bereikbaar is. Zonder dit ziet het cluster geen updates."})
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
			fix := "Assess whether this risk applies to this cluster before you move to " + version + "."
			fixNL := "Beoordeel of dit risico voor dit cluster geldt voordat je naar " + version + " gaat."
			if url != "" {
				fix += " See " + url
				fixNL += " Zie " + url
			}
			out = append(out, Finding{Severity: Info, Resource: res,
				Title:   clip(fmt.Sprintf("Known risk %s for update to %s: %s", name, version, msg)),
				Fix:     clip(fix),
				TitleNL: clip(fmt.Sprintf("Bekend risico %s voor update naar %s: %s", name, version, msg)),
				FixNL:   clip(fixNL)})
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
			Title:   clip("Admin gate not acknowledged: " + key + ": " + gateData[key]),
			Fix:     clip(fmt.Sprintf(`Read the gate, resolve what it says and acknowledge it: oc -n openshift-config patch cm admin-acks --type=merge -p "data: {%s: 'true'}"`, key)),
			TitleNL: clip("Admin-gate niet bevestigd: " + key + ": " + gateData[key]),
			FixNL:   clip(fmt.Sprintf(`Lees de gate, los op wat erin staat en bevestig: oc -n openshift-config patch cm admin-acks --type=merge -p "data: {%s: 'true'}"`, key))})
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
		fix := fmt.Sprintf("Check `oc get co %s -o yaml` and the pods of this Operator; resolve the cause before you upgrade.", name)
		fixNL := fmt.Sprintf("Bekijk `oc get co %s -o yaml` en de pods van deze Operator; los de oorzaak op voordat je upgradet.", name)
		conds := conditions(co.Object)
		if c := conds["Available"]; c.Status != "True" {
			out = append(out, Finding{Severity: Blocker, Resource: res, Title: withMessage("Operator is not Available", c), Fix: fix,
				TitleNL: withMessage("Operator is niet Available", c), FixNL: fixNL})
		}
		if c := conds["Degraded"]; c.Status == "True" {
			out = append(out, Finding{Severity: Blocker, Resource: res, Title: withMessage("Operator is Degraded", c), Fix: fix,
				TitleNL: withMessage("Operator is Degraded", c), FixNL: fixNL})
		}
		if c := conds["Upgradeable"]; c.Status == "False" {
			out = append(out, Finding{Severity: Blocker, Resource: res, Title: withMessage("Operator blocks upgrades (Upgradeable=False)", c), Fix: fix,
				TitleNL: withMessage("Operator blokkeert upgrades (Upgradeable=False)", c), FixNL: fixNL})
		}
		if c := conds["Progressing"]; c.Status == "True" {
			out = append(out, Finding{Severity: Warning, Resource: res, Title: withMessage("Operator is still rolling out (Progressing)", c),
				Fix:     "Wait until the Operator has finished rolling out.",
				TitleNL: withMessage("Operator is nog bezig (Progressing)", c),
				FixNL:   "Wacht tot de Operator klaar is met uitrollen."})
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
				Title:   withMessage("Operator installation is in phase "+orUnknown(phase), condition{Message: msg}),
				Fix:     fmt.Sprintf("Check `oc -n %s get csv %s -o yaml` and the InstallPlan; repair or reinstall the Operator.", csv.GetNamespace(), csv.GetName()),
				TitleNL: withMessage("Operator-installatie staat op phase "+orUnknownNL(phase), condition{Message: msg}),
				FixNL:   fmt.Sprintf("Bekijk `oc -n %s get csv %s -o yaml` en de InstallPlan; herstel of herinstalleer de Operator.", csv.GetNamespace(), csv.GetName())})
		}
		if s.target == nil {
			continue
		}
		maxV, ok := maxOpenShiftVersion(csv.GetAnnotations()["operatorframework.io/properties"])
		if ok && maxV.Less(*s.target) {
			out = append(out, Finding{Severity: Blocker, Resource: res,
				Title:   fmt.Sprintf("Operator supports at most OpenShift %s, target is %s", maxV, s.target),
				Fix:     "Update this Operator to a version that supports " + s.target.String() + " (switch channel if needed), or remove it.",
				TitleNL: fmt.Sprintf("Operator ondersteunt maximaal OpenShift %s, doel is %s", maxV, s.target),
				FixNL:   "Update deze Operator naar een versie die " + s.target.String() + " ondersteunt (wissel zo nodig van channel), of verwijder hem."})
		}
	}

	// Operator-updates die vastzitten. Een fout hier laat de check falen, maar wat we over de
	// CSV's vonden blijft staan.
	var errs []error
	subs, err := subscriptionFindings(ctx, s)
	if err != nil {
		errs = append(errs, fmt.Errorf("subscriptions: %w", err))
	}
	plans, err := installPlanFindings(ctx, s)
	if err != nil {
		errs = append(errs, fmt.Errorf("installplans: %w", err))
	}
	out = append(append(out, subs...), plans...)
	return out, errors.Join(errs...)
}

// subscriptionFindings: OLM kan voor deze Operator geen (nieuwe) versie vinden of installeren.
func subscriptionFindings(ctx context.Context, s *Scanner) ([]Finding, error) {
	list, err := s.client.Resource(gvrSubscription).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	var out []Finding
	for _, sub := range list.Items {
		ns, name := sub.GetNamespace(), sub.GetName()
		res := fmt.Sprintf("Subscription %s/%s", ns, name)
		conds := conditions(sub.Object)
		if c := conds["ResolutionFailed"]; c.Status == "True" {
			out = append(out, Finding{Severity: Warning, Resource: res,
				Title:   withMessage("Operator update cannot be resolved (ResolutionFailed)", c),
				Fix:     fmt.Sprintf("Check the package, channel and CatalogSource of this Subscription (`oc -n %s get sub %s -o yaml`). As long as this fails, the Operator gets no updates.", ns, name),
				TitleNL: withMessage("Operator-update kan niet worden opgelost (ResolutionFailed)", c),
				FixNL:   fmt.Sprintf("Controleer package, channel en CatalogSource van deze Subscription (`oc -n %s get sub %s -o yaml`). Zolang dit faalt krijgt de Operator geen updates.", ns, name)})
		}
		if c := conds["CatalogSourcesUnhealthy"]; c.Status == "True" {
			out = append(out, Finding{Severity: Warning, Resource: res,
				Title:   withMessage("CatalogSource of this Operator is unhealthy", c),
				Fix:     "Check `oc get catalogsource -n openshift-marketplace` and the pods there; without a catalog, no Operator updates come in.",
				TitleNL: withMessage("CatalogSource van deze Operator is niet gezond", c),
				FixNL:   "Bekijk `oc get catalogsource -n openshift-marketplace` en de pods daar; zonder catalogus komen er geen Operator-updates binnen."})
		}
		if c := conds["InstallPlanFailed"]; c.Status == "True" {
			out = append(out, Finding{Severity: Blocker, Resource: res,
				Title:   withMessage("Installation of the Operator update failed", c),
				Fix:     fmt.Sprintf("Check the InstallPlan in namespace %s (`oc -n %s get installplan`) and remove or repair it.", ns, ns),
				TitleNL: withMessage("Installatie van de Operator-update is mislukt", c),
				FixNL:   fmt.Sprintf("Bekijk de InstallPlan in namespace %s (`oc -n %s get installplan`) en verwijder of herstel hem.", ns, ns)})
		}
	}
	return out, nil
}

// installPlanFindings: een Operator-update staat klaar maar wacht op handmatige goedkeuring.
func installPlanFindings(ctx context.Context, s *Scanner) ([]Finding, error) {
	list, err := s.client.Resource(gvrInstallPlan).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	var out []Finding
	for _, ip := range list.Items {
		phase, _, _ := unstructured.NestedString(ip.Object, "status", "phase")
		approved, _, _ := unstructured.NestedBool(ip.Object, "spec", "approved")
		if phase != "RequiresApproval" || approved {
			continue
		}
		ns, name := ip.GetNamespace(), ip.GetName()
		csvs, _, _ := unstructured.NestedStringSlice(ip.Object, "spec", "clusterServiceVersionNames")
		out = append(out, Finding{Severity: Warning, Resource: fmt.Sprintf("InstallPlan %s/%s", ns, name),
			Title:   clip("Operator update is waiting for manual approval: " + strings.Join(csvs, ", ")),
			Fix:     clip(fmt.Sprintf(`Update Operators before the cluster upgrade. Approve in the console (Operators → Installed Operators) or with: oc -n %s patch installplan %s --type=merge -p "spec: {approved: true}"`, ns, name)),
			TitleNL: clip("Operator-update wacht op handmatige goedkeuring: " + strings.Join(csvs, ", ")),
			FixNL:   clip(fmt.Sprintf(`Werk Operators bij vóór de cluster-upgrade. Keur goed in de console (Operators → Installed Operators) of met: oc -n %s patch installplan %s --type=merge -p "spec: {approved: true}"`, ns, name))})
	}
	return out, nil
}

func orUnknown(s string) string {
	if s == "" {
		return "unknown"
	}
	return s
}

func orUnknownNL(s string) string {
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
			out = append(out, Finding{Severity: Blocker, Resource: res, Title: withMessage("Node is not Ready", c),
				Fix:     fmt.Sprintf("Investigate the node with `oc describe node %s` and `oc adm node-logs %s -u kubelet`.", name, name),
				TitleNL: withMessage("Node is niet Ready", c),
				FixNL:   fmt.Sprintf("Onderzoek de node met `oc describe node %s` en `oc adm node-logs %s -u kubelet`.", name, name)})
		}
		for _, p := range []string{"MemoryPressure", "DiskPressure", "PIDPressure"} {
			if c := conds[p]; c.Status == "True" {
				out = append(out, Finding{Severity: Warning, Resource: res, Title: withMessage("Node has "+p, c),
					Fix:     "Free up resources or move workloads; otherwise a drain during the upgrade can get stuck.",
					TitleNL: withMessage("Node heeft "+p, c),
					FixNL:   "Maak ruimte vrij of verplaats workloads; een drain tijdens de upgrade kan anders vastlopen."})
			}
		}
		if u, _, _ := unstructured.NestedBool(n.Object, "spec", "unschedulable"); u {
			out = append(out, Finding{Severity: Warning, Resource: res, Title: "Node is unschedulable (cordoned)",
				Fix:     fmt.Sprintf("Bring it back with `oc adm uncordon %s` when maintenance is done.", name),
				TitleNL: "Node staat op unschedulable (cordoned)",
				FixNL:   fmt.Sprintf("Zet hem terug met `oc adm uncordon %s` als het onderhoud klaar is.", name)})
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
				Fix:     fmt.Sprintf("Check `oc get mcp %s -o yaml` and the machine-config-daemon logs on the affected node.", name),
				TitleNL: withMessage("MachineConfigPool is Degraded", c),
				FixNL:   fmt.Sprintf("Bekijk `oc get mcp %s -o yaml` en de logs van machine-config-daemon op de betrokken node.", name)})
		}
		if paused, _, _ := unstructured.NestedBool(p.Object, "spec", "paused"); paused {
			out = append(out, Finding{Severity: Warning, Resource: res, Title: "MachineConfigPool is paused",
				Fix:     fmt.Sprintf(`Nodes in this pool will not get the upgrade. Resume it with: oc patch mcp %s --type=merge -p "spec: {paused: false}"`, name),
				TitleNL: "MachineConfigPool staat op paused",
				FixNL:   fmt.Sprintf(`Nodes in deze pool krijgen de upgrade niet. Zet hem aan met: oc patch mcp %s --type=merge -p "spec: {paused: false}"`, name)})
		}
		if c := conds["Updated"]; c.Status != "True" {
			out = append(out, Finding{Severity: Warning, Resource: res, Title: withMessage("MachineConfigPool is not Updated yet", c),
				Fix:     "Wait until all nodes in the pool have the current configuration before you upgrade.",
				TitleNL: withMessage("MachineConfigPool is nog niet Updated", c),
				FixNL:   "Wacht tot alle nodes in de pool de huidige configuratie hebben voordat je upgradet."})
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
		name := pdb.GetName()
		res := fmt.Sprintf("PodDisruptionBudget %s/%s", ns, name)
		expected, _, _ := unstructured.NestedInt64(pdb.Object, "status", "expectedPods")
		allowed, _, _ := unstructured.NestedInt64(pdb.Object, "status", "disruptionsAllowed")
		switch {
		case expected == 0:
			out = append(out, Finding{Severity: Warning, Resource: res, Title: "PDB does not match any pod",
				Fix:     fmt.Sprintf("Check the selector of the PDB, or remove it if the workload no longer exists: oc delete pdb %s -n %s", name, ns),
				TitleNL: "PDB matcht geen enkele pod",
				FixNL:   fmt.Sprintf("Controleer de selector van de PDB, of verwijder hem als de workload niet meer bestaat: oc delete pdb %s -n %s", name, ns)})
		case allowed == 0:
			out = append(out, Finding{Severity: Blocker, Resource: res,
				Title:   fmt.Sprintf("PDB allows 0 disruptions (%d pods) and blocks node drain", expected),
				Fix:     fmt.Sprintf(`Set maxUnavailable: 1 in the PDB, or scale the workload to more replicas than minAvailable. With: oc patch pdb %s -n %s --type=merge -p "spec: {minAvailable: null, maxUnavailable: 1}"`, name, ns),
				TitleNL: fmt.Sprintf("PDB staat 0 disruptions toe (%d pods) en blokkeert node-drain", expected),
				FixNL:   fmt.Sprintf(`Zet maxUnavailable: 1 in de PDB, of schaal de workload op naar meer replicas dan minAvailable. Met: oc patch pdb %s -n %s --type=merge -p "spec: {minAvailable: null, maxUnavailable: 1}"`, name, ns)})
		}
	}
	return out, nil
}

// ---------------------------------------------------------------- deprecated_apis

const (
	// Een gauge die op 1 staat zodra de API is aangeroepen sinds de start van die API-server.
	deprecatedAPIQuery = `max by (group, version, resource, removed_release) (apiserver_requested_deprecated_apis)`
	apiserverUpQuery   = `up{job="apiserver"} == 1`
)

// checkDeprecatedAPIs gebruikt twee bronnen. APIRequestCount noemt ook de gebruikers, maar was op
// crc03 helemaal leeg, zodat de check op OK stond terwijl het platform 6 verouderde API's zag.
// De metric vangt dat op. Een bron die faalt, laat de check falen in plaats van 0 te melden.
func checkDeprecatedAPIs(ctx context.Context, s *Scanner) ([]Finding, error) {
	var errs []error
	out, seen, err := deprecatedFromRequestCounts(ctx, s)
	if err != nil {
		errs = append(errs, fmt.Errorf("apirequestcounts: %w", err))
	}
	if s.metrics != nil {
		more, err := deprecatedFromMetrics(ctx, s, seen)
		if err != nil {
			errs = append(errs, fmt.Errorf("thanos: %w", err))
		}
		out = append(out, more...)
	}
	return out, errors.Join(errs...)
}

func deprecatedFromRequestCounts(ctx context.Context, s *Scanner) ([]Finding, map[string]bool, error) {
	seen := map[string]bool{}
	list, err := s.client.Resource(gvrAPIRequestCount).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, seen, err
	}
	var out []Finding
	for _, arc := range list.Items {
		removed, _, _ := unstructured.NestedString(arc.Object, "status", "removedInRelease")
		count, _, _ := unstructured.NestedInt64(arc.Object, "status", "requestCount")
		if removed == "" || count == 0 {
			continue
		}
		release, err := parseMinor(removed)
		if err != nil {
			continue
		}
		seen[arc.GetName()] = true
		ocp := removalInOCP(release)
		fix := "Migrate to a newer API version before you move to OpenShift " + ocp.String() + "."
		fixNL := "Migreer naar een nieuwere API-versie voordat je naar OpenShift " + ocp.String() + " gaat."
		if users := topUsers(arc.Object, 3); len(users) > 0 {
			fix += " Top users: " + strings.Join(users, ", ") + "."
			fixNL += " Grootste gebruikers: " + strings.Join(users, ", ") + "."
		}
		out = append(out, Finding{Severity: removalSeverity(s, ocp), Resource: "APIRequestCount " + arc.GetName(),
			Title:   "Deprecated API in use, removed in " + removalText(release),
			Fix:     clip(fix),
			TitleNL: "Deprecated API in gebruik, verdwijnt in " + removalText(release),
			FixNL:   clip(fixNL)})
	}
	return out, seen, nil
}

func deprecatedFromMetrics(ctx context.Context, s *Scanner, seen map[string]bool) ([]Finding, error) {
	// Zonder scrape van de API-server is een leeg antwoord geen bewijs dat er niets speelt.
	up, err := s.metrics.Query(ctx, apiserverUpQuery)
	if err != nil {
		return nil, err
	}
	if len(up) == 0 {
		return nil, errors.New("Prometheus heeft geen data van de API-server, dus er is geen oordeel mogelijk")
	}
	series, err := s.metrics.Query(ctx, deprecatedAPIQuery)
	if err != nil {
		return nil, err
	}
	const (
		findUsers   = " This source does not name users: look for them in the audit logs of the API server."
		findUsersNL = " Deze bron noemt geen gebruikers: zoek ze in de audit-logs van de API-server."
	)
	var out []Finding
	for _, m := range series {
		// Zelfde opbouw als de naam van een APIRequestCount: resource.versie.groep, zonder groep bij core.
		name := m["resource"] + "." + m["version"]
		if m["group"] != "" {
			name += "." + m["group"]
		}
		if seen[name] {
			continue
		}
		seen[name] = true
		f := Finding{Severity: Info, Resource: "API " + name,
			Title:   "Deprecated API in use, removal version not known yet",
			Fix:     clip("Plan the migration to a newer API version." + findUsers),
			TitleNL: "Deprecated API in gebruik, nog geen verwijderversie bekend",
			FixNL:   clip("Plan de migratie naar een nieuwere API-versie." + findUsersNL)}
		if release, err := parseMinor(m["removed_release"]); err == nil {
			ocp := removalInOCP(release)
			f.Severity = removalSeverity(s, ocp)
			f.Title = "Deprecated API in use, removed in " + removalText(release)
			f.Fix = clip("Migrate to a newer API version before you move to OpenShift " + ocp.String() + "." + findUsers)
			f.TitleNL = "Deprecated API in gebruik, verdwijnt in " + removalText(release)
			f.FixNL = clip("Migreer naar een nieuwere API-versie voordat je naar OpenShift " + ocp.String() + " gaat." + findUsersNL)
		}
		out = append(out, f)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Resource < out[j].Resource })
	return out, nil
}

// removalInOCP: alleen 1.x is een Kubernetes-versie. Op crc03 staat bij DeploymentConfig "6.0",
// een OpenShift-release; kubeToOCP zou daar 4.-13 van maken en dus een onterechte blocker.
func removalInOCP(release minorVersion) minorVersion {
	if release.Major == 1 {
		return kubeToOCP(release)
	}
	return release
}

func removalText(release minorVersion) string {
	if release.Major == 1 {
		return fmt.Sprintf("Kubernetes %s (OpenShift %s)", release, kubeToOCP(release))
	}
	return "OpenShift " + release.String()
}

func removalSeverity(s *Scanner, ocp minorVersion) string {
	// Onbekend doel: voorzichtig zijn en als blocker melden.
	if s.target == nil || !s.target.Less(ocp) {
		return Blocker
	}
	return Warning
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
			Title:   "unsupportedConfigOverrides is set",
			Fix:     fmt.Sprintf(`Not supported and blocks upgrades. Remove it: oc patch %s.operator.openshift.io cluster --type=merge -p "spec: {unsupportedConfigOverrides: null}"`, r),
			TitleNL: "unsupportedConfigOverrides is gezet",
			FixNL:   fmt.Sprintf(`Niet ondersteund en blokkeert upgrades. Verwijder het: oc patch %s.operator.openshift.io cluster --type=merge -p "spec: {unsupportedConfigOverrides: null}"`, r)})
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
			Title:   "Standalone pod without a controller",
			Fix:     "A node drain removes this pod permanently. Manage it through a Deployment or StatefulSet.",
			TitleNL: "Losse pod zonder controller",
			FixNL:   "Een node-drain verwijdert deze pod definitief. Beheer hem via een Deployment of StatefulSet."})
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

// channelCommand: het commando om het update-channel op de huidige minor te zetten, zodat het niet
// aan één OpenShift-versie vastzit. Bij een onbekende versie een plaatshouder.
func channelCommand(current string) string {
	if v, err := parseMinor(current); err == nil {
		return fmt.Sprintf("`oc adm upgrade channel stable-%s`", v)
	}
	return "`oc adm upgrade channel stable-<major.minor>`"
}
