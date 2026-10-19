#!/usr/bin/env bash
# Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
# SPDX-License-Identifier: agpl
# Own the test context outside reports and the operator's persistent state.
set -euo pipefail
cd "$(git rev-parse --show-toplevel)"
fixture="$(mktemp -d)"
pid=''
cleanup() {
  if [[ -n "$pid" ]]; then kill -TERM "$pid" 2>/dev/null || true; wait "$pid" 2>/dev/null || true; fi
  rm -rf "$fixture"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
export JUL_EFFECTIVE_VERSION_KEY_FILE="$fixture/private/version.key"
go build -tags 'console grpc' -o "$fixture/jul" ./cmd/jul
"$fixture/jul" version-key init >/dev/null
"$fixture/jul" serve -config testdata/console-e2e.toml &
pid=$!
wait "$pid"
