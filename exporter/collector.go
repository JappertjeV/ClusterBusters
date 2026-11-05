package main

import (
	"sync"

	"github.com/prometheus/client_golang/prometheus"
)

// collector serveert het laatste complete scanresultaat. Elke scan vervangt het resultaat in
// zijn geheel; dat heeft hetzelfde effect als Reset() op GaugeVecs (opgeloste bevindingen
// verdwijnen), maar een scrape halverwege een scan ziet nooit een half gevulde set.
type collector struct {
	mu  sync.RWMutex
	res *Result

	score, ready, findings, finding, checkUp, lastScan *prometheus.Desc
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
	}
}

func (c *collector) set(r Result) {
	c.mu.Lock()
	c.res = &r
	c.mu.Unlock()
}

func (c *collector) Describe(ch chan<- *prometheus.Desc) {
	for _, d := range []*prometheus.Desc{c.score, c.ready, c.findings, c.finding, c.checkUp, c.lastScan} {
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
