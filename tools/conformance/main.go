// Command conformance is a differential test harness: it runs npm, pnpm,
// and gnpm on the same fixtures and compares the *resolved version set*
// (name → {versions}) plus accept/reject behavior. gnpm's correctness
// target is "match npm/pnpm on the inputs they accept", so the meaningful
// signal is divergence from them, not whether gnpm survives arbitrary
// input. A package gnpm installs that the reference rejects — or a version
// it resolves differently — is a conformance defect.
//
// It reuses gnpm's own lockfile parsers (no jq/yq), so npm's
// package-lock.json and pnpm's pnpm-lock.yaml are read the same way gnpm
// reads them. Needs npm, pnpm, and network; run outside a sandbox.
//
//	go run ./tools/conformance [--gnpm-bin PATH] [--fixture NAME]
package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/koji-1009/gnpm/internal/lockfile"
)

type fixture struct {
	name    string
	pkgJSON string
}

// Fixtures: valid inputs npm and pnpm both accept, spanning representative
// real-world shapes — trivial, zero-dep-but-large, platform-optional native
// deps, deep transitive trees, peer-dependency graphs, toolchains, and a
// realistic multi-tool app. Ordered light → heavy so partial output is
// useful if a run is interrupted.
var fixtures = []fixture{
	{"lodash", `{"name":"cf","version":"1.0.0","dependencies":{"lodash":"^4.17.0"}}`},
	{"typescript", `{"name":"cf","version":"1.0.0","devDependencies":{"typescript":"^5.4.0"}}`},
	{"prettier", `{"name":"cf","version":"1.0.0","devDependencies":{"prettier":"^3.2.0"}}`},
	{"vue", `{"name":"cf","version":"1.0.0","dependencies":{"vue":"^3.4.0"}}`},
	{"express", `{"name":"cf","version":"1.0.0","dependencies":{"express":"^4.18.0"}}`},
	{"fastify", `{"name":"cf","version":"1.0.0","dependencies":{"fastify":"^4.26.0"}}`},
	{"swc", `{"name":"cf","version":"1.0.0","devDependencies":{"@swc/core":"^1.4.0"}}`},
	{"react-vite", `{"name":"cf","version":"1.0.0","dependencies":{"react":"^18.0.0","react-dom":"^18.0.0"},"devDependencies":{"vite":"^5.0.0"}}`},
	{"peer-eslint", `{"name":"cf","version":"1.0.0","devDependencies":{"eslint":"^8.57.0","eslint-plugin-react":"^7.34.0"}}`},
	{"ts-eslint", `{"name":"cf","version":"1.0.0","devDependencies":{"@typescript-eslint/parser":"^7.0.0","@typescript-eslint/eslint-plugin":"^7.0.0","eslint":"^8.57.0","typescript":"^5.4.0"}}`},
	{"babel", `{"name":"cf","version":"1.0.0","devDependencies":{"@babel/core":"^7.24.0","@babel/preset-env":"^7.24.0"}}`},
	{"jest", `{"name":"cf","version":"1.0.0","devDependencies":{"jest":"^29.7.0"}}`},
	{"fullstack", `{"name":"cf","version":"1.0.0","dependencies":{"react":"^18.2.0","react-dom":"^18.2.0"},"devDependencies":{"@types/react":"^18.2.0","@types/react-dom":"^18.2.0","typescript":"^5.4.0","vite":"^5.2.0","eslint":"^8.57.0","prettier":"^3.2.0"}}`},
}

const registryURL = "https://registry.npmjs.org/"

// Both tools run at their real defaults; no release-age flags are injected.
// gnpm now defaults its minimum-release-age to pnpm's (one day) in pnpm mode
// and to none in npm mode, matching each reference's own default gate, so the
// comparison needs no harness-side adjustment to that policy.

type result struct {
	ok   bool
	vers map[string]map[string]bool // name → set of versions
	note string                     // error / stderr tail when !ok
}

