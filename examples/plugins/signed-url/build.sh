#!/usr/bin/env bash
# Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
# SPDX-License-Identifier: agpl
set -euo pipefail
cd "$(dirname "$0")"
GOOS=wasip1 GOARCH=wasm go build -trimpath -buildvcs=false -buildmode=c-shared -o signed-url.wasm .
