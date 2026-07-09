# Install the repo-managed Git hooks for local CI gate parity (SEQ-08 / HP-04).
#
# One command; safe to re-run. Points Git at the version-controlled .githooks
# directory instead of copying files, so the hooks stay in sync automatically.
# The hook scripts run under Git for Windows' bundled `sh`, so no extra tooling
# is required.
#
# Uninstall with:  git config --unset core.hooksPath

$ErrorActionPreference = 'Stop'

$root = (git rev-parse --show-toplevel).Trim()
Set-Location $root

git config core.hooksPath .githooks

Write-Host 'Installed Git hooks: core.hooksPath -> .githooks'
Write-Host '  pre-commit : gofmt on staged Go files'
Write-Host '  pre-push   : gofmt + go vet/build/test (lean) [+ golangci-lint / frontend if available]'
Write-Host "Bypass once with --no-verify; disable all with JUL_SKIP_HOOKS=1; uninstall with 'git config --unset core.hooksPath'."