func main() {
	gnpmBin := flag.String("gnpm-bin", "", "gnpm binary to test (built from ./cmd/gnpm if empty)")
	only := flag.String("fixture", "", "run only this fixture by name")
	roundtrip := flag.Bool("roundtrip", false, "instead of comparing version sets, check whether pnpm accepts the pnpm-lock.yaml gnpm writes (frozen install) and whether a normal install rewrites it")
	flag.Parse()

	bin, cleanup, err := resolveGnpmBin(*gnpmBin)
	if err != nil {
		fmt.Fprintln(os.Stderr, "build gnpm:", err)
		os.Exit(1)
	}
	defer cleanup()

	home, err := os.MkdirTemp("", "gnpm-conf-home-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer os.RemoveAll(home)

	if *roundtrip {
		runRoundtripSuite(bin, home, *only)
		return
	}

	diverged := 0
	checked := 0
	for _, fx := range fixtures {
		if *only != "" && fx.name != *only {
			continue
		}
		for _, mode := range []string{"npm", "pnpm"} {
			if !toolAvailable(mode) {
				fmt.Printf("== %s [%s]: SKIP (%s not on PATH)\n", fx.name, mode, mode)
				continue
			}
			checked++
			ref := runReference(mode, fx, home)
			got := runGnpm(mode, fx, bin, home)
			if reportOne(fx.name, mode, ref, got) {
				diverged++
			}
		}
	}

	fmt.Printf("\n=== %d comparison(s), %d divergence(s) ===\n", checked, diverged)
	if diverged > 0 {
		os.Exit(1)
	}
}

// --- round-trip mode --------------------------------------------------
//
// The default mode proves gnpm *resolves* like pnpm (same version set). It
// does not prove gnpm *writes* a pnpm-lock.yaml that pnpm itself accepts:
// gnpm emits flat `name@version` snapshot keys, whereas pnpm instances a
// package per peer-dependency context and keys snapshots `name@version(peer@v)`.
// Round-trip mode measures the consequence directly with two questions per
// fixture:
//
//	A. frozen   — does `pnpm install --frozen-lockfile` accept gnpm's lockfile
//	              as-is? (the CI question: can a pnpm user consume it.)
//	B. rewrite  — does `pnpm install --lockfile-only` leave gnpm's lockfile
//	              byte-equal, or rewrite it to pnpm's canonical form? (the
//	              churn question: is gnpm's output already canonical.)
func runRoundtripSuite(bin, home, only string) {
	if !toolAvailable("pnpm") {
		fmt.Fprintln(os.Stderr, "pnpm not on PATH; round-trip mode needs pnpm")
		os.Exit(1)
	}
	checked, rejected, peerDiverged := 0, 0, 0
	for _, fx := range fixtures {
		if only != "" && fx.name != only {
			continue
		}
		checked++
		v := runRoundtrip(fx, bin, home)
		if v.frozenRejected {
			rejected++
		}
		if v.peerDiverged {
			peerDiverged++
		}
	}
	fmt.Printf("\n=== %d fixture(s): %d rejected by frozen install, %d with peer-context structural divergence ===\n",
		checked, rejected, peerDiverged)
}

type roundtripVerdict struct {
	frozenRejected bool
	peerDiverged   bool // pnpm peer-instances snapshots that gnpm writes flat
}

// runRoundtrip resolves a fixture with gnpm, then measures two things about the
// pnpm-lock.yaml gnpm writes: (1) whether `pnpm install --frozen-lockfile`
// accepts it as-is, and (2) how its snapshot structure compares to the lockfile
// pnpm itself produces from scratch — specifically pnpm's peer-context
// instancing (`name@version(peer@v)` keys), which gnpm's single-version model
// writes flat.
func runRoundtrip(fx fixture, bin, home string) roundtripVerdict {
	tag := fmt.Sprintf("== %s [roundtrip]:", fx.name)

	lock, err := gnpmProduceLock(fx, bin, home)
	if err != nil {
		fmt.Printf("%s gnpm could not produce a lockfile: %s\n", tag, err)
		return roundtripVerdict{}
	}
	gnpmKeys, err := snapshotKeySet(lock)
	if err != nil {
		fmt.Printf("%s could not parse gnpm lockfile: %s\n", tag, err)
		return roundtripVerdict{}
	}

	frozenOK, frozenNote := pnpmFrozenAccepts(fx, lock, home)
	pnpmKeys, nativeErr := pnpmNativeKeys(fx, home)

	var v roundtripVerdict
	frozen := "ACCEPT"
	if !frozenOK {
		frozen = "REJECT"
		v.frozenRejected = true
	}

	structure := "?"
	if nativeErr != nil {
		structure = "native-resolve-failed(" + nativeErr.Error() + ")"
	} else {
		gnpmPeer := countPeerSuffixed(gnpmKeys)
		pnpmPeer := countPeerSuffixed(pnpmKeys)
		if pnpmPeer > gnpmPeer {
			v.peerDiverged = true
		}
		structure = fmt.Sprintf("peer-instanced snapshots pnpm=%d gnpm=%d (of pnpm %d / gnpm %d total)",
			pnpmPeer, gnpmPeer, len(pnpmKeys), len(gnpmKeys))
		if ex := firstWithParen(minus(pnpmKeys, gnpmKeys), 1); len(ex) > 0 && pnpmPeer > gnpmPeer {
			structure += "; e.g. " + ex[0]
		}
	}

	fmt.Printf("%s frozen=%s  %s\n", tag, frozen, structure)
	if !frozenOK && frozenNote != "" {
		fmt.Printf("    frozen-lockfile error: %s\n", frozenNote)
	}
	return v
}

func countPeerSuffixed(keys map[string]bool) int {
	n := 0
	for k := range keys {
		if strings.ContainsRune(k, '(') {
			n++
		}
	}
	return n
}

// gnpmProduceLock runs gnpm in pnpm mode and returns the pnpm-lock.yaml bytes.
func gnpmProduceLock(fx fixture, bin, home string) ([]byte, error) {
	dir, err := os.MkdirTemp("", "gnpm-rt-gnpm-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(fx.pkgJSON), 0o644); err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(dir, "pnpm-workspace.yaml"), []byte("packages: []\n"), 0o644); err != nil {
		return nil, err
	}
	cmd := exec.Command(bin, "--silent", "install", "--ignore-scripts")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "HOME="+home)
	if out, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("%s", tail(string(out)))
	}
	return os.ReadFile(filepath.Join(dir, "pnpm-lock.yaml"))
}

