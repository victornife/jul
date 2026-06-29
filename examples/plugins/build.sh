#!/usr/bin/env bash
# Builds the example plugins to ../../testdata/plugins/<name>.wasm.
# Requires Go 1.26+ with the wasip1/wasm target.
set -euo pipefail

cd "$(dirname "$0")"
out="../../testdata/plugins"
mkdir -p "$out"

plugins=(header-inject request-block kv-counter egress-check testguest-panic testguest-loop)
for p in "${plugins[@]}"; do
	echo "building $p"
	GOOS=wasip1 GOARCH=wasm go build -buildmode=c-shared -o "$out/$p.wasm" "./$p"
done
echo "done -> $out"
