#!/usr/bin/env bash
# Install-time benchmark for gnpm vs other package managers.
#
# Measures cold + warm install time and peak memory for a chosen
# fixture across gnpm, pnpm, npm, bun. Every tool installs against its
# own fresh temp cache via a redirected HOME, which isolates each tool's
# entire cache (package store + packument metadata), so the user's real
# caches are never touched and a wiped dir is a true cold start. Cold
# runs use an empty cache; warm runs reuse a seeded cache + lockfile and
# only clear node_modules (which also drops gnpm's node_modules/.gnpm
# workspace state, so gnpm relinks rather than short-circuiting).
#
# Cold is run INTERLEAVED (one round = every tool back-to-back in a
# shuffled order, repeated) because it is network-bound and the CDN
# drifts minute-to-minute; measuring each tool in its own block would
# give tools different network windows and an unfair, non-reproducible
# gap. Warm is local-only, so it stays a per-tool block.
#
# Defaults are asymmetric on purpose:
#   - cold = 15 runs.  Network-bound; observed spreads of 5x are
#     common, so 15 samples is the minimum that yields a stable
#     median without pounding the CDN.
#   - warm = 20 runs.  No network cost (packument freshness window +
#     store hits make warm a pure local-CPU/IO scenario), so sampling
#     more is free.
#
# Warm timings here come from `/usr/bin/time`, which on macOS reports
# `real` at 0.01s resolution. That is too coarse for sub-100ms tools
# (gnpm/bun warm peg at 0). For ms-precise warm timings drive each tool
# through hyperfine separately:
#   hyperfine --warmup 2 --runs 20 --prepare 'rm -rf node_modules' \
#     '<tool> install'
#
# macOS and Linux are supported (different `/usr/bin/time` flags).
set -euo pipefail

show_help() {
  cat <<'EOF'
Usage: tools/bench/run.sh [options]

Options:
  --fixture NAME     fixture under tools/bench/fixtures (default: vite-react)
  --cold-runs N      cold-scenario repetitions (default: 15)
  --warm-runs N      warm-scenario repetitions (default: 20)
  --runs N           shortcut that sets both cold-runs and warm-runs to N
  --tools LIST       comma-separated subset of: gnpm,pnpm,npm,bun (default: gnpm,pnpm)
  --gnpm-bin PATH    use an existing gnpm binary; otherwise `go build` is invoked
  -h, --help         this help

Example:
  tools/bench/run.sh --fixture vite-react --tools gnpm,pnpm,npm,bun
EOF
}

fixture="vite-react"
cold_runs=15
warm_runs=20
tools_csv="gnpm,pnpm"
gnpm_bin="${GNPM_BIN:-}"

while [ "$#" -gt 0 ]; do
  case "$1" in
    --fixture)   fixture="$2"; shift 2 ;;
    --cold-runs) cold_runs="$2"; shift 2 ;;
    --warm-runs) warm_runs="$2"; shift 2 ;;
    --runs)      cold_runs="$2"; warm_runs="$2"; shift 2 ;;
    --tools)     tools_csv="$2"; shift 2 ;;
    --gnpm-bin)  gnpm_bin="$2"; shift 2 ;;
    -h|--help)   show_help; exit 0 ;;
    *)           echo "unknown arg: $1" >&2; exit 64 ;;
  esac
done

# --- platform-specific `/usr/bin/time` plumbing ------------------------------

case "$(uname -s)" in
  Darwin)
    # `/usr/bin/time -lp` prints `real`, `user`, `sys` plus extended
    # stats including `peak memory footprint` (in bytes).
    time_flag="-lp"
    parse_real() { awk '/^real/ {print $2}'; }
    parse_peak() { awk '/peak memory footprint/ {print $1}'; }
    ;;
  Linux)
    # `/usr/bin/time -v` prints `Elapsed (wall clock) time` (HH:MM:SS.SS)
    # and `Maximum resident set size (kbytes)`.
    time_flag="-v"
    parse_real() {
      awk '/Elapsed \(wall/ {
        n=split($NF, parts, ":")
        if (n == 3) print parts[1]*3600 + parts[2]*60 + parts[3]
        else        print parts[1]*60 + parts[2]
      }'
    }
    parse_peak() {
      awk '/Maximum resident set size/ {printf "%d", $NF * 1024}'
    }
    ;;
  *)
    echo "unsupported OS: $(uname -s) (macOS / Linux only)" >&2
    exit 1
    ;;