// pnpmFrozenAccepts writes gnpm's lockfile beside the fixture and asks pnpm to
// install with --frozen-lockfile: success means pnpm consumes gnpm's lockfile
// as-is, failure means it rejects it as not up to date / incompatible.
func pnpmFrozenAccepts(fx fixture, lock []byte, home string) (bool, string) {
	dir, err := os.MkdirTemp("", "gnpm-rt-frozen-")
	if err != nil {
		return false, err.Error()
	}
	defer os.RemoveAll(dir)
	if err := writeLockFixture(dir, fx, lock); err != nil {
		return false, err.Error()
	}
	cmd := exec.Command("pnpm", "install", "--frozen-lockfile", "--ignore-scripts", "--config.confirmModulesPurge=false")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "HOME="+home, "npm_config_fund=false", "npm_config_audit=false")
	if out, err := cmd.CombinedOutput(); err != nil {
		return false, tail(string(out))
	}
	return true, ""
}

// pnpmNativeKeys resolves the fixture with pnpm from scratch (no seed lockfile,
// metadata-only, no tarball downloads) and returns the snapshot key set of the
// lockfile pnpm produces — i.e. pnpm's canonical structure, with peer-context
// instancing — so the caller can compare it to gnpm's flat output.
func pnpmNativeKeys(fx fixture, home string) (map[string]bool, error) {
	dir, err := os.MkdirTemp("", "gnpm-rt-native-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(fx.pkgJSON), 0o644); err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(dir, "pnpm-workspace.yaml"), []byte("packages: []\n"), 0o644); err != nil {
		return nil, err
	}
	cmd := exec.Command("pnpm", "install", "--lockfile-only", "--silent")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "HOME="+home, "npm_config_fund=false", "npm_config_audit=false")
	if out, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("%s", tail(string(out)))
	}
	native, err := os.ReadFile(filepath.Join(dir, "pnpm-lock.yaml"))
	if err != nil {
		return nil, err
	}
	return snapshotKeySet(native)
}

