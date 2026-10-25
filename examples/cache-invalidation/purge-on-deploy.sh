#!/usr/bin/env bash
# Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
# SPDX-License-Identifier: agpl
#
# purge-on-deploy.sh — Authenticated scoped cache invalidation helper (#621).
#
# Executes the server-owned preview -> confirm -> execute workflow through
# `jul cache purge` (or `/api/v1/cache/purge*`) using a least-privilege
# `cache:purge` bearer token read from a restricted file (never passed on the
# command line or stored in browser storage).
#
# Usage examples:
#   # Invalidate by trusted origin tag after publishing article 42:
#   JUL_BIN=./jul ./examples/cache-invalidation/purge-on-deploy.sh \
#     --endpoint http://127.0.0.1:9090 \
#     --token-file /run/secrets/jul-cache-purge.token \
#     --host portal.example.com \
#     --tag article:42 \
#     --idempotency-key publish-article-42-rev-7
#
#   # Invalidate a path prefix without origin tag changes:
#   JUL_BIN=./jul ./examples/cache-invalidation/purge-on-deploy.sh \
#     --endpoint http://127.0.0.1:9090 \
#     --token-file /run/secrets/jul-cache-purge.token \
#     --host portal.example.com \
#     --prefix /articles/ \
#     --idempotency-key deploy-portal-20261025

set -euo pipefail

JUL_BIN="${JUL_BIN:-jul}"
ENDPOINT="${JUL_ENDPOINT:-}"
TOKEN_FILE="${JUL_TOKEN_FILE:-}"
CA_FILE=""
HOST=""
URL_TARGET=""
PREFIX=""
GLOB=""
TAG=""
TAG_MATCH="any"
IDEMPOTENCY_KEY=""
ALLOW_BROAD=0

usage() {
	cat >&2 <<'EOF'
Usage: purge-on-deploy.sh --endpoint <url> --token-file <path> [selector] [options]

Selectors (choose one):
  --url <http(s)://host/path[?q]>     Exact public URL invalidation
  --host <host> --prefix </path/>     Host-bound segment-prefix invalidation
  --host <host> --glob </path/*/x>    Host-bound anchored glob invalidation
  --host <host> --tag <tag[,tag2]>    Host-bound origin grouping-tag invalidation
  --host <host> --allow-broad         Whole-hostname invalidation (requires --allow-broad)

Options:
  --tag-match <any|all>               Multi-tag match mode (default: any)
  --idempotency-key <key>             8-128 char [A-Za-z0-9_-] key for safe retries
  --ca-file <path>                    Additional PEM CA bundle for HTTPS endpoint
  --allow-broad                       Permit broad-scope invalidation after preview
EOF
}

while [ $# -gt 0 ]; do
	case "$1" in
	--endpoint)
		ENDPOINT="${2:-}"
		shift 2
		;;
	--token-file)
		TOKEN_FILE="${2:-}"
		shift 2
		;;
	--ca-file)
		CA_FILE="${2:-}"
		shift 2
		;;
	--host)
		HOST="${2:-}"
		shift 2
		;;
	--url)
		URL_TARGET="${2:-}"
		shift 2
		;;
	--prefix)
		PREFIX="${2:-}"
		shift 2
		;;
	--glob)
		GLOB="${2:-}"
		shift 2
		;;
	--tag)
		if [ -n "$TAG" ]; then
			TAG="${TAG},${2:-}"
		else
			TAG="${2:-}"
		fi
		shift 2
		;;
	--tag-match)
		TAG_MATCH="${2:-}"
		shift 2
		;;
	--idempotency-key)
		IDEMPOTENCY_KEY="${2:-}"
		shift 2
		;;
	--allow-broad)
		ALLOW_BROAD=1
		shift 1
		;;
	-h|--help)
		usage
		exit 0
		;;
	*)
		echo "error: unknown argument: $1" >&2
		usage
		exit 2
		;;
	esac
done

if [ -z "$ENDPOINT" ]; then
	echo "error: --endpoint (or JUL_ENDPOINT) is required" >&2
	exit 2
fi
if [ -z "$TOKEN_FILE" ]; then
	echo "error: --token-file (or JUL_TOKEN_FILE) is required" >&2
	exit 2
fi
if [ "$TOKEN_FILE" != "-" ] && [ ! -r "$TOKEN_FILE" ]; then
	echo "error: token file is not readable: $TOKEN_FILE" >&2
	exit 2
fi

COMMON_ARGS=(--endpoint "$ENDPOINT" --token-file "$TOKEN_FILE" --json)
if [ -n "$CA_FILE" ]; then
	COMMON_ARGS+=(--ca-file "$CA_FILE")
fi

SELECTOR_ARGS=()
if [ -n "$URL_TARGET" ]; then
	SELECTOR_ARGS+=(--url "$URL_TARGET")
fi
if [ -n "$HOST" ]; then
	SELECTOR_ARGS+=(--host "$HOST")
fi
if [ -n "$PREFIX" ]; then
	SELECTOR_ARGS+=(--prefix "$PREFIX")
fi
if [ -n "$GLOB" ]; then
	SELECTOR_ARGS+=(--glob "$GLOB")
fi
if [ -n "$TAG" ]; then
	SELECTOR_ARGS+=(--tag "$TAG" --tag-match "$TAG_MATCH")
fi

if [ "${#SELECTOR_ARGS[@]}" -eq 0 ]; then
	echo "error: a selector (--url, or --host with --prefix/--glob/--tag) is required" >&2
	exit 2
fi

# Step 1: Non-destructive preview to verify scope and obtain a bound preview_token.
PREVIEW_JSON="$("$JUL_BIN" cache purge "${COMMON_ARGS[@]}" "${SELECTOR_ARGS[@]}" --preview)"

parsed_preview="$(python3 -c '
import json, sys
doc = json.loads(sys.stdin.read())
if not doc.get("ok"):
    sys.exit(2)
token = doc.get("preview_token", "")
broad = bool(doc.get("broad_scope", False))
reps = int(doc.get("matched_representations", 0))
res = int(doc.get("matched_resources", 0))
acc = doc.get("count_accuracy", "exact")
print(f"{token}\t{1 if broad else 0}\t{res}\t{reps}\t{acc}")
' <<<"$PREVIEW_JSON")"

IFS=$'\t' read -r PREVIEW_TOKEN IS_BROAD MATCHED_RES MATCHED_REPS ACCURACY <<<"$parsed_preview"

if [ -z "$PREVIEW_TOKEN" ]; then
	echo "error: preview did not return a preview_token" >&2
	exit 9
fi

if [ "$IS_BROAD" = "1" ] && [ "$ALLOW_BROAD" != "1" ]; then
	echo "error: selector is flagged as broad_scope (matched_resources=${MATCHED_RES}, accuracy=${ACCURACY}); pass --allow-broad to confirm" >&2
	exit 5
fi

EXEC_ARGS=("${COMMON_ARGS[@]}" "${SELECTOR_ARGS[@]}" --preview-token "$PREVIEW_TOKEN")
if [ "$ALLOW_BROAD" = "1" ]; then
	EXEC_ARGS+=(--confirm-broad)
fi
if [ -n "$IDEMPOTENCY_KEY" ]; then
	EXEC_ARGS+=(--idempotency-key "$IDEMPOTENCY_KEY")
fi

# Step 2: Execute scoped invalidation bound to the preview_token (and optional idempotency key).
set +e
EXEC_JSON="$("$JUL_BIN" cache purge "${EXEC_ARGS[@]}")"
EXEC_CODE=$?
set -e

printf '%s\n' "$EXEC_JSON"
exit "$EXEC_CODE"
