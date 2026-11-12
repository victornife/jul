#!/bin/bash
# Verify every archived run, then regenerate the summaries from the raw results.
# Usage, from the repository root:
#   go build -o /tmp/ia ./scripts/inspection-assessment
#   docs/reviews/evidence/2026-11-12-700-i5/scripts/regenerate-summaries.sh /tmp/ia OUT_DIR
# Compare OUT_DIR with docs/reviews/evidence/2026-11-12-700-i5/summary using diff -r.
set -euo pipefail
IA=${1:?harness binary}
OUT=${2:?output directory}
R=docs/reviews/evidence/2026-11-12-700-i5/runs
CMP="-compare base:main -compare base4:main -compare base:base4"

for d in "$R"/pre-freeze-controlled/* "$R"/*; do
	[ "$(basename "$d")" = pre-freeze-controlled ] && continue
	"$IA" verify "$d" >/dev/null
done

mkdir -p "$OUT"
sum() { "$IA" summarize "$@" >/dev/null; }
sum -out "$OUT/calibration" "$R/calibration"
sum -out "$OUT/campaign-normal" $CMP "$R/campaign-normal"
sum -out "$OUT/campaign-normal-with-supplement" $CMP "$R/campaign-normal" "$R/supplement-u1-none"
sum -out "$OUT/fidelity" "$R/fidelity"
sum -out "$OUT/investigate-s7-none" "$R/investigate-s7-none"
sum -out "$OUT/diagnose-s6-both" "$R/diagnose-s6-both"
sum -out "$OUT/controlled-success" "$R/controlled-success"
sum -out "$OUT/controlled-watchdog" "$R/controlled-watchdog"
