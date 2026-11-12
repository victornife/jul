#!/bin/bash
# Verify every archived run, then regenerate the summaries from the raw results.
# Usage, from the repository root:
#   go build -o /tmp/ia ./scripts/inspection-assessment
#   docs/reviews/evidence/2026-11-12-700-i2/scripts/regenerate-summaries.sh /tmp/ia OUT_DIR
# Compare OUT_DIR with docs/reviews/evidence/2026-11-12-700-i2/summary using diff -r.
set -euo pipefail
IA=${1:?harness binary}
OUT=${2:?output directory}
E=docs/reviews/evidence/2026-11-12-700-i2
R=$E/runs

for d in "$R"/*/ "$R"/pre-freeze-repeat-s6-s8/*/ "$R"/pre-freeze-repeat-s8-both/*/ "$R"/pre-freeze-smoke-suites/*/; do
	case "$d" in "$R"/pre-freeze-repeat-s6-s8/ | "$R"/pre-freeze-repeat-s8-both/ | "$R"/pre-freeze-smoke-suites/) continue ;; esac
	"$IA" verify "$d" >/dev/null
done

mkdir -p "$OUT"
sum() { "$IA" summarize "$@" >/dev/null; }
sum -out "$OUT/i2-normal" -compare main:cand -compare base:cand -compare base:main "$R/i2-normal"
sum -out "$OUT/i2-normal-supplement" -compare main:cand -compare base4:cand -compare base4:main -compare base:base4 "$R/i2-normal-supplement"
sum -out "$OUT/i2-normal-with-supplement" -compare main:cand -compare base:cand -compare base:main "$R/i2-normal" "$R/i2-normal-supplement"
sum -out "$OUT/i2-plugin" -compare main:cand "$R/i2-plugin"
sum -out "$OUT/i2-deadline" -compare main:cand "$R/i2-deadline"
sum -out "$OUT/i2-p3-repeat" -compare main:cand "$R/i2-p3-repeat"
sum -out "$OUT/i2-p1-none-repeat" -compare main:cand "$R/i2-p1-none-repeat"
sum -out "$OUT/i2-p4-709fix" -compare main:cand2 "$R/i2-p4-709fix"
python3 "$E/scripts/paired-ratios.py" "$R/i2-normal" "$R/i2-normal-supplement" >"$OUT/paired-ratios.md"
