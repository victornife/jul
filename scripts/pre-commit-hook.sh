#!/bin/sh
# Pre-commit hook for Jul.IA — install with:
#   cp scripts/pre-commit-hook.sh .git/hooks/pre-commit
#
# Blocks commits if:
#   - go test ./... fails
#   - scripts/docs-check.py finds placeholders/broken links (when python is available)
#   - gofmt would reformat any file
#
# Cross-platform: detects python / python3 / py, and performs a runtime smoke-test
# to skip Windows Store "python" stubs that block execution.

PY=""
for candidate in python python3 py; do
    if command -v "$candidate" >/dev/null 2>&1 && "$candidate" -c "import sys; sys.exit(0)" >/dev/null 2>&1; then
        PY="$candidate"
        break
    fi
done

if [ -z "$PY" ]; then
    echo "[pre-commit] Python not found; skipping docs-check"
fi

set -e

echo "[pre-commit] Running go test ./..."
go test ./... >/dev/null || {
    echo "[pre-commit] FAILED: go test ./..."
    exit 1
}
echo "[pre-commit] go test passed"

if [ -n "$PY" ]; then
    echo "[pre-commit] Running docs-check.py"
    "$PY" scripts/docs-check.py || {
        echo "[pre-commit] FAILED: docs-check.py"
        exit 1
    }
    echo "[pre-commit] docs-check passed"
fi

echo "[pre-commit] Running format check"
gofmt -l . | grep -q . && {
    echo "[pre-commit] FAILED: gofmt needed. Run 'make format'."
    exit 1
}

# Check for DCO sign-off in the commit message
if ! grep -q "Signed-off-by:" "$(git rev-parse --git-dir)/COMMIT_EDITMSG" 2>/dev/null; then
    echo "[pre-commit] FAILED: Missing Signed-off-by line."
    echo "[pre-commit]          Use 'git commit -s' to add it automatically."
    exit 1
fi

echo "[pre-commit] All checks passed."
