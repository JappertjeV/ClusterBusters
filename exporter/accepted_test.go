package main

import (
	"context"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
)

// De waarde uit base/exporter-deployment.yaml, zoals de container hem krijgt.
const crcAccepted = `clusterversion/ClusterVersion version: Cluster is not upgradeable: .*overrides;deprecated_apis/.*deploymentconfigs\.v1\.apps\.openshift\.io:`

func TestAcceptedMakesCRCReady(t *testing.T) {
	acc, err := parseAccepted(crcAccepted)
	if err != nil || len(acc) != 2 {
		t.Fatalf("parse: %v %d", err, len(acc))
	}
	objs := healthyCluster()
	cv := objs[0].(*unstructured.Unstructured)
	list, _, _ := unstructured.NestedSlice(cv.Object, "status", "conditions")
	list = append(list, map[string]interface{}{"type": "Upgradeable", "status": "False", "reason": "ClusterVersionOverridesSet",
		"message": "Disabling ownership via cluster version overrides prevents upgrades between minor or major versions. Please remove overrides before requesting a minor or major version update."})
	_ = unstructured.SetNestedSlice(cv.Object, list, "status", "conditions")
	objs = append(objs, obj("apiserver.openshift.io/v1", "APIRequestCount", "", "deploymentconfigs.v1.apps.openshift.io", map[string]interface{}{
		"status": map[string]interface{}{"removedInRelease": "6.0", "requestCount": int64(3)}}))

	s := newScanner(t, objs...)
	res := s.Scan(context.Background())
	if res.Ready || res.Score != 70 {
		t.Fatalf("zonder acceptatie: score %d ready %v, verwacht 70 en niet klaar", res.Score, res.Ready)
	}

	s.accepted = acc
	res = s.Scan(context.Background())
	if !res.Ready || res.Score != 100 {
		t.Fatalf("met acceptatie: score %d ready %v, verwacht 100 en klaar: %+v", res.Score, res.Ready, res.Findings)
	}
	for _, f := range res.Findings {
		if f.Severity != Info || !strings.HasPrefix(f.Title, "Accepted ") || !strings.HasPrefix(f.TitleNL, "Geaccepteerde ") {
			t.Errorf("hoort zichtbaar als geaccepteerde info te blijven: %+v", f)
		}
	}
	if len(res.Findings) != 2 {
		t.Errorf("verwacht 2 geaccepteerde bevindingen, kreeg %d", len(res.Findings))
	}
}

// Een echte blocker mag niet onder de acceptatie vallen, ook niet van dezelfde ClusterVersion.
func TestAcceptedIsNarrow(t *testing.T) {
	acc, _ := parseAccepted(crcAccepted)
	objs := []runtime.Object{
		obj("config.openshift.io/v1", "ClusterVersion", "", "version", map[string]interface{}{
			"status": merge(conds("Failing", "True"), map[string]interface{}{"desired": map[string]interface{}{"version": "4.17.3"}}),
		}),
		obj("policy/v1", "PodDisruptionBudget", "clusterbusters-demo", "demo-app", map[string]interface{}{
			"status": map[string]interface{}{"expectedPods": int64(1), "disruptionsAllowed": int64(0)}}),
		obj("apiserver.openshift.io/v1", "APIRequestCount", "", "flowschemas.v1beta3.flowcontrol.apiserver.k8s.io", map[string]interface{}{
			"status": map[string]interface{}{"removedInRelease": "1.32", "requestCount": int64(3)}}),
	}
	s := newScanner(t, objs...)
	s.accepted = acc
	res := s.Scan(context.Background())
	for _, f := range res.Findings {
		if strings.HasPrefix(f.Title, "Accepted") {
			t.Errorf("onterecht geaccepteerd: %s", acceptKey(f))
		}
	}
	if res.Ready {
		t.Error("met een Failing cluster en een blokkerende PDB mag het nooit klaar zijn")
	}
}

func TestAcceptedBadRegex(t *testing.T) {
	if _, err := parseAccepted("goed;(kapot"); err == nil {
		t.Error("ongeldige expressie hoort een fout te geven")
	}
	if acc, err := parseAccepted(" ; \n "); err != nil || len(acc) != 0 {
		t.Errorf("lege waarde: %v %d", err, len(acc))
	}
}
