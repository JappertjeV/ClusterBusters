package main

import (
	"fmt"
	"regexp"
	"strings"
)

// Geaccepteerde bevindingen: bekende punten waar het team bewust mee leeft, zoals de overrides die CRC
// zelf in de ClusterVersion zet. Ze blijven zichtbaar, maar als info, zodat ze score en ready niet raken.
//
// ACCEPTED_FINDINGS bevat reguliere expressies, gescheiden door ; of een nieuwe regel. Elke expressie
// wordt gezocht in "<check>/<resource>: <title>", bijvoorbeeld
// "clusterversion/ClusterVersion version: Cluster is not upgradeable: ... overrides ...".
func parseAccepted(v string) ([]*regexp.Regexp, error) {
	var out []*regexp.Regexp
	for _, part := range strings.FieldsFunc(v, func(r rune) bool { return r == ';' || r == '\n' }) {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		re, err := regexp.Compile(part)
		if err != nil {
			return nil, fmt.Errorf("ACCEPTED_FINDINGS: %q is geen geldige reguliere expressie: %w", part, err)
		}
		out = append(out, re)
	}
	return out, nil
}

func acceptKey(f Finding) string { return f.Check + "/" + f.Resource + ": " + f.Title }

// applyAccepted zet blockers en warnings die op een expressie passen om naar info.
func applyAccepted(findings []Finding, accepted []*regexp.Regexp) []Finding {
	if len(accepted) == 0 {
		return findings
	}
	for i, f := range findings {
		if f.Severity == Info {
			continue
		}
		key := acceptKey(f)
		for _, re := range accepted {
			if !re.MatchString(key) {
				continue
			}
			was := f.Severity
			f.Severity = Info
			f.Title = clip("Accepted " + was + ": " + f.Title)
			f.TitleNL = clip("Geaccepteerde " + was + ": " + orDefault(f.TitleNL, findings[i].Title))
			f.Fix = clip("Accepted via ACCEPTED_FINDINGS in base/exporter-deployment.yaml, so it does not count. Original fix: " + f.Fix)
			f.FixNL = clip("Geaccepteerd via ACCEPTED_FINDINGS in base/exporter-deployment.yaml, dus telt niet mee. Oorspronkelijke fix: " + orDefault(f.FixNL, findings[i].Fix))
			findings[i] = f
			break
		}
	}
	return findings
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}
