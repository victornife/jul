#!/usr/bin/env bash
#
# Advisory benchmark reporting tool.
#
# Runs the benchmark harness and, when benchstat is installed and a baseline
# file is present, prints a comparison for human review. This is ADVISORY
# ONLY: it never blocks CI and never auto-creates a baseline.
#
# Usage:
#   scripts/bench-compare.sh                   # run + compare if baseline present
#   scripts/bench-compare.sh --update-baseline # run and save as new baseline
#
# To establish a meaningful baseline, run on dedicated (not shared-CI) hardware
# with COUNT>=3, then commit docs/benchmarks-baseline.txt. Shared CI runners
# vary ±15–30% per run, making numeric regression gating unreliable there.
#
# Environment overrides (same as bench.sh):
#   BENCH_TAGS  build tags (default: full opt-in set)
#   BENCHTIME   -benchtime value (default: 2s)
#   COUNT       -count value (default: 1; use 3+ for a baseline update)
#
set -euo pipefail

BASELINE="${BASH_SOURCE[0]%/*}/../docs/benchmarks-baseline.txt"

UPDATE_BASELINE=0
for arg in "$@"; do
  if [ "$arg" = "--update-baseline" ]; then
    UPDATE_BASELINE=1
  fi
done

CURRENT=$(mktemp)
trap 'rm -f "$CURRENT"' EXIT

# Run the benchmark harness (smoke gate: every Benchmark* compiles and runs).
COUNT="${COUNT:-1}" BENCHTIME="${BENCHTIME:-2s}" "$(dirname "$0")/bench.sh" | tee "$CURRENT"

if [ "$UPDATE_BASELINE" -eq 1 ]; then
  cp "$CURRENT" "$BASELINE"
  echo ""
  echo "Baseline updated: $BASELINE"
  echo "Commit this file to record the new reference point."
  echo "Re-generate with COUNT=3 on dedicated hardware for statistical stability."
  exit 0
fi

if [ ! -f "$BASELINE" ]; then
  echo ""
  echo "No baseline at $BASELINE — comparison skipped."
  echo "Run with --update-baseline on dedicated hardware to establish one."
  exit 0
fi

if ! command -v benchstat &>/dev/null; then
  echo ""
  echo "benchstat not installed — comparison skipped."
  echo "Install with: go install golang.org/x/perf/cmd/benchstat@latest"
  exit 0
fi

echo ""
echo "== benchstat comparison (advisory — not a CI regression gate)"
benchstat "$BASELINE" "$CURRENT"
# Always exit 0: shared runners are too noisy for reliable thresholds.
# Review the comparison manually; use a self-hosted runner for real gating.
exit 0
