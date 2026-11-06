package main

import (
	"sync"

	"github.com/prometheus/client_golang/prometheus"
)

// collector serveert het laatste complete scanresultaat. Elke scan vervangt het resultaat in
// zijn geheel; dat heeft hetzelfde effect als Reset() op GaugeVecs (opgeloste bevindingen
// verdwijnen), maar een scrape halverwege een scan ziet nooit een half gevulde set.
type collector struct {
	mu   sync.RWMutex
	res  *Result
	hist []historyPoint

	score, ready, findings, finding, checkUp, lastScan, version *prometheus.Desc
}

func newCollector() *collector {
	return &collector{
		score: prometheus.NewDesc("upgrade_readiness_score",
			"Readiness-score 0-100: 100 min 25 per blocker en 5 per warning.", nil, nil),
		ready: prometheus.NewDesc("upgrade_readiness_ready",
			"1 als er geen blockers en geen falende checks zijn, anders 0.", nil, nil),
		findings: prometheus.NewDesc("upgrade_readiness_findings",
			"Aantal bevindingen per categorie en severity.", []string{"category", "severity"}, nil),
		finding: prometheus.NewDesc("upgrade_readiness_finding",
			"Eén bevinding; altijd 1, de uitleg staat in de labels.",
			[]string{"check", "category", "severity", "resource", "title", "fix"}, nil),
		checkUp: prometheus.NewDesc("upgrade_readiness_check_up",
			"1 als de check gelukt is, 0 bij een fout (bijvoorbeeld geen rechten).", []string{"check"}, nil),
		lastScan: prometheus.NewDesc("upgrade_readiness_last_scan_timestamp_seconds",
			"Unix-tijd van de laatste afgeronde scan.", nil, nil),
		version: prometheus.NewDesc("upgrade_readiness_version_info",
			"Altijd 1. De huidige clusterversie en de doelversie waarop de checks beoordeeld zijn.",
			[]string{"current_version", "target_version"}, nil),
	}
}

// Scores van de laatste scans voor de grafiek op de webpagina. Alleen in het geheugen: na een
// herstart van de pod begint de grafiek opnieuw. Langere historie staat in Prometheus.
const maxHistory = 180

type historyPoint struct {
	Time     int64 `json:"t"`
	Score    int   `json:"score"`
	Blockers int   `json:"blockers"`
	Warnings int   `json:"warnings"`
}

func (c *collector) set(r Result) {
	p := historyPoint{Time: r.Timestamp.Unix(), Score: r.Score}
	for _, f := range r.Findings {
		switch f.Severity {
		case Blocker:
			p.Blockers++
		case Warning:
			p.Warnings++
		}
	}
	c.mu.Lock()
	c.res = &r
	c.hist = append(c.hist, p)
	if len(c.hist) > maxHistory {
		c.hist = append([]historyPoint(nil), c.hist[len(c.hist)-maxHistory:]...)
	}
	c.mu.Unlock()
}

// snapshot geeft het laatste resultaat (nil als er nog geen scan klaar is) en een kopie van de historie.
func (c *collector) snapshot() (*Result, []historyPoint) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.res, append([]historyPoint{}, c.hist...)
}

func (c *collector) Describe(ch chan<- *prometheus.Desc) {
	for _, d := range []*prometheus.Desc{c.score, c.ready, c.findings, c.finding, c.checkUp, c.lastScan, c.version} {
		ch <- d
	}
}

func (c *collector) Collect(ch chan<- prometheus.Metric) {
	c.mu.RLock()
	r := c.res
	c.mu.RUnlock()
	if r == nil {
		return // nog geen scan klaar
	}

	ready := 0.0
	if r.Ready {
		ready = 1
	}
	ch <- prometheus.MustNewConstMetric(c.score, prometheus.GaugeValue, float64(r.Score))
	ch <- prometheus.MustNewConstMetric(c.ready, prometheus.GaugeValue, ready)
	ch <- prometheus.MustNewConstMetric(c.lastScan, prometheus.GaugeValue, float64(r.Timestamp.Unix()))
	ch <- prometheus.MustNewConstMetric(c.version, prometheus.GaugeValue, 1,
		orUnknown(r.CurrentVersion), orUnknown(r.TargetVersion))

	// Ook nullen tonen, zodat sum(...{severity="blocker"}) 0 geeft in plaats van niets.
	counts := map[[2]string]int{}
	for _, f := range r.Findings {
		counts[[2]string{f.Category, f.Severity}]++
	}
	for _, cat := range categories() {
		for _, sev := range severities {
			ch <- prometheus.MustNewConstMetric(c.findings, prometheus.GaugeValue,
				float64(counts[[2]string{cat, sev}]), cat, sev)
		}
	}

	for _, f := range r.Findings {
		ch <- prometheus.MustNewConstMetric(c.finding, prometheus.GaugeValue, 1,
			f.Check, f.Category, f.Severity, f.Resource, f.Title, f.Fix)
	}

	for _, chk := range checks {
		up := 0.0
		if r.CheckUp[chk.name] {
			up = 1
		}
		ch <- prometheus.MustNewConstMetric(c.checkUp, prometheus.GaugeValue, up, chk.name)
	}
}
