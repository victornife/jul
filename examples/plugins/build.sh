#!/usr/bin/env bash
# Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
# SPDX-License-Identifier: agpl

# Builds the example plugins. Requires Go 1.26+ with the wasip1/wasm target.
#
#   ./build.sh              refresh the committed fixtures in testdata/plugins:
#                           the jul-abi/v2 guests and the current-toolchain v1
#                           guest (v1-current-header-inject.wasm). The other v1
#                           fixtures there are the historical regression fleet
#                           (docs/abi.md) and are never overwritten.
#   ./build.sh DIR all      build every example into DIR under its own name
#                           (CI compiles and runs them this way).
set -euo pipefail

cd "$(dirname "$0")"
out="${1:-../../testdata/plugins}"
mode="${2:-fixtures}"
mkdir -p "$out"

build() { # build <package> <output name>
	echo "building $1 -> $2.wasm"
	GOOS=wasip1 GOARCH=wasm go build -trimpath -buildvcs=false -buildmode=c-shared -o "$out/$2.wasm" "./$1"
}

v2=(v2-status-header v2-redact testguest-v2)
if [[ "$mode" == "all" ]]; then
	for p in header-inject request-block kv-counter egress-check testguest-panic testguest-loop "${v2[@]}"; do
		build "$p" "$p"
	done
else
	build header-inject v1-current-header-inject
	for p in "${v2[@]}"; do
		build "$p" "$p"
	done
fi
echo "done -> $out"
