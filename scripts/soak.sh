#!/usr/bin/env bash
#
# Soak harness — post-GA stability gate (ADR 0005).
#
# Runs the in-tree TestSoak, which drives sustained concurrent traffic through a
# real reverse-proxy handler and asserts the process stays healthy: every request
# succeeds, the goroutine count returns to a steady state, and the heap does not
# grow without bound (a leak gate). TestSoak lives behind the `soak` build tag so
# it never runs in the normal `go test ./...`; this script adds that tag.
#
# Two ways it is used:
#   * CI (`soak (smoke)` job) runs it as a smoke gate with a short SOAK_DURATION:
#     the harness must keep compiling and the proxy must survive a brief burst
#     without errors or a leak. This keeps the gate from rotting between releases.
#   * The release workflow (`.github/workflows/release.yml`, on a version tag)
#     runs it for several minutes as the actual soak gate. A red run blocks the
#     release job — the "block tag on red" gate ADR 0005 calls for.
#
# Usage:
#   scripts/soak.sh                          # 30s soak (TestSoak default)
#   SOAK_DURATION=5m SOAK_WORKERS=32 scripts/soak.sh   # release-style run
#
# Environment overrides:
#   SOAK_TAGS      build tags (default: the full opt-in set, kept in sync with CI)
#   SOAK_DURATION  wall-clock run time   (passed through to TestSoak; default 30s)
#   SOAK_WORKERS   concurrent clients    (passed through to TestSoak; default 16)
#
set -euo pipefail

# Keep in sync with .github/workflows/ci.yml `env.FULL_TAGS` (minus waf, which the
# proxy soak does not exercise). The `soak` tag activates TestSoak.
DEFAULT_TAGS="brotli zstd acme acme_dns console otel grpc http3 importer wasmplugins stream consul kubernetes"

TAGS="soak ${SOAK_TAGS:-$DEFAULT_TAGS}"
DURATION="${SOAK_DURATION:-30s}"
WORKERS="${SOAK_WORKERS:-16}"

# Give the run a generous timeout: the soak duration plus headroom for build,
# warm-up, and shutdown. A fixed pad keeps short smoke runs snappy.
echo "== soak gate (ADR 0005)"
echo "   tags=${TAGS}"
echo "   duration=${DURATION} workers=${WORKERS}"
echo

# -run pins TestSoak; -count=1 defeats the test cache so the soak always runs;
# -timeout 0 disables Go's test timeout so a long release soak is not killed.
exec env SOAK_DURATION="${DURATION}" SOAK_WORKERS="${WORKERS}" \
	go test \
	-tags "${TAGS}" \
	-run '^TestSoak$' \
	-count=1 \
	-timeout 0 \
	-v \
	./internal/handler/
