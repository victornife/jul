#!/bin/sh
# Install the repo-managed Git hooks for local CI gate parity (SEQ-08 / HP-04).
#
# One command; safe to re-run. It points Git at the version-controlled .githooks
# directory instead of copying files, so the hooks stay in sync automatically.
#
# Uninstall with:  git config --unset core.hooksPath
set -eu

root=$(git rev-parse --show-toplevel)
cd "$root"

git config core.hooksPath .githooks
chmod +x .githooks/pre-commit .githooks/pre-push 2>/dev/null || true

echo "Installed Git hooks: core.hooksPath -> .githooks"
echo "  pre-commit : gofmt on staged Go files"
echo "  pre-push   : gofmt + go vet/build/test (lean) [+ golangci-lint / frontend if available]"
echo "Bypass once with --no-verify; disable all with JUL_SKIP_HOOKS=1; uninstall with 'git config --unset core.hooksPath'."
