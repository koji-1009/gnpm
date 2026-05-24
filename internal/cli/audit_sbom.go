package cli

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/koji-1009/gnpm/internal/audit"
	"github.com/koji-1009/gnpm/internal/core"
	"github.com/koji-1009/gnpm/internal/lockfile"
	"github.com/koji-1009/gnpm/internal/npmrc"
	"github.com/koji-1009/gnpm/internal/project"
	"github.com/koji-1009/gnpm/internal/sbom"
)

func cmdAudit(ctx context.Context, env *Env, args []string) error {
	fs := flag.NewFlagSet("audit", flag.ContinueOnError)
	fs.SetOutput(env.Stderr)
	level := fs.String("level", "high", "minimum severity to fail on")
	ignoreGhsas := fs.String("ignore-ghsas", "", "comma-separated GHSA ids to ignore")
	if err := parseFlags(fs, args); err != nil {
		return err
	}

	cfg, err := npmrc.Loader{ProjectDir: env.Cwd}.Load()
	if err != nil {
		return err
	}
	lock, err := lockfile.Read(env.Cwd, cfg.Registry())
	if err != nil {
		return err
	}
	if lock == nil {
		return core.Recoverable("audit requires a project lockfile; run `gnpm install` first")
	}

	ignore := buildIgnoreSet(env, cfg, *ignoreGhsas)
	svc := &audit.Service{Config: cfg, UserAgent: "gnpm/" + Version, Ignore: ignore}
	report := svc.Audit(ctx, lock)
	threshold := audit.ParseSeverity(*level)

	if env.JSON {
		if err := printAuditJSON(env, report); err != nil {
			return err
		}
	} else {
		printAuditText(env, report)
	}

	// Exit 1 when any finding meets the threshold, or when there were no
	// findings but at least one advisory fetch failed (incomplete data).
	for _, f := range report.Findings {
		if audit.ParseSeverity(f.Advisory.Severity).AtLeast(threshold) {
			return core.Recoverable("found advisories at or above %s", threshold)
		}
	}
	if len(report.Findings) == 0 && len(report.Errors) > 0 {
		return core.Recoverable("audit incomplete: %d advisory fetch error(s)", len(report.Errors))
	}
	return nil
}

func buildIgnoreSet(env *Env, cfg *npmrc.Config, flagCSV string) map[string]bool {
	out := map[string]bool{}
	add := func(csv string) {
		for _, id := range strings.Split(csv, ",") {
			if t := strings.TrimSpace(id); t != "" {
				out[strings.ToLower(t)] = true
			}
		}
	}
	add(flagCSV)
	if v, ok := cfg.Get("ignore-ghsas"); ok {
		add(v)
	}
	if pkg, err := project.ReadPackageJSON(packageJSONPath(env)); err == nil {
		for _, id := range pkg.AuditIgnoreGhsas {
			out[strings.ToLower(id)] = true
		}
	}
	return out
}

func printAuditText(env *Env, report *audit.Report) {
	for _, e := range report.Errors {
		fmt.Fprintf(env.Stderr, "audit: %s\n", e)
	}
	if len(report.Findings) == 0 {
		fmt.Fprintln(env.Stdout, "found 0 advisories")
		return
	}
	for _, f := range report.Findings {
		fmt.Fprintf(env.Stdout, "%s  %s@%s: %s (%s)\n",
			strings.ToUpper(f.Advisory.Severity), f.Package, f.Installed, f.Advisory.Title, f.Advisory.URL)
	}
	counts := report.CountsBySeverity()
	var parts []string
	for _, sev := range []string{"critical", "high", "moderate", "low", "info"} {
		if counts[sev] > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", counts[sev], sev))
		}
	}
	fmt.Fprintf(env.Stdout, "\n%d advisories (%s)\n", len(report.Findings), strings.Join(parts, ", "))
}

func printAuditJSON(env *Env, report *audit.Report) error {
	findings := make([]map[string]any, 0, len(report.Findings))
	totals := map[string]int{}
	for _, f := range report.Findings {
		sev := strings.ToLower(f.Advisory.Severity)
		totals[sev]++
		var patched any
		if f.Advisory.PatchedVersions != "" {
			patched = f.Advisory.PatchedVersions
		}
		findings = append(findings, map[string]any{
			"package":             f.Package,
			"installed":           f.Installed,
			"severity":            sev,
			"title":               f.Advisory.Title,
			"vulnerable_versions": f.Advisory.VulnerableVersions,
			"patched_versions":    patched,
			"url":                 f.Advisory.URL,
			"id":                  f.Advisory.ID,
		})
	}
	total := 0
	for _, c := range totals {
		total += c
	}
	if report.Errors == nil {
		report.Errors = []string{}
	}
	return printJSON(env, map[string]any{
		"findings": findings, "totals": totals, "total": total, "errors": report.Errors,
	}, true)
}

func cmdSbom(ctx context.Context, env *Env, args []string) error {
	fs := flag.NewFlagSet("sbom", flag.ContinueOnError)
	fs.SetOutput(env.Stderr)
	format := fs.String("format", "", "cyclonedx|spdx (required)")
	specVersion := fs.String("spec-version", "", "override the spec version string")
	outFile := fs.String("o", "", "write to a file instead of stdout")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	if *format == "" {
		return core.Usage("sbom requires --format=cyclonedx|spdx")
	}
	cfg, _ := npmrc.Loader{ProjectDir: env.Cwd}.Load()
	reg := npmrc.DefaultRegistry
	if cfg != nil {
		reg = cfg.Registry()
	}
	lock, err := lockfile.Read(env.Cwd, reg)
	if err != nil {
		return err
	}
	if lock == nil {
		return core.Usage("sbom requires a project lockfile; run `gnpm install` first")
	}
	projectName := ""
	if pkg, err := project.ReadPackageJSON(packageJSONPath(env)); err == nil {
		projectName = pkg.Name
	}
	doc, err := sbom.Build(lock, *format, *specVersion, projectName)
	if err != nil {
		return err
	}
	if *outFile != "" {
		if err := os.WriteFile(*outFile, append(doc, '\n'), 0o644); err != nil {
			return core.IOError("writing %s", *outFile).Wrap(err)
		}
		return nil
	}
	fmt.Fprintln(env.Stdout, string(doc))
	return nil
}
