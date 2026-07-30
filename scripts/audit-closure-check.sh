#!/usr/bin/env bash
#
# Audit-closure certification gate.
#
# Runs, on the current tree, the vertical set of checks the configuration-audit
# closure report (docs/audit/2026-07-25-configuration-audit-closure.md) requires
# before a finding may move from "remediated" to formally "Closed". This is the
# LOCAL MIRROR of .github/workflows/ci.yml: a green run here is necessary but not
# sufficient. Only the exact-SHA CI run (all ci.yml jobs, including the -race and
# multi-OS matrix) plus two independent human sign-offs makes closure final. A
# passing pre-commit hook or a passing local run of this script is NOT CI.
#
# The opt-in build-tag set is kept in sync with the Makefile FULL_TAGS and the
# ci.yml env.FULL_TAGS. Override for a narrower local run:
#
#   scripts/audit-closure-check.sh
#   FULL_TAGS="console" scripts/audit-closure-check.sh
#
# Note: the race detector needs CGO and a C toolchain. On a host without one,
# `go test -race` fails with "-race requires cgo"; run that lane in CI instead.
set -euo pipefail

# Keep in sync with Makefile FULL_TAGS and .github/workflows/ci.yml env.FULL_TAGS.
FULL_TAGS="${FULL_TAGS:-brotli zstd acme console otel grpc http3 importer wasmplugins stream consul kubernetes waf}"
CONSOLE_UI="internal/admin/ui"

run() {
  echo "== $* =="
  "$@"
}

# --- Go: format, vet, test, race -------------------------------------------
# gofmt must be clean. The //go:build ignore descriptor generator is
# intentionally not gofmt-clean and is excluded, mirroring the ci.yml `format` job.
unformatted="$(gofmt -l . | grep -v '^examples/grpc-gateway/gen_descriptor.go$' || true)"
if [ -n "$unformatted" ]; then
  echo "ERROR: gofmt found unformatted files:"
  echo "$unformatted"
  exit 1
fi

run go vet -tags "$FULL_TAGS" ./...
run go test -tags "$FULL_TAGS" ./...
# -p 2 caps parallelism because the race detector multiplies memory use, matching
# the ci.yml `race` job.
run go test -race -p 2 -tags "$FULL_TAGS" \
  ./internal/admin/... ./internal/app/... ./internal/server/... ./internal/config/...

# --- Console v2 -------------------------------------------------------------
run pnpm --dir "$CONSOLE_UI" install --frozen-lockfile
run pnpm --dir "$CONSOLE_UI" run typecheck
run pnpm --dir "$CONSOLE_UI" run lint
run pnpm --dir "$CONSOLE_UI" run test:coverage
run pnpm --dir "$CONSOLE_UI" run build

# --- Docs -------------------------------------------------------------------
run python scripts/docs-check.py
run python scripts/test_docs_check.py

echo
echo "audit-closure-check: all local certification gates passed."
echo "This is NOT closure: formal closure requires the exact-SHA CI run and two"
echo "independent human sign-offs recorded in the closure report."
