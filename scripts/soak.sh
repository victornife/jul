#!/usr/bin/env bash
#
# Soak harness — post-GA stability gate (ADR 0005).
#
# Runs the in-tree soak tests, which drive sustained concurrent traffic through
# real data paths and assert work succeeds while goroutine, heap and capacity
# state remain bounded. Three scenarios:
#
#   * proxy     — TestSoak (internal/handler): sustained HTTP requests through a
#                 real reverse-proxy handler; zero request errors plus steady
#                 goroutines/heap.
#   * cache     — TestCacheRecertificationSoak (internal/cache): mixed shared-cache
#                 traffic covering HIT/MISS/STALE/REVALIDATED/BYPASS, validation,
#                 invalidation, Vary membership and memory/disk capacity.
#   * udp-churn — TestSoakUDPChurn (internal/stream): sustained UDP source-address
#                 churn; live sessions stay capped and every reaped/evicted
#                 session tears down fully.
#
# The tests live behind the `soak` build tag so normal `go test ./...` stays
# bounded. CI runs short smoke windows; release workflows use longer windows.
#
# Usage:
#   scripts/soak.sh
#   SOAK_DURATION=5m SOAK_WORKERS=32 scripts/soak.sh
#   SOAK_SCENARIO=cache scripts/soak.sh
#
# Environment overrides:
#   SOAK_TAGS      build tags (default: full opt-in set, kept in sync with CI)
#   SOAK_DURATION  wall-clock run time per scenario (default 30s)
#   SOAK_WORKERS   concurrent clients per scenario (default 16)
#   SOAK_SCENARIO  proxy | cache | udp-churn | all (default all)
#
set -euo pipefail

DEFAULT_TAGS="brotli zstd acme console otel grpc http3 importer wasmplugins stream consul kubernetes"

TAGS="soak ${SOAK_TAGS:-$DEFAULT_TAGS}"
DURATION="${SOAK_DURATION:-30s}"
WORKERS="${SOAK_WORKERS:-16}"
SCENARIO="${SOAK_SCENARIO:-all}"

case "${SCENARIO}" in
	proxy)     PKGS=("./internal/handler/"); RUN='^TestSoak$' ;;
	cache)     PKGS=("./internal/cache/"); RUN='^TestCacheRecertificationSoak$' ;;
	udp-churn) PKGS=("./internal/stream/");  RUN='^TestSoakUDPChurn$' ;;
	all)       PKGS=("./internal/cache/" "./internal/handler/" "./internal/stream/"); RUN='^(TestCacheRecertificationSoak|TestSoak|TestSoakUDPChurn)$' ;;
	*)
		echo "unknown SOAK_SCENARIO='${SCENARIO}' (want: proxy | cache | udp-churn | all)" >&2
		exit 2
		;;
esac

echo "== soak gate (ADR 0005)"
echo "   scenario=${SCENARIO}"
echo "   tags=${TAGS}"
echo "   duration=${DURATION} workers=${WORKERS} (per scenario)"
echo

exec env SOAK_DURATION="${DURATION}" SOAK_WORKERS="${WORKERS}" \
	CACHE_SOAK_DURATION="${DURATION}" CACHE_SOAK_WORKERS="${WORKERS}" \
	go test \
	-tags "${TAGS}" \
	-run "${RUN}" \
	-count=1 \
	-timeout 0 \
	-v \
	"${PKGS[@]}"
