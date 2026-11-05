package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"

	"k8s.io/client-go/rest"
)

const (
	defaultThanosURL = "https://thanos-querier.openshift-monitoring.svc:9091"

	// OpenShift zet dit bestand in elke pod. Thanos gebruikt de service-CA, niet de CA van de
	// API-server die in rest.Config staat; die laatste geeft "certificate signed by unknown authority".
	serviceCAFile = "/var/run/secrets/kubernetes.io/serviceaccount/service-ca.crt"
)

// metricQuerier voert een instant query uit en geeft de labels van elke serie terug.
type metricQuerier interface {
	Query(ctx context.Context, expr string) ([]map[string]string, error)
}

type thanosClient struct {
	baseURL string
	http    *http.Client
}

// newThanosClient gebruikt de token uit cfg. Via rest.HTTPClientFor leest client-go een
// geroteerde ServiceAccount-token vanzelf opnieuw in.
func newThanosClient(cfg *rest.Config, baseURL, caFile string) (*thanosClient, error) {
	c := rest.CopyConfig(cfg)
	if _, err := os.Stat(caFile); err == nil {
		c.TLSClientConfig.CAFile = caFile
		c.TLSClientConfig.CAData = nil
	}
	hc, err := rest.HTTPClientFor(c)
	if err != nil {
		return nil, fmt.Errorf("http-client voor Thanos: %w", err)
	}
	return &thanosClient{baseURL: strings.TrimRight(baseURL, "/"), http: hc}, nil
}

func (t *thanosClient) Query(ctx context.Context, expr string) ([]map[string]string, error) {
	// partial_response=false: met een halve antwoordset zouden we onterecht "niets gevonden" melden.
	q := url.Values{"query": {expr}, "partial_response": {"false"}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, t.baseURL+"/api/v1/query?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	resp, err := t.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Thanos gaf HTTP %d: %s", resp.StatusCode, clip(string(body)))
	}
	var parsed struct {
		Status string `json:"status"`
		Error  string `json:"error"`
		Data   struct {
			Result []struct {
				Metric map[string]string `json:"metric"`
			} `json:"result"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("antwoord van Thanos onleesbaar: %w", err)
	}
	if parsed.Status != "success" {
		return nil, fmt.Errorf("Thanos gaf status %q: %s", parsed.Status, parsed.Error)
	}
	out := make([]map[string]string, 0, len(parsed.Data.Result))
	for _, r := range parsed.Data.Result {
		out = append(out, r.Metric)
	}
	return out, nil
}
