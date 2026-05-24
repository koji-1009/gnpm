# Pipelined cold install (hoisted path)

## Motivation

A cold install spends almost all of its wall time on the network, in two
phases that today run **strictly one after the other**:

| phase (cold vite-react, 5 runs, ms) | min | median | max |
|---|---|---|---|
| warmup packuments (metadata) | 1030 | **1252** | 5564 |
| resolve (tree) | 6 | 14 | 16 |
| fetch tarballs + ingest | 304 | **360** | 453 |
| link (materialize) | 3 | 3 | 3 |

`Warmup` (parallel packument prefetch) blocks to completion, then the
resolver runs (instant, all metadata cached), then tarballs are fetched.
So the two heavy phases — packument metadata (~1250 ms) and tarball
download + SHA-512 + extraction (~360 ms) — never overlap. Total ≈ their
sum (~1610 ms), matching the measured cold median of ~1735 ms.

Tarball work is independent of *other* packages' metadata: once a package's
version is final, its tarball can be fetched while the rest of the graph's
packuments are still arriving. bun and pnpm exploit exactly this — they
pipeline resolution and downloading. gnpm does not.

## Goal / non-goals

Goal: overlap tarball download + ingest with packument fetching on the
**cold, full-resolve, hoisted** path, hiding the ~360 ms tarball phase
under the ~1250 ms metadata phase. Expected cold ≈ max(metadata, tarball)
instead of their sum.

Non-goals / out of scope:
- The **warm** path (`runLocked`, the 8 ms relink) is untouched — it has
  no resolver and reads tarballs from the store.
- The **isolated** (pnpm-style) path keeps its blocking
  `Warmup` → pubgrub → fetch. Pubgrub backtracks (a tentatively assigned
  version can be retracted), so streaming its assignments into a downloader
  would fetch tarballs that resolution later discards. Pipelining is only
  safe where placements are final.
- Exotic (git/https) fetching already happens inside `ResolveExotic`
  during the walk; it is not part of this change.

## Why it is safe on the hoisted path

1. **The tree resolver is greedy and never backtracks.** `resolveEdge`
   creates a node with its chosen version and never moves or removes it.
   So the moment a node is created, its `(name, version)` is final and its
   tarball can be fetched without risk of waste.
2. **The packument cache is already concurrency-safe.**
   `regprovider.packumentFor` is a per-name singleflight (a `pkgResult`
   with a `done` channel under a mutex): concurrent callers for the same
   name coalesce onto one fetch, different names fetch in parallel. So a
   background prefetch and the resolver's synchronous asks can run at the
   same time against the same provider with no extra locking.
3. **Resolution stays pure.** The resolver gains one injected hook,
   `OnResolved(name, version)`, in the same dependency-injection spirit as
   `Provider` and `ResolveExotic`. It calls the hook; it owns no
   goroutines, channels, or I/O itself.

## Design

Replace, on the hoisted full-resolve path, the sequence

```
Warmup(declared)            // blocks until every packument is cached
placements = Resolve(req)   // instant (cache hits)
fetchTree(placements)       // download every tarball, then assemble
```

with an overlapped pipeline:

```
go Warmup(declared)                 // background: prefetch packuments
fetcher := newTarballFetcher(...)   // bounded worker pool (HTTPConcurrency)
req.OnResolved = fetcher.submit     // each finalized registry node → pool
placements = Resolve(req)           // emits as it walks; provider asks
                                    // coalesce with the background prefetch
fetcher.closeAndWait()              // drain downloads
<-warmupDone                        // join the prefetch (no goroutine leak)
specs, locks = assembleHoisted(placements, fetcher.infos, aliasByPackage)
```

Flow: the resolver's first ask (e.g. `react`) triggers that packument
fetch while the background `Warmup` concurrently pulls `react-dom`, `vite`,
and their dependencies. As each node is finalized the resolver hands its
`(name, version)` to the fetcher pool, which downloads + verifies +
ingests that tarball **while the resolver is still resolving (and metadata
for other packages is still arriving)**. The CPU of ingest (per-file
SHA-512 + extraction) overlaps the network of metadata fetches.

### Components

- **`treeresolver.Request.OnResolved func(name string, version semver.Version)`** —
  called from `resolveEdge` right after a registry node is created (final
  version). Not called from `placeExotic` (exotic tarballs/clones are
  fetched inside `ResolveExotic`). Nil hook ⇒ no streaming (behavior
  unchanged), so non-hoisted callers and tests are unaffected.

