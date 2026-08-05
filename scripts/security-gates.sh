#!/usr/bin/env bash
# Dedicated fail-closed security-package gates for issue #129.
set -euo pipefail

FULL_TAGS="${FULL_TAGS:-brotli zstd acme console otel grpc http3 importer wasmplugins stream consul kubernetes waf}"
cover="$(mktemp "${TMPDIR:-/tmp}/jul-security-cover.XXXXXX")"
trap 'rm -f "$cover"' EXIT

printf '%s\n' '== security negative tests: lean'
go test -count=1 ./internal/rbac ./internal/waf ./internal/plugins

printf '%s\n' '== security negative tests: full tags'
go test -count=1 -tags "$FULL_TAGS" ./internal/rbac ./internal/waf ./internal/plugins

printf '%s\n' '== security package coverage: full tags'
go test -count=1 -covermode=atomic -coverprofile="$cover" \
  -tags "$FULL_TAGS" ./internal/rbac ./internal/waf ./internal/plugins

python3 scripts/check-package-coverage.py \
  --profile scripts/security-package-coverage.json \
  --coverprofile "$cover"
python3 scripts/test_check_package_coverage.py
