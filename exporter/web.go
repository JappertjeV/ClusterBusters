package main

import (
	_ "embed"
	"encoding/json"
	"net/http"
	"sort"
	"time"
)

// De KPN-dashboardpagina. Lettertype en logo zitten in het bestand zelf, dus de pagina heeft
// geen internet nodig.
//
//go:embed web/index.html
var indexHTML []byte

type apiFinding struct {
	Check    string `json:"check"`
	Category string `json:"category"`
	Severity string `json:"severity"`
	Resource string `json:"resource"`
	Title    string `json:"title"`
	Fix      string `json:"fix"`
}

type apiCheck struct {
	Name     string `json:"name"`
	Category string `json:"category"`
	Up       bool   `json:"up"`
}

type apiStatus struct {
	Ready          bool                      `json:"ready"`
	Scanned        bool                      `json:"scanned"`
	Score          int                       `json:"score"`
	LastScan       int64                     `json:"lastScan"`
	AgeSeconds     int64                     `json:"ageSeconds"`
	CurrentVersion string                    `json:"currentVersion"`
	TargetVersion  string                    `json:"targetVersion"`
	Findings       []apiFinding              `json:"findings"`
	Checks         []apiCheck                `json:"checks"`
	Categories     map[string]map[string]int `json:"categories"`
	History        []historyPoint            `json:"history"`
	Platform       *PlatformStatus           `json:"platform"`
}

var severityOrder = map[string]int{Blocker: 0, Warning: 1, Info: 2}

func buildStatus(r *Result, hist []historyPoint, now time.Time) apiStatus {
	st := apiStatus{Findings: []apiFinding{}, Checks: []apiCheck{}, Categories: map[string]map[string]int{}, History: hist}
	for _, cat := range categories() {
		st.Categories[cat] = map[string]int{Blocker: 0, Warning: 0, Info: 0}
	}
	if r == nil {
		for _, c := range checks {
			st.Checks = append(st.Checks, apiCheck{Name: c.name, Category: c.category})
		}
		return st
	}
	st.Scanned = true
	st.Ready, st.Score = r.Ready, r.Score
	st.LastScan = r.Timestamp.Unix()
	st.AgeSeconds = int64(now.Sub(r.Timestamp).Seconds())
	st.CurrentVersion, st.TargetVersion = r.CurrentVersion, r.TargetVersion
	st.Platform = r.Platform
	for _, f := range r.Findings {
		st.Findings = append(st.Findings, apiFinding(f))
		st.Categories[f.Category][f.Severity]++
	}
	// Ernstigste eerst, daarna op resource: zo staat bovenaan wat het eerst moet.
	sort.SliceStable(st.Findings, func(i, j int) bool {
		a, b := st.Findings[i], st.Findings[j]
		if severityOrder[a.Severity] != severityOrder[b.Severity] {
			return severityOrder[a.Severity] < severityOrder[b.Severity]
		}
		return a.Resource < b.Resource
	})
	for _, c := range checks {
		st.Checks = append(st.Checks, apiCheck{Name: c.name, Category: c.category, Up: r.CheckUp[c.name]})
	}
	return st
}

func registerWeb(mux *http.ServeMux, coll *collector) {
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		_, _ = w.Write(indexHTML)
	})
	mux.HandleFunc("/api/status", func(w http.ResponseWriter, _ *http.Request) {
		res, hist := coll.snapshot()
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(buildStatus(res, hist, time.Now()))
	})
}
