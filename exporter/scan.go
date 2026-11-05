package main

import (
	"context"
	"log/slog"
	"time"

	"k8s.io/client-go/dynamic"
)

const (
	Blocker = "blocker"
	Warning = "warning"
	Info    = "info"
)

var severities = []string{Blocker, Warning, Info}

// Finding is één ding dat vóór de upgrade gefixt (of bekeken) moet worden.
type Finding struct {
	Check    string
	Category string
	Severity string
	Resource string
	Title    string
	Fix      string
}

// Result is de uitkomst van één volledige scan. Het collector-deel serveert altijd het
// laatste complete Result, zodat Prometheus nooit een half bijgewerkte scan ziet.
type Result struct {
	Findings  []Finding
	CheckUp   map[string]bool
	Score     int
	Ready     bool
	Timestamp time.Time
}

type checkFunc func(ctx context.Context, s *Scanner) ([]Finding, error)

type check struct {
	name     string
	category string
	run      checkFunc
}

// Volgorde en namen liggen vast: ze komen terug als label check="..." in de metrics.
var checks = []check{
	{"clusterversion", "cluster", checkClusterVersion},
	{"operators", "operators", checkOperators},
	{"olm", "operators", checkOLM},
	{"nodes", "nodes", checkNodes},
	{"pdb", "pdb", checkPDBs},
	{"deprecated_apis", "deprecated_apis", checkDeprecatedAPIs},
	{"unsupported", "unsupported", checkUnsupported},
}

// categories bevat elke categorie één keer, zodat de collector ook nullen kan tonen.
func categories() []string {
	seen := map[string]bool{}
	var out []string
	for _, c := range checks {
		if !seen[c.category] {
			seen[c.category] = true
			out = append(out, c.category)
		}
	}
	return out
}

type Scanner struct {
	client          dynamic.Interface
	includePlatform bool
	targetOverride  string
	metrics         metricQuerier

	// Doelversie van de upgrade, per scan bepaald. nil als die onbekend is.
	target *minorVersion
}

func (s *Scanner) Scan(ctx context.Context) Result {
	s.target = s.resolveTarget(ctx)

	res := Result{CheckUp: map[string]bool{}, Timestamp: time.Now()}
	for _, c := range checks {
		findings, err := c.run(ctx, s)
		// Een fout in één check stopt de rest niet. Wat de check wel vond, tonen we ook.
		res.CheckUp[c.name] = err == nil
		if err != nil {
			slog.Error("check mislukt", "check", c.name, "err", err)
		}
		for _, f := range findings {
			f.Check = c.name
			f.Category = c.category
			res.Findings = append(res.Findings, f)
		}
	}
	res.Findings = dedupe(res.Findings)
	res.Score, res.Ready = score(res.Findings, res.CheckUp)
	return res
}

// score: 100 − 25 per blocker − 5 per warning, minimaal 0. Klaar als er geen blockers zijn
// en alle checks gelukt zijn.
func score(findings []Finding, up map[string]bool) (int, bool) {
	blockers, warnings := 0, 0
	for _, f := range findings {
		switch f.Severity {
		case Blocker:
			blockers++
		case Warning:
			warnings++
		}
	}
	sc := 100 - 25*blockers - 5*warnings
	if sc < 0 {
		sc = 0
	}
	ready := blockers == 0
	for _, ok := range up {
		if !ok {
			ready = false
		}
	}
	return sc, ready
}

// Twee bevindingen met precies dezelfde labels zouden de scrape laten falen
// ("collected before with the same name and label values").
func dedupe(in []Finding) []Finding {
	seen := map[Finding]bool{}
	out := in[:0]
	for _, f := range in {
		if !seen[f] {
			seen[f] = true
			out = append(out, f)
		}
	}
	return out
}
