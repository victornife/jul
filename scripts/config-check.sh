#!/usr/bin/env bash
# Config-example validation gate (JUL-AUD-002).
#
# Every root-level and examples/ TOML config Jul ships is a documented,
# copy-pasteable artifact — and, for the burn-in-*.toml profiles, the substrate
# the final soak runs against. Nothing previously loaded any of them in CI, so
# a schema change could silently break one (this is exactly how burn-in-full.toml
# stopped parsing: see docs/audit/2026-09-16-pre-soak-readiness-audit.md
# JUL-AUD-001). This script runs `jul check -skip-static-roots` over all of
# them: bundled sample content directories are deployment inputs, not part of
# the repository checkout. An operator's default `jul check` opens static roots.
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
	if [ -n "${LEAN_CHECK_BIN:-}" ]; then
		rm -f "$LEAN_CHECK_BIN"
	fi
	if [ -n "${f1_dir:-}" ]; then
		rm -rf "$f1_dir"
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
	if ! ( cd "$workdir" && "$BIN" check -config "$(basename "$cfg")" -quiet -skip-static-roots ); then
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
	examples/plugins/signed-url/jul.toml)
		# Opaque guest parameters are runtime-checked; build the shipped source
		# and validate the sample with the same CWD semantics as its recipe.
		guest_dir="$(mktemp -d "${TMPDIR:-/tmp}/jul-signed-url-check.XXXXXX")"
		if (cd examples/plugins && GOOS=wasip1 GOARCH=wasm go build -trimpath -buildvcs=false -buildmode=c-shared -o "$guest_dir/signed-url.wasm" ./signed-url); then
			cp "$f" "$guest_dir/jul.toml"
			SIGNED_URL_KEY=YWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWE check jul.toml "$guest_dir"
		else
			fail=1
		fi
		rm -rf "$guest_dir"
		continue
		;;
	examples/cache-invalidation/jul.toml)
		echo "-- examples/cache-invalidation/jul.toml"
		if ! (
			cd examples/cache-invalidation &&
			JUL_ADMIN_TOKEN=placeholder-admin-token-for-config-check \
			JUL_CACHE_PURGE_TOKEN=placeholder-purge-token-for-config-check \
			"$BIN" check -config jul.toml -quiet -skip-static-roots
		); then
			echo "   FAILED: examples/cache-invalidation/jul.toml" >&2
			fail=1
		fi
		continue
		;;
	examples/sharing/f1-standalone/*.toml)
		# Fixture F1 (#581) configs reference generated ephemeral TLS/mTLS chains,
		# CRLs, bcrypt htpasswd, and the static portal root. Materialize them once
		# into a temporary directory and run both full-check (including static
		# roots, TLS/CRL, and htpasswd) and jul lint, plus verify lean-build WAF
		# rejection before persistence.
		if [ -z "${f1_dir:-}" ]; then
			f1_dir="$(mktemp -d "${TMPDIR:-/tmp}/jul-f1-config-check.XXXXXX")"
			cp -r examples/sharing/f1-standalone/*.toml examples/sharing/f1-standalone/*.py "$f1_dir/"
			python3 "$f1_dir/generate_lab_assets.py" --out "$f1_dir" --deterministic >/dev/null
		fi
		cfg_base="$(basename "$f")"
		echo "-- examples/sharing/f1-standalone/${cfg_base} (with static roots + TLS + lint)"
		if ! (
			cd "$f1_dir" &&
			JUL_ADMIN_TOKEN=placeholder-token-for-config-check \
			JUL_F1_AUDITOR_TOKEN=placeholder-auditor-token-for-config-check \
			JUL_F1_VIEWER_TOKEN=placeholder-viewer-token-for-config-check \
			JUL_F1_OPERATOR_TOKEN=placeholder-operator-token-for-config-check \
			"$BIN" check -config "$cfg_base" -quiet &&
			JUL_ADMIN_TOKEN=placeholder-token-for-config-check \
			JUL_F1_AUDITOR_TOKEN=placeholder-auditor-token-for-config-check \
			JUL_F1_VIEWER_TOKEN=placeholder-viewer-token-for-config-check \
			JUL_F1_OPERATOR_TOKEN=placeholder-operator-token-for-config-check \
			"$BIN" lint -config "$cfg_base" >/dev/null
		); then
			echo "   FAILED: examples/sharing/f1-standalone/${cfg_base}" >&2
			fail=1
		fi
		continue
		;;
	*/rust-proxy/Cargo.toml | */rust-proxy/.cargo/config.toml)
		continue
		;;
	esac
	check "$(basename "$f")" "$(dirname "$f")"
done < <(find examples -name '*.toml' -print0 | sort -z)

if [ -n "${f1_dir:-}" ]; then
	echo "-- examples/sharing/f1-standalone/jul.toml (lean build must reject WAF with specific diagnostic)"
	LEAN_CHECK_BIN="$(mktemp "${TMPDIR:-/tmp}/jul-lean-config-check.XXXXXX")"
	LEAN_ERR_FILE="$(mktemp "${TMPDIR:-/tmp}/jul-lean-config-err.XXXXXX")"
	go build -o "$LEAN_CHECK_BIN" ./cmd/jul
	if (
		cd "$f1_dir" &&
		JUL_ADMIN_TOKEN=placeholder-token-for-config-check \
		JUL_F1_AUDITOR_TOKEN=placeholder-auditor-token-for-config-check \
		JUL_F1_VIEWER_TOKEN=placeholder-viewer-token-for-config-check \
		JUL_F1_OPERATOR_TOKEN=placeholder-operator-token-for-config-check \
		"$LEAN_CHECK_BIN" check -config jul.toml -quiet >"$LEAN_ERR_FILE" 2>&1
	); then
		echo "   FAILED: lean build unexpectedly accepted WAF-enabled examples/sharing/f1-standalone/jul.toml" >&2
		fail=1
	elif ! grep -qi "waf" "$LEAN_ERR_FILE" || ! grep -qiE "built without waf support|rebuild with -tags waf|build tag|not compiled|unsupported|disabled" "$LEAN_ERR_FILE"; then
		echo "   FAILED: lean build failed without expected WAF unsupported-capability diagnostic:" >&2
		cat "$LEAN_ERR_FILE" >&2
		fail=1
	fi
	rm -f "$LEAN_CHECK_BIN" "$LEAN_ERR_FILE"
fi

if [ "$fail" -ne 0 ]; then
	echo "config-check: one or more shipped configs failed to load" >&2
	exit 1
fi
echo "config-check: all shipped configs load cleanly"