func writeLockFixture(dir string, fx fixture, lock []byte) error {
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(fx.pkgJSON), 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "pnpm-workspace.yaml"), []byte("packages: []\n"), 0o644); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "pnpm-lock.yaml"), lock, 0o644)
}

func snapshotKeySet(lock []byte) (map[string]bool, error) {
	p, err := lockfile.ParsePnpm(lock)
	if err != nil {
		return nil, err
	}
	out := map[string]bool{}
	for k := range p.Snapshots {
		out[k] = true
	}
	return out, nil
}

// minus returns the elements of a not present in b.
func minus(a, b map[string]bool) []string {
	var out []string
	for k := range a {
		if !b[k] {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

// firstWithParen returns up to n keys that carry a peer-context suffix.
func firstWithParen(keys []string, n int) []string {
	var out []string
	for _, k := range keys {
		if strings.ContainsRune(k, '(') {
			out = append(out, k)
			if len(out) == n {
				break
			}
		}
	}
	if len(out) == 0 && len(keys) > 0 {
		// no peer suffix among the additions; show plain examples
		for _, k := range keys {
			out = append(out, k)
			if len(out) == n {
				break
			}
		}
	}
	return out
}

// reportOne prints one comparison and returns true on divergence.
func reportOne(name, mode string, ref, got result) bool {
	tag := fmt.Sprintf("== %s [%s] gnpm vs %s:", name, mode, mode)

	// Accept/reject conformance first.
	if ref.ok != got.ok {
		switch {
		case ref.ok && !got.ok:
			fmt.Printf("%s DIVERGE — %s resolved but gnpm failed: %s\n", tag, mode, got.note)
		default:
			fmt.Printf("%s DIVERGE — gnpm resolved but %s rejected (over-permissive): %s\n", tag, mode, ref.note)
		}
		return true
	}
	if !ref.ok && !got.ok {
		fmt.Printf("%s ok (both reject)\n", tag)
		return false
	}

	// Both succeeded → compare resolved version sets.
	var diffs []string
	names := map[string]bool{}
	for n := range ref.vers {
		names[n] = true
	}
	for n := range got.vers {
		names[n] = true
	}
	for _, n := range sortedKeys(names) {
		r, g := setStr(ref.vers[n]), setStr(got.vers[n])
		if r != g {
			diffs = append(diffs, fmt.Sprintf("    %-32s %s=%s gnpm=%s", n, mode, orNone(r), orNone(g)))
		}
	}
	if len(diffs) == 0 {
		fmt.Printf("%s MATCH (%d packages)\n", tag, len(ref.vers))
		return false
	}
	fmt.Printf("%s DIVERGE (%d package(s) differ of %d)\n", tag, len(diffs), len(names))
	for _, d := range diffs {
		fmt.Println(d)
	}
	return true
}

func runReference(mode string, fx fixture, home string) result {
	dir, err := os.MkdirTemp("", "gnpm-conf-ref-")
	if err != nil {
		return result{note: err.Error()}
	}
	defer os.RemoveAll(dir)
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(fx.pkgJSON), 0o644); err != nil {
		return result{note: err.Error()}
	}

	var cmd *exec.Cmd
	switch mode {
	case "npm":
		cmd = exec.Command("npm", "install", "--package-lock-only", "--no-audit", "--no-fund", "--silent")
	case "pnpm":
		cmd = exec.Command("pnpm", "install", "--lockfile-only", "--silent")
	}
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "HOME="+home, "npm_config_fund=false", "npm_config_audit=false")
	if out, err := cmd.CombinedOutput(); err != nil {
		return result{ok: false, note: tail(string(out))}
	}
	return parseLock(mode, dir)
}

func runGnpm(mode string, fx fixture, bin, home string) result {
	dir, err := os.MkdirTemp("", "gnpm-conf-gnpm-")
	if err != nil {
		return result{note: err.Error()}
	}
	defer os.RemoveAll(dir)
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(fx.pkgJSON), 0o644); err != nil {
		return result{note: err.Error()}
	}
	// An empty pnpm-workspace.yaml forces gnpm into pnpm mode (isolated +
	// pnpm-lock.yaml); a bare dir is npm/hoisted mode. gnpm picks its own
	// mode-appropriate release-age default, matching the reference.
	if mode == "pnpm" {
		if err := os.WriteFile(filepath.Join(dir, "pnpm-workspace.yaml"), []byte("packages: []\n"), 0o644); err != nil {
			return result{note: err.Error()}
		}
	}
	cmd := exec.Command(bin, "--silent", "install", "--ignore-scripts")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "HOME="+home)
	if out, err := cmd.CombinedOutput(); err != nil {
		return result{ok: false, note: tail(string(out))}
	}
	return parseLock(mode, dir)
}