esac

repo_root="$(cd "$(dirname "$0")/../.." && pwd)"
fixture_dir="$repo_root/tools/bench/fixtures/$fixture"
[ -d "$fixture_dir" ] || { echo "fixture not found: $fixture_dir" >&2; exit 64; }

# One scratch root holds every tool's throwaway cache and the optional gnpm
# build, so a single trap cleans up and the benchmark never touches the
# user's real ~/.gnpm, pnpm, npm, or bun caches.
bench_tmp="$(mktemp -d -t gnpm-bench.XXXXXX)"
trap 'rm -rf "$bench_tmp"' EXIT

# Build gnpm if no binary was supplied.
if [ -z "$gnpm_bin" ]; then
  echo "building gnpm binary (one-time)..." >&2
  (cd "$repo_root" && go build -o "$bench_tmp/gnpm" ./cmd/gnpm)
  gnpm_bin="$bench_tmp/gnpm"
fi
[ -x "$gnpm_bin" ] || { echo "gnpm binary not executable: $gnpm_bin" >&2; exit 1; }
# Install commands run after `cd` into the fixture dir, so a relative
# --gnpm-bin would no longer resolve — absolutize it now.
case "$gnpm_bin" in
  /*) ;;
  *)  gnpm_bin="$(cd "$(dirname "$gnpm_bin")" && pwd)/$(basename "$gnpm_bin")" ;;
esac

# --- per-tool helpers --------------------------------------------------------

# Each tool installs against its own fresh temp cache under $bench_tmp, so the
# user's real caches are never touched and a wiped dir is a genuine cold
# start. We redirect HOME for every tool (gnpm ~/.gnpm, pnpm ~/Library/pnpm
# + ~/Library/Caches/pnpm, npm ~/.npm, bun ~/.bun all live under $HOME), so a
# single mechanism isolates each tool's *entire* cache — both the package
# store and the packument metadata. (A pnpm-only `--store-dir` would leave its
# metadata cache warm, making its cold unfairly fast versus the others.)
tool_cache() { echo "$bench_tmp/cache-$1"; }

reset_cache() {
  rm -rf "$(tool_cache "$1")"
  mkdir -p "$(tool_cache "$1")"
}

clear_project() {
  rm -rf "$fixture_dir/node_modules" \
         "$fixture_dir/package-lock.json" \
         "$fixture_dir/pnpm-lock.yaml" \
         "$fixture_dir/bun.lock" \
         "$fixture_dir/bun.lockb" \
         "$fixture_dir/yarn.lock"
}

tool_command() {
  case "$1" in
    gnpm) echo "$gnpm_bin --silent install --ignore-scripts" ;;
    pnpm) echo "pnpm install --ignore-scripts" ;;
    npm)  echo "npm install --ignore-scripts" ;;
    bun)  echo "bun install --ignore-scripts" ;;
  esac
}

# Env prefix that points the tool's whole cache at its isolated temp HOME.
tool_env() { echo "HOME=$(tool_cache "$1")"; }

# Populate a tool's cache + lockfile without timing it (used to seed warm).
seed_install() {
  local cmd; cmd="$(tool_command "$1")"
  (cd "$fixture_dir" && env $(tool_env "$1") $cmd >/dev/null 2>&1 || true)
}

# Run one timed install against the tool's isolated cache; print
# "<seconds> <peak_bytes>".
run_once() {
  local cmd stderr_log
  cmd="$(tool_command "$1")"
  stderr_log="$(mktemp "$bench_tmp/stderr.XXXXXX")"
  (cd "$fixture_dir" && env $(tool_env "$1") /usr/bin/time $time_flag $cmd >/dev/null 2>"$stderr_log") \
    || { echo "install failed for $1:" >&2; cat "$stderr_log" >&2; rm -f "$stderr_log"; return 1; }
  local real peak
  real="$(parse_real < "$stderr_log")"
  peak="$(parse_peak < "$stderr_log")"
  rm -f "$stderr_log"
  echo "$real $peak"
}

median() {
  # numbers on stdin, one per line.
  sort -n | awk '
    {a[NR]=$1}
    END {
      if (NR == 0)        {print "0"}
      else if (NR % 2)    {print a[(NR+1)/2]}
      else                {print (a[NR/2] + a[NR/2+1]) / 2}
    }'
}

min() { sort -n | head -1; }
max() { sort -n | tail -1; }

# Fisher-Yates shuffle of the positional args, one per line. Randomizing the
# within-round order keeps any tool from always installing first (and e.g.
# warming the DNS/TCP path for the rest). Uses $RANDOM, so it needs no
# external `shuf`/`sort -R`.
shuffle() {
  local arr=("$@") i j tmp
  for ((i=${#arr[@]}-1; i>0; i--)); do
    j=$(( RANDOM % (i + 1) ))
    tmp="${arr[i]}"; arr[i]="${arr[j]}"; arr[j]="$tmp"
  done
  printf '%s\n' "${arr[@]}"
}

format_ms() {
  # `%d` truncates to zero for sub-ms runs (gnpm/bun-warm hit this);
  # `%.1f` keeps a digit for small values without faking precision
  # at the second scale.
  awk -v s="$1" 'BEGIN {
    ms = s * 1000
    if (ms >= 100) printf "%d ms", ms
    else           printf "%.1f ms", ms
  }'
}
format_mb() { awk -v b="$1" 'BEGIN {printf "%.1f MB", b / 1024 / 1024}'; }

# --- main loop ---------------------------------------------------------------
#
# Resolve the requested tools to those actually installed.
IFS=',' read -ra requested <<< "$tools_csv"
tool_list=()
for tool in "${requested[@]}"; do
  if [ "$tool" != "gnpm" ] && ! command -v "$tool" >/dev/null 2>&1; then
    echo "skipping $tool (not on PATH)" >&2
    continue
  fi
  tool_list+=("$tool")
done
[ "${#tool_list[@]}" -gt 0 ] || { echo "no runnable tools" >&2; exit 1; }

samples="$bench_tmp/samples"
mkdir -p "$samples"

# Cold is network-bound and the registry/CDN drifts on a minute scale, so
# measuring all of tool A then all of tool B lets each tool land in its own
# (faster or slower) network window — an unfair, non-reproducible gap. Run
# the tools INTERLEAVED instead: one round installs every tool back-to-back
# (in a shuffled order, so no tool is always first), so the drift hits them
# inside the same window and cancels across rounds.
echo "cold: $cold_runs interleaved rounds over ${tool_list[*]}..." >&2
for r in $(seq 1 "$cold_runs"); do
  for tool in $(shuffle "${tool_list[@]}"); do
    reset_cache "$tool"   # genuine cold: empty, isolated cache
    clear_project
    if read -r t p <<< "$(run_once "$tool")"; then
      echo "$t $p" >> "$samples/cold-$tool.txt"
    fi
  done
done

# Warm hits the store + lockfile, not the network, so there is no window to
# cancel — a per-tool block is fine. clear_project drops the lockfile, so
# re-seed before each measurement to restore it (and warm the cache).
echo "warm: $warm_runs runs per tool..." >&2
for tool in "${tool_list[@]}"; do
  reset_cache "$tool"
  for _ in $(seq 1 "$warm_runs"); do
    clear_project
    if [ ! -d "$fixture_dir/node_modules" ]; then
      seed_install "$tool"
      rm -rf "$fixture_dir/node_modules"
    fi
    if read -r t p <<< "$(run_once "$tool")"; then
      echo "$t $p" >> "$samples/warm-$tool.txt"
    fi
  done
done

# --- table -------------------------------------------------------------------

echo "## bench: $fixture (cold N=$cold_runs interleaved / warm N=$warm_runs)"
echo
echo "| tool | scenario | best | median | worst | peak memory |"
echo "|------|----------|------|--------|-------|-------------|"
for tool in "${tool_list[@]}"; do
  for scenario in cold warm; do
    f="$samples/$scenario-$tool.txt"
    if [ ! -s "$f" ]; then
      echo "| $tool | $scenario | (no samples) | | | |"
      continue
    fi
    min_t=$(awk '{print $1}' "$f" | min)
    median_t=$(awk '{print $1}' "$f" | median)
    max_t=$(awk '{print $1}' "$f" | max)
    median_p=$(awk '{print $2}' "$f" | median)
    echo "| $tool | $scenario | $(format_ms "$min_t") | $(format_ms "$median_t") | $(format_ms "$max_t") | $(format_mb "$median_p") |"
  done
done
