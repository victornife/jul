#!/usr/bin/env bash
# Soak reproduction smoke test (JUL-AUD-003).
#
# docs/soak-evidence.md publishes exact reproduction commands so a soak run can
# be repeated rather than taken on faith. A stale command in that document is
# worse than no command at all — it fails silently until someone actually tries
# to run a 24-hour soak from it. This script runs the #287 resilience-soak
# reproduction (the currently open, actively-relied-upon one) at a trivial
# duration and asserts every step succeeds.
#
# Usage:
#   scripts/soak-repro-smoke.sh
#   SMOKE_DURATION=5s scripts/soak-repro-smoke.sh
set -euo pipefail

cd "$(git rev-parse --show-toplevel)"

FULL_TAGS="${FULL_TAGS:-brotli zstd acme console otel grpc http3 importer wasmplugins stream consul kubernetes waf}"
DURATION="${SMOKE_DURATION:-2s}"

WORKDIR="$(mktemp -d "${TMPDIR:-/tmp}/jul-soak-repro-smoke.XXXXXX")"
PIDS=()

cleanup() {
	local pid
	for pid in "${PIDS[@]:-}"; do
		[ -n "$pid" ] && kill "$pid" >/dev/null 2>&1 || true
	done
	wait 2>/dev/null || true
	rm -rf "$WORKDIR"
}
trap cleanup EXIT

echo "== building full-tag jul binary"
go build -tags "$FULL_TAGS" -o "$WORKDIR/jul" ./cmd/jul

echo "== starting backend :8081"
go run scripts/burn-in-backend.go -port 8081 >"$WORKDIR/backend-8081.log" 2>&1 &
PIDS+=("$!")
echo "== starting backend :8082"
go run scripts/burn-in-backend.go -port 8082 >"$WORKDIR/backend-8082.log" 2>&1 &
PIDS+=("$!")
echo "== starting TCP echo :55432"
go run scripts/stream-echo.go -port 55432 >"$WORKDIR/echo.log" 2>&1 &
PIDS+=("$!")

sleep 2

echo "== starting jul -config burn-in-resilience.toml"
"$WORKDIR/jul" -config burn-in-resilience.toml >"$WORKDIR/jul.log" 2>&1 &
JUL_PID="$!"
PIDS+=("$JUL_PID")

sleep 2
if ! kill -0 "$JUL_PID" 2>/dev/null; then
	echo "jul failed to start; log:" >&2
	cat "$WORKDIR/jul.log" >&2
	exit 1
fi

echo "== HTTP admission/resilience load (${DURATION})"
go run scripts/burn-in-load.go -duration "$DURATION" -workers 4 \
	-health "http://127.0.0.1:8080/bounded/" \
	-out "$WORKDIR/pprof"

echo "== L4 TCP stream load (${DURATION})"
go run scripts/burn-in-stream-load.go -duration "$DURATION" -workers 4 \
	-target 127.0.0.1:15432

echo "soak-repro-smoke: the documented #287 resilience-soak reproduction runs end to end"
