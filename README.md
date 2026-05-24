# gnpm

A Go implementation of an npm/pnpm-compatible package manager. The goal is **interoperation without a migration step**: point gnpm at an existing npm or pnpm project and it reads and writes that project's native formats (`package.json`, `package-lock.json`, `pnpm-lock.yaml`, `.npmrc`, `pnpm-workspace.yaml`) directly, with no conversion pass.

`gnpm` does not publish packages — the registry is read-only from its perspective.

The behavioral contract lives in [doc/spec.md](doc/spec.md).

## Why Go

gnpm is a single statically-linked binary built on the Go standard library, which keeps startup instant and distribution trivial:

| Concern | gnpm |
|---|---|
| Concurrency | goroutines + bounded semaphores |
| Hardlink / clonefile / chmod | `os` + `golang.org/x/sys` (clonefile on macOS, per-file hardlink elsewhere) |
| ECDSA signature verification | `crypto/ecdsa` (standard library) |
| Binary | single static executable |

External dependencies are kept to two: `gopkg.in/yaml.v3` (pnpm YAML) and `golang.org/x/sys` (clonefile/hardlink). Everything else is the Go standard library.

## Build

```sh
go build -o gnpm ./cmd/gnpm
```

## Usage

```sh
gnpm install                 # resolve, fetch, link; write lockfile + state
gnpm ci                      # locked install (requires a lockfile)
gnpm add react react-dom     # add deps and install
gnpm add -D vitest           # add a devDependency
gnpm remove lodash
gnpm update                  # bump within declared ranges
gnpm run build -- --watch    # run a package.json script
gnpm exec eslint .           # run a node_modules/.bin binary
gnpm list / why <pkg> / outdated
gnpm view react versions
gnpm pkg get scripts.build / pkg set version=2.0.0
gnpm config get registry
gnpm audit --level=high [--json]
gnpm sbom --format=cyclonedx -o sbom.json
gnpm dlx cowsay "hello"
gnpm clean [--delete-lockfile]
gnpm doctor
```

The store and cache live under `~/.gnpm/` (`store`, `cache`, `dlx`).

## Architecture

```
cmd/gnpm/            entrypoint, exit-code mapping
internal/
  core/              typed errors + exit codes, logger, hashing, concurrency
  semver/            npm-dialect version + range engine (node-semver conformant)
  npmrc/             .npmrc parser, layer precedence, auth, named registries
  project/           package.json model, mode detection, dependency specifiers,
                     pnpm-workspace.yaml
  registry/          HTTP client (ETag/Cache-Control), packument, cache, SRI
  archive/           tar+gzip extraction with path-traversal sanitization
  store/             content-addressable store (per-file SHA-512), materializer
  platform/          OS/arch/libc detection, hardlink/symlink/chmod
  resolver/          Pubgrub solver + npm extensions (overrides, peers, optionals)
  regprovider/       registry → resolver bridge, minimum-release-age filter
  lockfile/          package-lock.json (v3) + pnpm-lock.yaml read/write/convert
  linker/            hoisted (flat) + isolated (pnpm-style) node_modules
  scripts/           lifecycle runner (restricted env) + build-script gate
  workspacestate/    install fingerprint (spec §4.3.1) + verify policy
  signature/         ECDSA P-256 tarball signature verification
  audit/             bulk advisory endpoint
  sbom/              CycloneDX 1.7 / SPDX 2.3
  policy/            pmOnFail, catalog resolution
  pkgedit/           order-preserving package.json editor
  installer/         end-to-end install orchestration
  cli/               command dispatch + per-command implementations
```

## Status

Implemented and tested:

- the full resolve → fetch → ingest → link → scripts → lockfile → state pipeline
- all 19 spec commands
- **multiple versions of the same package**: the default (hoisted) path uses an npm-style tree resolver that hoists the first version and nests conflicting ones (`a/node_modules/x`), so version-conflicting graphs install instead of failing — verified installing real trees like `eslint`. (The `node-linker=isolated` path uses the single-version pubgrub solver.)
- **pruning** of extraneous packages (a removed dependency is deleted from node_modules), and network **retries** on transient 429/5xx
- both lockfile formats (`package-lock.json` v3 with nested `node_modules/.../node_modules/...` keys, `pnpm-lock.yaml`) read/write/convert
- dependency specifiers: semver, `npm:` aliases, dist-tags, `file:`, `link:`, `https` tarballs (pinned by integrity), `git`/`github:` (pinned to a resolved commit), `catalog:`. git/https deps — **direct or transitive** (a registry package that itself declares a git/https URL) — are resolved through the tree resolver's injected fetch capability, recursing into their own dependencies and materializing as real directories so Node resolution reaches the hoisted deps. (Transitive exotic resolution applies to the default hoisted layout; the isolated layout supports direct git/https deps only.)
- multi-package **workspaces** (glob discovery, `workspace:` protocol, per-member node_modules)
- the workspace-state fingerprint, build-script gate, `minimum-release-age`
- security/policy: ECDSA signature verification, `audit` (+ `--audit-level` on install), `trustPolicy` no-downgrade (+ `trustPolicyIgnoreAfter`), `pmOnFail`, `catalogMode` `manual`/`prefer`/`strict`, and **`blockExoticSubdeps`** (enforced: a transitive git/https dependency is rejected unless its repo is on the trusted-repo allowlist)
- `configDependencies` materialization, SBOM (CycloneDX/SPDX), `dlx`

Known limitations (stated plainly):

- `node-linker=isolated` uses the single-version pubgrub solver, so a version-conflicting graph **errors** there (loudly), and it resolves only *direct* git/https deps (transitive exotic deps need the tree resolver). The default hoisted layout installs multiple versions and resolves transitive exotic deps.
- Per the spec, the `--json` schemas are not frozen during 0.x.

gnpm is also younger and less battle-tested than pnpm across unusual registries, large monorepos, and recovery paths — smaller code is partly design, partly immaturity.

## Benchmark

Measured on macOS arm64, fixture `react@^18 + react-dom@^18 + vite@^5` (16 packages, incl. esbuild/rollup platform-native optional deps), against the real npm registry. Each tool ran with an isolated `HOME` so global stores were not shared. Warm = `node_modules` removed with the store + lockfile kept; relink timed with `hyperfine` at ms precision.

| scenario | gnpm | pnpm | bun |
|---|---|---|---|
| cold (empty cache + network) | ~1.1 s / 82 MB | ~1.2 s / 389 MB | ~0.7 s / 146 MB |
| warm relink | **8.0 ms ± 0.5** | 311.1 ms ± 2.3 | 9.2 ms ± 0.5 |
| no-op (everything intact) | ~4 ms | — | — |

gnpm's warm relink is ~39× faster than pnpm and edges out bun, at a fraction of the memory. Two things make the warm path fast: each package is materialized with a single recursive `clonefile(2)` on macOS (per-file hardlink elsewhere), and the host node version is cached by binary identity so only the first install pays the `node --version` fork+exec (profiling showed it was ~80% of warm time before caching). The no-op case short-circuits on the workspace-state fingerprint. Numbers belong to this set of versions (pnpm 11.2.2, bun 1.3.14, node 24).

On the **cold** path the resolver streams each finalized package straight into a tarball-fetch pool while packument prefetch and resolution are still in flight, so download + ingest overlap the metadata fetches rather than running after them ([doc/pipelined-install.md](doc/pipelined-install.md)). This is the default (hoisted) path only — the greedy tree resolver never revises a placement, so a streamed version is final; the pubgrub-based isolated path keeps its sequential fetch. Measured effect: the tarball phase (~360 ms) folds under the metadata phase, dropping the combined network phase from ~1.6 s to ~1.1 s.

Reproduce with the harness under [tools/bench](tools/bench): `tools/bench/run.sh --tools gnpm,pnpm,bun` prints cold/warm best/median/worst + peak memory (it builds gnpm if `--gnpm-bin` is not given). `/usr/bin/time` is too coarse for the sub-10 ms warm path, so drive warm through `hyperfine` (the script header has the exact invocation). `tools/bench/install_phases.sh <gnpm-bin> <fixture-dir>` runs with `GNPM_PROFILE=1` and prints the per-phase min/median/max (cold wall time is network-variance dominated; the phase view is the stable signal).

## Development

```sh
make build    # build the binary
make test     # go test ./...
make check    # gofmt + vet + test
```

Tests that bind a loopback HTTP server (registry/installer integration) need real
localhost networking; run them outside a restrictive sandbox.

## License

MIT — see [LICENSE](LICENSE).
