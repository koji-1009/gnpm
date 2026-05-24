#!/usr/bin/env bash
# Install-time benchmark for gnpm vs other package managers.
#
# Measures cold + warm install time and peak memory for a chosen
# fixture across gnpm, pnpm, npm, bun. Cold runs wipe each tool's
# global cache/store first; warm runs only clear node_modules (which
# also drops gnpm's node_modules/.gnpm workspace state, so gnpm
# actually relinks rather than short-circuiting).
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

# Build gnpm if no binary was supplied.
if [ -z "$gnpm_bin" ]; then
  build_dir="$(mktemp -d -t gnpm-bench.XXXXXX)"
  trap 'rm -rf "$build_dir"' EXIT
  echo "building gnpm binary (one-time)..." >&2
  (cd "$repo_root" && go build -o "$build_dir/gnpm" ./cmd/gnpm)
  gnpm_bin="$build_dir/gnpm"
fi
[ -x "$gnpm_bin" ] || { echo "gnpm binary not executable: $gnpm_bin" >&2; exit 1; }

# --- per-tool helpers --------------------------------------------------------

clear_global_cache() {
  case "$1" in
    gnpm)
      rm -rf "$HOME/.gnpm/cache" "$HOME/.gnpm/store"
      ;;
    pnpm)
      rm -rf "$HOME/Library/pnpm/store" \
             "$HOME/.local/share/pnpm/store" \
             "$HOME/Library/Caches/pnpm" \
             "$HOME/.cache/pnpm"
      ;;
    npm)
      rm -rf "$HOME/.npm/_cacache"
      ;;
    bun)
      rm -rf "$HOME/.bun/install/cache"
      ;;
  esac
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

# Run one install, print "<seconds> <peak_bytes>".
run_once() {
  local cmd
  cmd="$(tool_command "$1")"
  local stderr_log
  stderr_log="$(mktemp)"
  (cd "$fixture_dir" && /usr/bin/time $time_flag $cmd >/dev/null 2>"$stderr_log") \
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

echo "## bench: $fixture (cold N=$cold_runs / warm N=$warm_runs)"
echo
echo "| tool | scenario | best | median | worst | peak memory |"
echo "|------|----------|------|--------|-------|-------------|"

IFS=',' read -ra tool_list <<< "$tools_csv"
for tool in "${tool_list[@]}"; do
  if ! command -v "$tool" >/dev/null 2>&1 && [ "$tool" != "gnpm" ]; then
    echo "| $tool | — | (not on PATH, skipped) | |"
    continue
  fi
  for scenario in cold warm; do
    if [ "$scenario" = "cold" ]; then
      n="$cold_runs"
    else
      n="$warm_runs"
    fi
    times=() peaks=()
    for _ in $(seq 1 "$n"); do
      clear_project
      [ "$scenario" = "cold" ] && clear_global_cache "$tool"
      # warm scenarios need an established lockfile/cache from a
      # prior run; do a single seed install when needed.
      if [ "$scenario" = "warm" ] && [ ! -d "$fixture_dir/node_modules" ]; then
        (cd "$fixture_dir" && $(tool_command "$tool") >/dev/null 2>&1 || true)
        rm -rf "$fixture_dir/node_modules"
      fi
      read -r t p <<< "$(run_once "$tool")"
      times+=("$t")
      peaks+=("$p")
    done
    median_t=$(printf '%s\n' "${times[@]}" | median)
    min_t=$(printf '%s\n' "${times[@]}" | min)
    max_t=$(printf '%s\n' "${times[@]}" | max)
    median_p=$(printf '%s\n' "${peaks[@]}" | median)
    echo "| $tool | $scenario | $(format_ms "$min_t") | $(format_ms "$median_t") | $(format_ms "$max_t") | $(format_mb "$median_p") |"
  done
done