func parseLock(mode, dir string) result {
	switch mode {
	case "npm":
		data, err := os.ReadFile(filepath.Join(dir, "package-lock.json"))
		if err != nil {
			return result{ok: false, note: "no package-lock.json: " + err.Error()}
		}
		lf, err := lockfile.ImportNpm(data)
		if err != nil {
			return result{ok: false, note: "parse package-lock.json: " + err.Error()}
		}
		return result{ok: true, vers: resolvedSet(lf)}
	default:
		data, err := os.ReadFile(filepath.Join(dir, "pnpm-lock.yaml"))
		if err != nil {
			return result{ok: false, note: "no pnpm-lock.yaml: " + err.Error()}
		}
		p, err := lockfile.ParsePnpm(data)
		if err != nil {
			return result{ok: false, note: "parse pnpm-lock.yaml: " + err.Error()}
		}
		return result{ok: true, vers: resolvedSet(lockfile.PnpmToLockfile(p, registryURL))}
	}
}

func resolvedSet(lf *lockfile.Lockfile) map[string]map[string]bool {
	out := map[string]map[string]bool{}
	for _, p := range lf.Packages {
		if p.Name == "" || p.Version == "" {
			continue
		}
		if out[p.Name] == nil {
			out[p.Name] = map[string]bool{}
		}
		out[p.Name][p.Version] = true
	}
	return out
}

func resolveGnpmBin(flagBin string) (string, func(), error) {
	if flagBin != "" {
		return flagBin, func() {}, nil
	}
	tmp, err := os.MkdirTemp("", "gnpm-conf-bin-")
	if err != nil {
		return "", func() {}, err
	}
	bin := filepath.Join(tmp, "gnpm")
	cmd := exec.Command("go", "build", "-o", bin, "./cmd/gnpm")
	if out, err := cmd.CombinedOutput(); err != nil {
		os.RemoveAll(tmp)
		return "", func() {}, fmt.Errorf("%s", out)
	}
	return bin, func() { os.RemoveAll(tmp) }, nil
}

func toolAvailable(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

func setStr(s map[string]bool) string {
	if len(s) == 0 {
		return ""
	}
	vs := make([]string, 0, len(s))
	for v := range s {
		vs = append(vs, v)
	}
	sort.Strings(vs)
	return strings.Join(vs, ",")
}

func orNone(s string) string {
	if s == "" {
		return "{absent}"
	}
	return "{" + s + "}"
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func tail(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 300 {
		return "..." + s[len(s)-300:]
	}
	return s
}
