#!/usr/bin/env bash
#
# Perf-gate benchmark harness.
#
# Runs every in-tree Benchmark* across the module with the full opt-in build-tag
# set, so feature benchmarks that live behind a tag (gRPC transcoding/passthrough,
# mTLS, ...) are included alongside the always-on ones (auth, router, balancer,
# static, TLS).
#
# Two ways it is used:
#   * CI (`benchmarks` job) runs it as a *smoke gate*: every benchmark must
#     compile and execute without panicking. A tiny -benchtime keeps CI minutes
#     low. This is deliberately NOT a nanosecond regression gate -- GitHub's
#     shared runners are far too noisy for reliable numeric gating. The captured
#     output is uploaded as an artifact so the numbers quoted in
#     docs/<feature>.md can be spot-checked.
#   * Locally / on a quiet dedicated machine it (re)generates the numbers that
#     the feature docs quote. Use a longer -benchtime there for a stable signal.
#
# Usage:
#   scripts/bench.sh                       # measurement run (-benchtime=2s)
#   BENCHTIME=10x scripts/bench.sh         # fast smoke run (CI default)
#   COUNT=6 scripts/bench.sh ./internal/auth/   # 6 samples, one package
#
# Environment overrides:
#   BENCH_TAGS  build tags (default: the full opt-in set, kept in sync with CI)
#   BENCHTIME   -benchtime value (default: 2s)
#   COUNT       -count value     (default: 1)
#
set -euo pipefail

# Keep in sync with .github/workflows/ci.yml `env.FULL_TAGS`.
DEFAULT_TAGS="brotli zstd acme console otel grpc http3 importer wasmplugins stream consul kubernetes"

TAGS="${BENCH_TAGS:-$DEFAULT_TAGS}"
BENCHTIME="${BENCHTIME:-2s}"
COUNT="${COUNT:-1}"

PKGS=("$@")
if [ "${#PKGS[@]}" -eq 0 ]; then
	PKGS=("./...")
fi

echo "== perf-gate harness"
echo "   tags=${TAGS}"
echo "   benchtime=${BENCHTIME} count=${COUNT}"
echo "   packages=${PKGS[*]}"
echo

# -run '^$' disables unit tests so only Benchmark* runs; packages without any
# benchmark simply report "no benchmarks" and exit 0.
exec go test \
	-tags "${TAGS}" \
	-run '^$' \
	-bench . \
	-benchmem \
	-benchtime="${BENCHTIME}" \
	-count="${COUNT}" \
	"${PKGS[@]}"
