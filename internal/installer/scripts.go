package installer

import (
	"context"
	"path/filepath"

	"github.com/koji-1009/gnpm/internal/core"
	"github.com/koji-1009/gnpm/internal/linker"
	"github.com/koji-1009/gnpm/internal/npmrc"
	"github.com/koji-1009/gnpm/internal/project"
	"github.com/koji-1009/gnpm/internal/scripts"
)

// runRootScript runs one root-project lifecycle script if defined.
func (op *Operation) runRootScript(ctx context.Context, pkg *project.PackageJSON, event scripts.LifecycleEvent) error {
	cmd := pkg.Scripts[string(event)]
	if cmd == "" {
		return nil
	}
	runner := scripts.NewRunner()
	_, err := runner.Run(ctx, scripts.Script{
		Event:          event,
		PackageName:    pkg.Name,
		PackageVersion: pkg.Version,
		WorkingDir:     op.ProjectRoot,
		Command:        cmd,
	}, linker.TopLevelBinDir(op.ProjectRoot))
	return err
}

// runLifecycleScripts runs per-dependency install-time scripts (gated by
// the build-script allowlist) in topological order, then the root's
// install/postinstall/prepare.
func (op *Operation) runLifecycleScripts(ctx context.Context, pkg *project.PackageJSON, cfg *npmrc.Config, specs []linker.LinkSpec, kind linker.Kind) ([]string, error) {
	var warnings []string
	runner := scripts.NewRunner()
	binDir := linker.TopLevelBinDir(op.ProjectRoot)

	gate := scripts.BuildPolicy{
		AllowBuilds:               op.reviewedAllowlist(pkg),
		NeverBuild:                op.neverBuildList(pkg),
		StrictDepBuilds:           op.Options.StrictDepBuilds,
		DangerouslyAllowAllBuilds: op.Options.DangerouslyAllowAllBuilds,
	}
	enforce := op.Options.EffectiveScriptPolicy() == ScriptAllowlist

	for _, spec := range topoOrder(specs) {
		if len(spec.Scripts) == 0 {
			continue
		}
		triggers := scripts.TriggersFromScripts(spec.Scripts, false, false)
		// The denylist is a hard block regardless of the script policy — it
		// applies even under dangerouslyAllowAllBuilds, which bypasses the
		// allowlist gate below.
		if triggers.Any() && scripts.MatchesAllowPattern(spec.Name, gate.NeverBuild) {
			warnings = append(warnings, "skipped install scripts for "+spec.ID()+": in neverBuiltDependencies")
			continue
		}
		if enforce {
			switch gate.Evaluate(spec.Name, triggers) {
			case scripts.BuildFail:
				return warnings, core.Usage("install refused: %s ships install-time build scripts but is not in allowBuilds; add it after review or set dangerouslyAllowAllBuilds", spec.ID())
			case scripts.BuildSkip:
				warnings = append(warnings, "skipped install scripts for "+spec.ID()+": not in allowBuilds")
				continue
			}
		}
		workingDir := op.linkedPath(kind, spec)
		for _, event := range scripts.InstallEvents {
			cmd := spec.Scripts[string(event)]
			if cmd == "" {
				continue
			}
			_, err := runner.Run(ctx, scripts.Script{
				Event:          event,
				PackageName:    spec.Name,
				PackageVersion: spec.Version,
				WorkingDir:     workingDir,
				Command:        cmd,
			}, binDir)
			if err != nil {
				warnings = append(warnings, "script "+string(event)+" for "+spec.ID()+" failed: "+err.Error())
			}
		}
	}

	for _, event := range []scripts.LifecycleEvent{scripts.Install, scripts.Postinstall, scripts.Prepare} {
		if err := op.runRootScript(ctx, pkg, event); err != nil {
			return warnings, err
		}
	}
	return warnings, nil
}

func (op *Operation) reviewedAllowlist(pkg *project.PackageJSON) []string {
	out := append([]string(nil), pkg.AllowBuilds...)
	out = append(out, pkg.OnlyBuiltDependencies...)
	ws := project.ReadPnpmWorkspace(op.ProjectRoot)
	out = append(out, ws.AllowBuilds...)
	out = append(out, ws.OnlyBuiltDependencies...)
	return out
}

// neverBuildList unions pnpm's neverBuiltDependencies denylist from
// package.json and pnpm-workspace.yaml.
func (op *Operation) neverBuildList(pkg *project.PackageJSON) []string {
	out := append([]string(nil), pkg.NeverBuiltDependencies...)
	out = append(out, project.ReadPnpmWorkspace(op.ProjectRoot).NeverBuiltDependencies...)
	return out
}

func (op *Operation) linkedPath(kind linker.Kind, spec linker.LinkSpec) string {
	if kind == linker.Isolated {
		return filepath.Join(op.ProjectRoot, "node_modules", ".pnpm", safeID(spec.ID()), "node_modules", filepath.FromSlash(spec.Name))
	}
	return filepath.Join(op.ProjectRoot, "node_modules", filepath.FromSlash(spec.Name))
}

func safeID(id string) string {
	out := make([]byte, 0, len(id))
	for i := 0; i < len(id); i++ {
		if id[i] == '/' {
			out = append(out, '+')
		} else {
			out = append(out, id[i])
		}
	}
	return string(out)
}

// topoOrder returns specs deepest-dependency-first (post-order DFS).
func topoOrder(specs []linker.LinkSpec) []linker.LinkSpec {
	byID := map[string]linker.LinkSpec{}
	for _, s := range specs {
		byID[s.ID()] = s
	}
	var out []linker.LinkSpec
	visited := map[string]bool{}
	var visit func(s linker.LinkSpec)
	visit = func(s linker.LinkSpec) {
		if visited[s.ID()] {
			return
		}
		visited[s.ID()] = true
		for dep, ver := range s.Dependencies {
			if d, ok := byID[dep+"@"+ver]; ok {
				visit(d)
			}
		}
		out = append(out, s)
	}
	for _, s := range specs {
		visit(s)
	}
	return out
}
