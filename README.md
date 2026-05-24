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

`tools/bench/run.sh` measures cold + warm install time and peak memory across gnpm, pnpm, npm, and bun on a chosen fixture. Cold runs wipe each tool's global cache/store first; warm runs only clear `node_modules` (which also drops gnpm's `node_modules/.gnpm` workspace state, so gnpm relinks rather than short-circuiting). Each scenario runs N times and the table reports best / median / worst, so the network floor and the tail are both visible alongside the typical observation.

```
tools/bench/run.sh --fixture vite-react --tools gnpm,pnpm,npm,bun
```

| Flag | Default | Notes |
|------|---------|-------|
| `--fixture NAME` | `vite-react` | Any directory under `tools/bench/fixtures/` |
| `--cold-runs N` | `15` | Cold-scenario repetitions (network-bound; observed spreads of ~5x argue against fewer) |
| `--warm-runs N` | `20` | Warm-scenario repetitions (no network cost, so sampling more is free) |
| `--runs N` | — | Shortcut that sets both `--cold-runs` and `--warm-runs` to `N` |
| `--tools LIST` | `gnpm,pnpm` | Comma-separated subset of `gnpm,pnpm,npm,bun` |
| `--gnpm-bin PATH` | (auto-build) | Reuse an existing gnpm binary instead of running `go build` |

Warm timings from `run.sh` come from `/usr/bin/time` (`real` at 0.01s resolution), which is too coarse for the sub-10 ms native tools — drive those through `hyperfine` when you need ms precision:

```
hyperfine --warmup 2 --runs 20 --prepare 'rm -rf node_modules' '<tool> install'
```

`tools/bench/install_phases.sh <gnpm-bin> <fixture-dir>` prints gnpm's per-phase min/median/max under `GNPM_PROFILE=1`. macOS and Linux are supported (`/usr/bin/time -lp` / `-v`).

### Reference run

One author run on **macOS arm64 (10 cores)**, fixture `vite-react` (16 packages: react + react-dom + vite with its transitive deps, including the esbuild/rollup platform-native optional deps), measured 2026-05-24.

Pinned versions — these numbers are sensitive to each package manager's implementation language and release, so they belong to **this** set:

| component | version |
|-----------|---------|
| Go (used to build gnpm) | 1.26.3 |
| gnpm | HEAD of this branch |
| pnpm | 11.2.2 |
| npm | 11.12.1 |
| bun | 1.3.14 |
| node | 24.15.0 |

| tool | scenario | best | center | worst | peak memory |
|------|----------|------|--------|-------|-------------|
| **gnpm** | cold | 1510 ms | 2320 ms | 7190 ms | **74 MB** |
| **gnpm** | warm | **7.3 ms** | **8.2 ± 0.3 ms** | 8.9 ms | **8 MB** |
| pnpm | cold | 1930 ms | 4070 ms | 7380 ms | 392 MB |
| pnpm | warm | 283.8 ms | 287.1 ± 1.9 ms | 291.3 ms | 268 MB |
| npm | cold | 8720 ms | 10860 ms | 17640 ms | 379 MB |
| npm | warm | 244.6 ms | 250.1 ± 2.7 ms | 255.1 ms | 106 MB |
| bun | cold | 1630 ms | 2480 ms | 5240 ms | 121 MB |
| bun | warm | 8.2 ms | 9.0 ± 0.3 ms | 9.6 ms | 7 MB |

- `best` = min over N runs (the floor when the network and host cooperate).
- `center` = median for cold, hyperfine `mean ± σ` for warm.
- `worst` = max over N runs (the tail; cold can spike several× on a network-noisy session — treat the bracket as that session's spread, not a confidence interval).
- Cold time + peak memory from `tools/bench/run.sh --cold-runs 10`; warm time from `hyperfine --warmup 2 --runs 20`, each tool seeded against its own lockfile.
- Peak memory is `/usr/bin/time -lp`'s `peak memory footprint`.

gnpm's warm relink lands in bun's tier and is ~35× faster than pnpm/npm at a fraction of the memory: each package is materialized with one recursive `clonefile(2)` on macOS (per-file hardlink elsewhere), and the host node version is cached by binary identity so only the first install pays the `node --version` fork+exec. Cold is network-bound — resolve + link are single-digit ms — and gnpm pipelines it (resolution streams each finalized package into a tarball-fetch pool so download + ingest overlap the packument fetches; [doc/pipelined-install.md](doc/pipelined-install.md)), so the cold spread above is registry/CDN variance, not gnpm compute. Rerun in your own environment with current versions for an up-to-date picture.

### Binary size

`go build ./cmd/gnpm` produces a single static binary, ~10 MB (10,761,378 bytes on macOS arm64) with **no JavaScript runtime and no native dylib** — ECDSA verification is `crypto/ecdsa` and the syscalls go through `golang.org/x/sys`, all in-process Go. For comparison, npm and pnpm ship on top of (or bundle) a Node.js runtime, and bun's standalone binary bundles its JS engine.

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
