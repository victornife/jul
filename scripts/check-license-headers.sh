#!/usr/bin/env bash
# Check that every source file has the AGPL-3.0-or-later SPDX license header.
# Intended for CI (make license-check).
set -euo pipefail

if ! command -v addlicense &>/dev/null; then
  echo "addlicense not found; install with:"
  echo "  go install github.com/google/addlicense@latest"
  exit 1
fi

addlicense -check -c "Victor Niharra <vniharrafe@gmail.com>" -l agpl -s \
  -ignore "**/node_modules/**" \
  -ignore "**/.git/**" \
  -ignore "**/assets/dist/**" \
  -ignore "**/pnpm-lock.yaml" \
  -ignore "**/*.log" \
  -ignore "**/*.exe" \
  -ignore "**/go.mod" \
  -ignore "**/go.sum" \
  -ignore "**/tmp/**" \
  -ignore "**/jul-data/**" \
  cmd/ internal/ examples/
