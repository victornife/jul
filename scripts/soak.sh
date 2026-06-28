#!/usr/bin/env bash
#
# Soak harness — post-GA stability gate (ADR 0005).
#
# Runs the in-tree soak tests, which drive sustained traffic through real proxy
# data paths and assert the process stays healthy over time: work succeeds, the
# goroutine count returns to a steady state, and the heap does not grow without
# bound (a leak gate). Two scenarios:
#
#   * proxy     — TestSoak (internal/handler): sustained concurrent HTTP requests
#                 through a real reverse-proxy handler; zero request errors plus
#                 steady goroutines/heap.
#   * udp-churn — TestSoakUDPChurn (internal/stream): sustained UDP source-address
#                 churn through a real stream listener; live sessions stay capped
#                 at max_udp_sessions and every reaped/evicted session tears down
#                 fully (no goroutine/backend-socket leak). Backs the v1.16 UDP
#                 session-safety hardening — a public-internet DoS guard.
#
# Both live behind the `soak` build tag so they never run in the normal
# `go test ./...`; this script adds that tag.
#
# Two ways it is used:
#   * CI (`soak (smoke)` job) runs it as a smoke gate with a short SOAK_DURATION:
#     the harness must keep compiling and the proxies must survive a brief burst
#     without errors or a leak. This keeps the gate from rotting between releases.
#   * The release workflow (`.github/workflows/release.yml`, on a version tag)
#     runs it for several minutes as the actual soak gate. A red run blocks the
#     release job — the "block tag on red" gate ADR 0005 calls for.
#
# Usage:
#   scripts/soak.sh                          # both scenarios, 30s each
#   SOAK_DURATION=5m SOAK_WORKERS=32 scripts/soak.sh        # release-style run
#   SOAK_SCENARIO=udp-churn scripts/soak.sh  # only the UDP churn scenario
#
# Environment overrides:
#   SOAK_TAGS      build tags (default: the full opt-in set, kept in sync with CI)
#   SOAK_DURATION  wall-clock run time per scenario (default 30s)
#   SOAK_WORKERS   concurrent clients per scenario  (default 16)
#   SOAK_SCENARIO  proxy | udp-churn | all          (default all)
#
set -euo pipefail

# Keep in sync with .github/workflows/ci.yml `env.FULL_TAGS` (minus waf, which the
# soak does not exercise). The `soak` tag activates the soak tests; `stream` is
# required for the udp-churn scenario.
DEFAULT_TAGS="brotli zstd acme console otel grpc http3 importer wasmplugins stream consul kubernetes"

TAGS="soak ${SOAK_TAGS:-$DEFAULT_TAGS}"
DURATION="${SOAK_DURATION:-30s}"
WORKERS="${SOAK_WORKERS:-16}"
SCENARIO="${SOAK_SCENARIO:-all}"

# Map the scenario to its package(s) and -run pattern. `^TestSoak` matches both
# TestSoak (proxy) and TestSoakUDPChurn (udp-churn) when running all scenarios.
case "${SCENARIO}" in
	proxy)     PKGS=("./internal/handler/"); RUN='^TestSoak$' ;;
	udp-churn) PKGS=("./internal/stream/");  RUN='^TestSoakUDPChurn$' ;;
	all)       PKGS=("./internal/handler/" "./internal/stream/"); RUN='^TestSoak' ;;
	*)
		echo "unknown SOAK_SCENARIO='${SCENARIO}' (want: proxy | udp-churn | all)" >&2
		exit 2
		;;
esac

echo "== soak gate (ADR 0005)"
echo "   scenario=${SCENARIO}"
echo "   tags=${TAGS}"
echo "   duration=${DURATION} workers=${WORKERS} (per scenario)"
echo

# -run pins the soak tests; -count=1 defeats the test cache so the soak always
# runs; -timeout 0 disables Go's test timeout so a long release soak is not killed.
exec env SOAK_DURATION="${DURATION}" SOAK_WORKERS="${WORKERS}" \
	go test \
	-tags "${TAGS}" \
	-run "${RUN}" \
	-count=1 \
	-timeout 0 \
	-v \
	"${PKGS[@]}"
