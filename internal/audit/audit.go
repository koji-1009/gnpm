// Package audit queries the npm bulk advisory endpoint for the resolved
// dependency tree (doc/spec.md §7).
package audit

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"

	"github.com/koji-1009/gnpm/internal/lockfile"
	"github.com/koji-1009/gnpm/internal/npmrc"
	"github.com/koji-1009/gnpm/internal/semver"
)

// Severity is an advisory severity, ordered low→high.
type Severity int

const (
	SevUnknown Severity = iota
	SevInfo
	SevLow
	SevModerate
	SevHigh
	SevCritical
)

// ParseSeverity maps a severity string to a Severity.
func ParseSeverity(s string) Severity {
	switch strings.ToLower(s) {
	case "info":
		return SevInfo
	case "low":
		return SevLow
	case "moderate":
		return SevModerate
	case "high":
		return SevHigh
	case "critical":
		return SevCritical
	default:
		return SevUnknown
	}
}

func (s Severity) String() string {
	switch s {
	case SevInfo:
		return "info"
	case SevLow:
		return "low"
	case SevModerate:
		return "moderate"
	case SevHigh:
		return "high"
	case SevCritical:
		return "critical"
	default:
		return "unknown"
	}
}

// AtLeast reports whether s is at least level.
func (s Severity) AtLeast(level Severity) bool { return s >= level }

// Advisory is one advisory from the registry.
type Advisory struct {
	ID                 string
	Title              string
	Severity           string
	URL                string
	VulnerableVersions string
	PatchedVersions    string
}

// Finding pairs an installed package version with a matching advisory.
type Finding struct {
	Package   string
	Installed string
	Advisory  Advisory
}

// Report is the audit outcome.
type Report struct {
	Findings []Finding
	Errors   []string
}

// CountsBySeverity tallies findings per severity.
func (r *Report) CountsBySeverity() map[string]int {
	out := map[string]int{}
	for _, f := range r.Findings {
		out[strings.ToLower(f.Advisory.Severity)]++
	}
	return out
}

// MaxSeverity returns the highest severity among findings.
func (r *Report) MaxSeverity() Severity {
	max := SevUnknown
	for _, f := range r.Findings {
		if s := ParseSeverity(f.Advisory.Severity); s > max {
			max = s
		}
	}
	return max
}

// Service posts bulk advisory requests.
type Service struct {
	Config    *npmrc.Config
	UserAgent string
	HTTP      *http.Client
	Ignore    map[string]bool // lowercased GHSA ids to exclude
}

// Audit groups the lockfile's packages by registry, posts a bulk request
// to each, and returns the findings whose advisories match an installed
// version.
func (s *Service) Audit(ctx context.Context, lock *lockfile.Lockfile) *Report {
	report := &Report{}
	byRegistry := map[string]map[string][]string{} // registry → name → versions
	for _, p := range lock.Packages {
		reg := s.registryFor(p.Name)
		if byRegistry[reg] == nil {
			byRegistry[reg] = map[string][]string{}
		}
		byRegistry[reg][p.Name] = append(byRegistry[reg][p.Name], p.Version)
	}

	for reg, names := range byRegistry {
		advisories, err := s.bulk(ctx, reg, names)
		if err != nil {
			report.Errors = append(report.Errors, err.Error())
			continue
		}
		for name, advs := range advisories {
			for _, adv := range advs {
				if adv.ID != "" && s.Ignore[strings.ToLower(adv.ID)] {
					continue
				}
				for _, installed := range names[name] {
					if matchesVulnerable(installed, adv.VulnerableVersions) {
						report.Findings = append(report.Findings, Finding{Package: name, Installed: installed, Advisory: adv})
					}
				}
			}
		}
	}
	sort.Slice(report.Findings, func(i, j int) bool {
		if report.Findings[i].Package != report.Findings[j].Package {
			return report.Findings[i].Package < report.Findings[j].Package
		}
		return report.Findings[i].Advisory.ID < report.Findings[j].Advisory.ID
	})
	return report
}

func (s *Service) registryFor(name string) string {
	if strings.HasPrefix(name, "@") {
		scope := name
		if slash := strings.IndexByte(name, '/'); slash > 0 {
			scope = name[:slash]
		}
		if r := s.Config.RegistryFor(scope); r != "" {
			return r
		}
	}
	return s.Config.Registry()
}

func (s *Service) bulk(ctx context.Context, registry string, names map[string][]string) (map[string][]Advisory, error) {
	body, _ := json.Marshal(names)
	endpoint := strings.TrimRight(registry, "/") + "/-/npm/v1/security/advisories/bulk"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", s.UserAgent)
	if u, perr := url.Parse(registry); perr == nil {
		if tok := s.Config.AuthTokenFor(u); tok != "" {
			req.Header.Set("Authorization", "Bearer "+tok)
		}
	}
	client := s.HTTP
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("advisory fetch from %s failed: %w", registry, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("advisory fetch from %s returned %d", registry, resp.StatusCode)
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	var raw map[string][]map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("advisory response parse: %w", err)
	}
	out := map[string][]Advisory{}
	for name, advs := range raw {
		for _, a := range advs {
			out[name] = append(out[name], advisoryFromMap(a))
		}
	}
	return out, nil
}

func advisoryFromMap(m map[string]any) Advisory {
	id := str(m["github_advisory_id"])
	if id == "" {
		id = str(m["id"])
	}
	return Advisory{
		ID:                 id,
		Title:              str(m["title"]),
		Severity:           str(m["severity"]),
		URL:                str(m["url"]),
		VulnerableVersions: str(m["vulnerable_versions"]),
		PatchedVersions:    str(m["patched_versions"]),
	}
}

func matchesVulnerable(installed, vulnerableRange string) bool {
	v, err := semver.Parse(installed)
	if err != nil {
		return false
	}
	r, err := semver.ParseRange(vulnerableRange)
	if err != nil {
		return true // unparseable range → be conservative, flag it
	}
	return r.Satisfies(v)
}

func str(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case float64:
		return fmt.Sprintf("%v", x)
	case nil:
		return ""
	default:
		return fmt.Sprint(x)
	}
}