- **`tarballFetcher`** (installer) — a fixed pool of `HTTPConcurrency`
  workers reading a buffered channel. Per unique `name@version`
  (deduplicated under a mutex, since one version can be placed at many
  paths) a worker runs the existing per-package body: `SliceOf` →
  `platformMatches` (skip mismatched platform variants **before**
  downloading) → `Tarball` → `verifySignature` → `IngestTarball`, recording
  a `sliceInfo` in a mutex-guarded map. `submit` selects on `ctx.Done()` so
  a cancelled run never blocks the resolver on a full channel.

- **`assembleHoisted(placements, infos, aliasByPackage)`** — the existing
  fetchTree assembly loop, run **after** the pool drains. It iterates
  placements deterministically and builds the link specs + lockfile
  entries, so output is independent of download completion order.

### Platform filter

The resolver finalizes every optional platform variant (`@esbuild/*`,
`@rollup/*`, …) so the lockfile records them (pnpm does the same). Their
tarballs must **not** be downloaded on a mismatched host. The fetcher
applies `platformMatches` (os/cpu/libc from the already-fetched packument
slice) before downloading, exactly as `fetchTree` does today, so a streamed
placement for a foreign-platform package is skipped, not fetched.

### Error handling & cancellation

The pipeline runs under a `context.WithCancel` derived from the install
context. The first worker error is stored once and triggers `cancel()`;
that propagates through the shared `Provider`, so the resolver's next
packument ask fails and `Resolve` returns. After draining, the pipeline
returns the first of {fetcher error, resolve error}. `submit` and the
workers both select on `ctx.Done()`, so cancellation cannot deadlock the
resolver on channel back-pressure. The background `Warmup` is joined before
return (no leaked goroutine racing, e.g., test temp-dir cleanup).

### Determinism

Downloads complete in arbitrary order, but nothing order-dependent is
emitted while they run: link specs and lockfile entries are built only in
`assembleHoisted`, after the pool has drained, by walking `placements` in
their existing deterministic order. The resulting `node_modules` and
lockfile are byte-for-byte identical to the sequential path.

## Risks

- **Concurrency-bug surface.** Mitigated by: the resolver staying pure
  (all concurrency lives in `tarballFetcher`), reuse of the already-safe
  singleflight provider, deterministic post-drain assembly, and running the
  full installer test suite under `-race`.
- **Wasted downloads under `--frozen-lockfile` divergence.** If a frozen
  install reaches the full-resolve path and diverges from the lock, some
  tarballs may already be downloading when the divergence is detected. The
  error is still returned correctly; only bandwidth is wasted, in an error
  case that is rare (frozen installs normally take the locked fast path).
- **Bounded, network-dependent gain.** The overlap can only hide work up
  to the metadata phase length; if the metadata phase is short relative to
  tarballs (large tree, tiny metadata) the win shrinks. It does not close
  the entire gap to bun (which also wins on native parse/syscall cost), but
  it removes gnpm's one structural serialization.

## Validation

- Existing installer integration tests exercise the hoisted full-resolve
  path; they must stay green (correctness/determinism), and the suite is
  run with `-race`.
- Re-run `tools/bench/run.sh` and `tools/bench/install_phases.sh` cold to
  confirm the tarball phase moves under the metadata phase and cold median
  drops toward ~max(metadata, tarball).

### Measured outcome (cold vite-react, macOS arm64)

The full installer suite passes under `-race`. Phase breakdown
(`install_phases.sh`, 6 runs), which isolates compute from wall-clock
network noise:

| | before (sequential) | after (pipelined) |
|---|---|---|
| packument warmup | ~1252 ms (median) | — folded in — |
| tarball fetch + ingest | ~360 ms (median) | — folded in — |
| combined network phase | ~1612 ms (sum) | **~1103 ms (median, 940 min)** |

The ~360 ms tarball phase is now hidden under the metadata fetch: the
combined pipelined phase (~1103 ms) sits near the old warmup-alone time,
~30 % below the old sum. The greedy resolver, deterministic post-drain
assembly, and singleflight provider mean `node_modules` and the lockfile
are unchanged. (Cold *wall-clock* medians remain network-variance
dominated; the phase view is the stable signal.)
