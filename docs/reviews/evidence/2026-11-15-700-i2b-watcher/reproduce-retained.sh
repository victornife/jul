#!/usr/bin/env bash
set -euo pipefail

repo="$(git rev-parse --show-toplevel)"
evidence="$repo/docs/reviews/evidence/2026-11-15-700-i2b-watcher"
scratch="$repo/.i2b-watcher-probe"
umask 077
if ! mkdir -m 700 "$scratch"; then
	echo "Fixture path already exists or cannot be created: $scratch; inspect it before another run" >&2
	exit 1
fi
echo "Fixture retained for manual inspection: $scratch" >&2
mkdir "$scratch/go-tmp"
export TMPDIR="$scratch/go-tmp" GOTMPDIR="$scratch/go-tmp"
version="$(go list -m -f '{{.Version}}' github.com/tetratelabs/wazero)"
if [ "$version" != "v1.12.0" ]; then
	echo "expected wazero v1.12.0, found $version" >&2
	exit 1
fi
source="$(go env GOMODCACHE)/github.com/tetratelabs/wazero@$version"
cp -R "$source" "$scratch/wazero"
if [ -L "$scratch/wazero" ] || [ -L "$scratch/wazero/internal" ] ||
	[ -L "$scratch/wazero/internal/wasm" ] || [ -L "$scratch/wazero/internal/wasm/module_instance.go" ]; then
	echo "Refusing symlink in copied patch path" >&2
	exit 1
fi
chmod u+w "$scratch/wazero/internal/wasm" "$scratch/wazero/internal/wasm/module_instance.go"
patch -s -d "$scratch/wazero" -p1 < "$evidence/wazero-before-select.patch"
cp "$repo/go.mod" "$scratch/probe.mod"
cp "$repo/go.sum" "$scratch/probe.sum"
go mod edit -modfile "$scratch/probe.mod" -replace "github.com/tetratelabs/wazero=$scratch/wazero"
python3 - "$repo" "$evidence" "$scratch" <<'PY'
import json
import os
import sys

repo, evidence, scratch = sys.argv[1:]
with open(os.path.join(evidence, "watcher-probe_test.go.txt"), encoding="utf-8") as f:
    probe = f.read()
old = "memoryLimitBytes: 64 << 10, closeOnContextDone: true,"
assert old in probe
probe = probe.replace(old, "memoryLimitBytes: 64 << 10,")
probe = probe.replace("inspection.GuestFailedRetained", "inspection.GuestTeardownUnconfirmed")
path = os.path.join(scratch, "watcher-probe_test.go")
with open(path, "w", encoding="utf-8") as f:
    f.write(probe)
with open(os.path.join(scratch, "overlay.json"), "w", encoding="utf-8") as f:
    json.dump({"Replace": {os.path.join(repo, "internal/plugins/zz_evidence_watcher_test.go"): path}}, f)
PY
case "${PROBE_RACE:-0}" in
	0) race_flag= ;;
	1) race_flag=-race ;;
	*) echo "PROBE_RACE must be 0 or 1" >&2; exit 1 ;;
esac
go test $race_flag -count=1 -tags wasmplugins -modfile "$scratch/probe.mod" \
	-overlay "$scratch/overlay.json" ./internal/plugins \
	-run '^TestOverlayRetains(UnreturnedStartModule|ClosedGuestFromWatcher)$'
