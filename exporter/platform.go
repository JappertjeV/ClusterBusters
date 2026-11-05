package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
)

// PlatformStatus: de ruwe platform-metrics uit Thanos, voor de pagina Platform. Die tellen ook de
// platform-namespaces mee die de checks overslaan, dus ze kunnen afwijken van de bevindingen.
type PlatformStatus struct {
	BlockingPDBs       int             `json:"blockingPDBs"`
	UnreadyNodes       int             `json:"unreadyNodes"`
	UnhealthyOperators int             `json:"unhealthyOperators"`
	DeprecatedAPIs     []DeprecatedAPI `json:"deprecatedAPIs"`
	Error              string          `json:"error,omitempty"`
}

type DeprecatedAPI struct {
	Group   string `json:"group"`
	Version string `json:"version"`
	Rsrc    string `json:"resource"`
	Removed string `json:"removedRelease"`
}

type scalarQuerier interface {
	QueryScalar(ctx context.Context, expr string) (float64, error)
}

// Dezelfde queries als de platform-panelen in base/perses-dashboard.yaml.
const (
	platformPDBQuery       = `count(kube_poddisruptionbudget_status_pod_disruptions_allowed == 0) or vector(0)`
	platformNodesQuery     = `count(kube_node_status_condition{condition="Ready", status!="true"} == 1) or vector(0)`
	platformOperatorsQuery = `count(group by (name) (cluster_operator_up == 0 or cluster_operator_conditions{condition="Degraded"} == 1 or cluster_operator_conditions{condition="Available"} == 0)) or vector(0)`
	platformDeprecated     = `count by (group, version, resource, removed_release) (apiserver_requested_deprecated_apis)`
)

// platformStatus faalt nooit hard: een fout komt in Error en de pagina toont dat.
func (s *Scanner) platformStatus(ctx context.Context) *PlatformStatus {
	sq, ok := s.metrics.(scalarQuerier)
	if s.metrics == nil || !ok {
		return nil
	}
	p := &PlatformStatus{DeprecatedAPIs: []DeprecatedAPI{}}
	var errs []error
	for _, q := range []struct {
		expr string
		dst  *int
	}{
		{platformPDBQuery, &p.BlockingPDBs},
		{platformNodesQuery, &p.UnreadyNodes},
		{platformOperatorsQuery, &p.UnhealthyOperators},
	} {
		v, err := sq.QueryScalar(ctx, q.expr)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		*q.dst = int(v)
	}
	series, err := s.metrics.Query(ctx, platformDeprecated)
	if err != nil {
		errs = append(errs, err)
	}
	for _, m := range series {
		p.DeprecatedAPIs = append(p.DeprecatedAPIs, DeprecatedAPI{Group: m["group"], Version: m["version"], Rsrc: m["resource"], Removed: m["removed_release"]})
	}
	sort.Slice(p.DeprecatedAPIs, func(i, j int) bool {
		a, b := p.DeprecatedAPIs[i], p.DeprecatedAPIs[j]
		return fmt.Sprint(a.Group, a.Version, a.Rsrc) < fmt.Sprint(b.Group, b.Version, b.Rsrc)
	})
	if err := errors.Join(errs...); err != nil {
		slog.Warn("platform-metrics niet volledig", "err", err)
		p.Error = err.Error()
	}
	return p
}
