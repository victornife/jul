#!/usr/bin/env bash
set -euo pipefail

repo="$(git rev-parse --show-toplevel)"
cd "$repo"
evidence="$repo/docs/reviews/evidence/2026-11-16-700-i2b-join"
version="$(go list -m -f '{{.Version}}' github.com/tetratelabs/wazero)"
if [ "$version" != "v1.12.0" ]; then
	echo "expected wazero v1.12.0, found $version" >&2
	exit 1
fi
source="$(go env GOMODCACHE)/github.com/tetratelabs/wazero@$version"
if [ ! -d "$source" ]; then
	echo "wazero source missing from module cache: $source" >&2
	exit 1
fi
scratch="$(mktemp -d "${TMPDIR:-/tmp}/jul-i2b-join.XXXXXXXX")"
echo "Scratch directory retained for inspection: $scratch" >&2
cp -R "$source" "$scratch/wazero"
chmod -R u+w "$scratch/wazero"
cp "$repo/go.mod" "$scratch/probe.mod"
cp "$repo/go.sum" "$scratch/probe.sum"
go mod edit -modfile "$scratch/probe.mod" -replace "github.com/tetratelabs/wazero=$scratch/wazero"
patch -s -d "$scratch/wazero" -p1 < "$evidence/instrument.patch"
python3 - "$repo" "$evidence" "$scratch" <<'PY'
import json
import os
import sys

repo, evidence, scratch = sys.argv[1:]
overlay = {"Replace": {
    os.path.join(repo, "internal/plugins/zz_join_probe_test.go"):
        os.path.join(evidence, "join-probe_test.go.txt")
}}
with open(os.path.join(scratch, "overlay.json"), "w", encoding="utf-8") as f:
    json.dump(overlay, f)
PY

if go test -count=1 -timeout=90s -tags wasmplugins -modfile "$scratch/probe.mod" \
	-overlay "$scratch/overlay.json" ./internal/plugins \
	-run '^TestJoinProbeGuestStartupAndRetirement$' > "$scratch/baseline.txt" 2>&1; then
	cat "$scratch/baseline.txt"
	echo "unexpected baseline success" >&2
	exit 1
fi
cat "$scratch/baseline.txt"
grep -Fq 'startup returned before watcher exited' "$scratch/baseline.txt"

patch -s -d "$scratch/wazero" -p1 < "$evidence/join.patch"
go test -race -count=5 -timeout=90s -tags wasmplugins -modfile "$scratch/probe.mod" \
	-overlay "$scratch/overlay.json" ./internal/plugins \
	-run '^TestJoinProbe(GuestStartupAndRetirement|AfterSignalReachability)$' \
	> "$scratch/join.txt" 2>&1
cat "$scratch/join.txt"
echo "Baseline failed as expected; both experimental join probes passed."
