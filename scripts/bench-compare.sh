#!/usr/bin/env bash
#
# Benchmark comparison gate.
#
# Runs the benchmark harness and compares results against a stored baseline
# using `benchstat`. Exits non-zero when any benchmark regresses beyond the
# configured threshold, so performance regressions fail CI just like test
# failures.
#
# Usage:
#   scripts/bench-compare.sh                      # compare vs stored baseline
#   scripts/bench-compare.sh --update-baseline    # update the baseline file
#
# The baseline file is docs/benchmarks-baseline.txt. Committing it pins the
# expected performance level; Dependabot/renovate does not touch it.
# Re-generate it on a quiet, representative machine (not a shared CI runner)
# after intentional performance improvements.
#
# Environment overrides (same as bench.sh):
#   BENCH_TAGS   build tags (default: full opt-in set)
#   BENCHTIME    -benchtime value for the comparison run (default: 2s)
#   COUNT        -count value — must be >=2 for benchstat to compute variance
#   THRESHOLD    benchstat -delta-test threshold (default: 0.05, i.e. 5%)
#
set -euo pipefail

BASELINE="${BASH_SOURCE[0]%/*}/../docs/benchmarks-baseline.txt"
THRESHOLD="${THRESHOLD:-0.05}"

UPDATE_BASELINE=0
for arg in "$@"; do
  if [ "$arg" = "--update-baseline" ]; then
    UPDATE_BASELINE=1
  fi
done

# benchstat must be installed: go install golang.org/x/perf/cmd/benchstat@latest
if ! command -v benchstat &>/dev/null; then
  echo "benchstat not found — install with: go install golang.org/x/perf/cmd/benchstat@latest"
  echo "Falling back to smoke-only mode (no comparison)."
  exec "$(dirname "$0")/bench.sh"
fi

CURRENT=$(mktemp)
trap 'rm -f "$CURRENT"' EXIT

# Run with count>=2 so benchstat has variance data.
COUNT="${COUNT:-3}" BENCHTIME="${BENCHTIME:-2s}" "$(dirname "$0")/bench.sh" > "$CURRENT"

if [ "$UPDATE_BASELINE" -eq 1 ]; then
  cp "$CURRENT" "$BASELINE"
  echo ""
  echo "Baseline updated: $BASELINE"
  echo "Commit this file to pin the new expected performance level."
  exit 0
fi

if [ ! -f "$BASELINE" ]; then
  echo "No baseline file at $BASELINE. Run with --update-baseline first."
  echo "Treating this run as the initial baseline (no comparison)."
  cp "$CURRENT" "$BASELINE"
  exit 0
fi

echo ""
echo "== benchstat comparison (threshold: ${THRESHOLD})"
benchstat -delta-test none "$BASELINE" "$CURRENT"

echo ""
echo "== regressions check (>${THRESHOLD} slower)"
# benchstat exits 0 regardless; parse its output to detect regressions.
REGRESSIONS=$(benchstat "$BASELINE" "$CURRENT" 2>&1 | grep -E '^\w.*\+[0-9]' | \
  awk -v threshold="$THRESHOLD" '
    match($0, /\+([0-9]+(\.[0-9]+)?)%/, m) {
      if (m[1]/100 > threshold) print
    }
  ' || true)

if [ -n "$REGRESSIONS" ]; then
  echo "FAIL: the following benchmarks regressed beyond ${THRESHOLD}:"
  echo "$REGRESSIONS"
  exit 1
fi
echo "PASS: no regressions beyond ${THRESHOLD} threshold."
