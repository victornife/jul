#!/usr/bin/env bash
set -euo pipefail

repo="$(git rev-parse --show-toplevel)"
evidence="$repo/docs/reviews/evidence/2026-11-15-700-i2b-watcher"
version="$(go list -m -f '{{.Version}}' github.com/tetratelabs/wazero)"
if [ "$version" != "v1.12.0" ]; then
	echo "expected wazero v1.12.0, found $version" >&2
	exit 1
fi

scratch="$(mktemp -d "${TMPDIR:-/tmp}/jul-i2b-watcher.XXXXXXXX")"
echo "Scratch directory retained for inspection: $scratch" >&2
source="$(go env GOMODCACHE)/github.com/tetratelabs/wazero@$version"
if [ ! -d "$source" ]; then
	echo "wazero source is missing from the Go module cache: $source" >&2
	exit 1
fi
cp -R "$source" "$scratch/wazero"
chmod -R u+w "$scratch/wazero"
patch -s -d "$scratch/wazero" -p1 < "$evidence/wazero-before-select.patch"
cp "$repo/go.mod" "$scratch/probe.mod"
cp "$repo/go.sum" "$scratch/probe.sum"
go mod edit -modfile "$scratch/probe.mod" -replace "github.com/tetratelabs/wazero=$scratch/wazero"
python3 - "$repo" "$evidence" "$scratch" <<'PY'
import json
import os
import sys

repo, evidence, scratch = sys.argv[1:]
overlay = {
    "Replace": {
        os.path.join(repo, "internal/plugins/zz_evidence_watcher_test.go"):
            os.path.join(evidence, "watcher-probe_test.go.txt")
    }
}
with open(os.path.join(scratch, "overlay.json"), "w", encoding="utf-8") as f:
    json.dump(overlay, f)
PY

case "${PROBE_RACE:-0}" in
	0) race_flag= ;;
	1) race_flag=-race ;;
	*) echo "PROBE_RACE must be 0 or 1" >&2; exit 1 ;;
esac
if go test $race_flag -count=1 -tags wasmplugins -modfile "$scratch/probe.mod" \
	-overlay "$scratch/overlay.json" ./internal/plugins \
	-run '^TestOverlayRetains(UnreturnedStartModule|ClosedGuestFromWatcher)$' \
	> "$scratch/result.txt" 2>&1; then
	cat "$scratch/result.txt"
	echo "both invariants passed; reassess this historical blocker" >&2
	exit 1
fi
cat "$scratch/result.txt"
grep -Fq 'start failure refunded capacity=0 while a runtime watcher still held 65536 bytes' "$scratch/result.txt"
grep -Eq 'guest close refunded capacity=0 while [0-9]+ bytes of guest memory remained reachable after GC' "$scratch/result.txt"
echo "Both expected premature-refund failures were reproduced."
