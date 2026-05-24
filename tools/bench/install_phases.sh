#!/usr/bin/env bash
# Run `gnpm install` N times on a fixture with GNPM_PROFILE=1, parse the
# phase marks, and print min / median / max for each phase.
#
# Each iteration wipes:
#   - $HOME/.gnpm/cache + $HOME/.gnpm/store    (cold packument + tarball)
#   - <fixture>/node_modules + lockfiles        (forces resolve + link)
#
# Usage: tools/bench/install_phases.sh <gnpm-bin> <fixture-dir> [runs]
set -euo pipefail

gnpm_bin="${1:?usage: $0 <gnpm-bin> <fixture-dir> [runs]}"
fixture_dir="${2:?usage: $0 <gnpm-bin> <fixture-dir> [runs]}"
runs="${3:-5}"

[ -x "$gnpm_bin" ] || { echo "gnpm binary not executable: $gnpm_bin" >&2; exit 1; }
[ -d "$fixture_dir" ] || { echo "fixture not found: $fixture_dir" >&2; exit 1; }

raw=$(mktemp)
trap 'rm -f "$raw"' EXIT

for i in $(seq 1 "$runs"); do
  rm -rf "$HOME/.gnpm/cache" "$HOME/.gnpm/store" \
         "$fixture_dir/node_modules" \
         "$fixture_dir/package-lock.json" \
         "$fixture_dir/pnpm-lock.yaml"
  echo "=== run $i ===" >> "$raw"
  (cd "$fixture_dir" && GNPM_PROFILE=1 "$gnpm_bin" install --ignore-scripts 2>&1) >> "$raw"
done

extract_ms() {
  # Pulls a trailing "<N.NN> ms" off a PHASE line.
  awk '{
    for (i = NF; i > 0; i--) {
      if ($i ~ /^[0-9.]+$/ && $(i+1) == "ms") { print $i; next }
    }
  }'
}

median() {
  sort -n | awk '{a[NR]=$1} END{
    if (NR == 0) {print "N/A"}
    else if (NR % 2) {print a[(NR+1)/2]}
    else {print (a[NR/2] + a[NR/2+1]) / 2}
  }'
}

# Distinct phase labels GNPM_PROFILE prints (see installer phaseTimer.mark).
# A PHASE line is: "  PHASE <label padded to 24> <NN.NN> ms"; the label may
# contain spaces, so strip the leading "PHASE" and the trailing "<num> ms"
# with [[:space:]] (portable across BSD/GNU sed, unlike \s).
phases=$(grep -E '^[[:space:]]*PHASE ' "$raw" \
         | sed -E 's/^[[:space:]]*PHASE +//; s/ +[0-9.]+ ms[[:space:]]*$//' \
         | sort -u)

printf '%-26s %8s %8s %8s\n' "phase" "min" "median" "max"
printf '%-26s %8s %8s %8s\n' "-----" "---" "------" "---"
while IFS= read -r name; do
  [ -z "$name" ] && continue
  values=$(grep -F "PHASE $name " "$raw" | extract_ms)
  [ -z "$values" ] && continue
  mn=$(echo "$values" | sort -n | head -1)
  md=$(echo "$values" | median)
  mx=$(echo "$values" | sort -n | tail -1)
  printf '%-26s %8s %8s %8s\n' "$name" "$mn" "$md" "$mx"
done <<< "$phases"
