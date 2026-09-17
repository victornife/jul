#!/usr/bin/env bash
# Config-example validation gate (JUL-AUD-002).
#
# Every root-level and examples/ TOML config Jul ships is a documented,
# copy-pasteable artifact — and, for the burn-in-*.toml profiles, the substrate
# the final soak runs against. Nothing previously loaded any of them in CI, so
# a schema change could silently break one (this is exactly how burn-in-full.toml
# stopped parsing: see docs/audit/2026-09-16-pre-soak-readiness-audit.md
# JUL-AUD-001). This script runs `jul check` over all of them.
#
# Scope:
#   - every *.toml at the repository root (burn-in-*.toml, server*.toml,
#     dev-server.toml)
#   - every examples/**/jul.toml and examples/migrate/imported.toml
#
# Deliberately out of scope:
#   - examples/rust-proxy/Cargo.toml and examples/rust-proxy/.cargo/config.toml
#     are Rust build files, not Jul configs — they merely share the extension.
#   - testdata/*.toml are per-feature fixtures owned and already exercised by
#     their respective Go test suites (some may gain deliberately-invalid
#     siblings for negative tests); re-validating them here would duplicate
#     that ownership rather than add coverage.
#
# Env-dependent profiles are handled by exporting placeholder values for the
# secret references they declare, not by skipping them — a config that no
# longer parses once the placeholder resolves is exactly the defect this gate
# exists to catch.
#
# Usage:
#   scripts/config-check.sh              # builds ./jul-config-check with FULL_TAGS
#   JUL_BIN=./jul scripts/config-check.sh # reuse an already-built full-tag binary
set -euo pipefail

cd "$(git rev-parse --show-toplevel)"

FULL_TAGS="${FULL_TAGS:-brotli zstd acme console otel grpc http3 importer wasmplugins stream consul kubernetes waf}"

BIN="${JUL_BIN:-}"
CLEANUP_BIN=0
if [ -z "$BIN" ]; then
	BIN="$(mktemp "${TMPDIR:-/tmp}/jul-config-check.XXXXXX")"
	CLEANUP_BIN=1
	echo "== building full-tag binary for config-check"
	go build -tags "$FULL_TAGS" -o "$BIN" ./cmd/jul
fi
cleanup() {
	if [ "$CLEANUP_BIN" = 1 ]; then
		rm -f "$BIN"
	fi
}
trap cleanup EXIT

fail=0

# check RELATIVE_CONFIG_PATH [WORKDIR]
# WORKDIR defaults to the repo root; some examples resolve relative paths
# (e.g. a descriptor set) against the process CWD rather than the config
# file's own directory (see docs/configuration.md).
check() {
	local cfg="$1" workdir="${2:-.}"
	echo "-- ${workdir}/${cfg}"
	if ! ( cd "$workdir" && "$BIN" check -config "$(basename "$cfg")" -quiet ); then
		echo "   FAILED: ${workdir}/${cfg}" >&2
		fail=1
	fi
}

echo "== root configs"
for f in *.toml; do
	case "$f" in
	burn-in-phase2a.toml)
		JUL_ADMIN_TOKEN=placeholder-token-for-config-check check "$f"
		;;
	server.everything.toml)
		JUL_ADMIN_TOKEN=placeholder-token-for-config-check \
			JUL_ALICE_TOKEN=placeholder-token-for-config-check \
			JUL_CI_TOKEN=placeholder-token-for-config-check \
			JUL_CONSUL_TOKEN=placeholder-token-for-config-check \
			JUL_K8S_TOKEN=placeholder-token-for-config-check \
			check "$f"
		;;
	*)
		check "$f"
		;;
	esac
done

echo "== examples/"
while IFS= read -r -d '' f; do
	case "$f" in
	*/rust-proxy/Cargo.toml | */rust-proxy/.cargo/config.toml)
		continue
		;;
	esac
	check "$(basename "$f")" "$(dirname "$f")"
done < <(find examples -name '*.toml' -print0 | sort -z)

if [ "$fail" -ne 0 ]; then
	echo "config-check: one or more shipped configs failed to load" >&2
	exit 1
fi
echo "config-check: all shipped configs load cleanly"
