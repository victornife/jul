#!/usr/bin/env bash
# Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
# SPDX-License-Identifier: agpl
set -euo pipefail
if [[ $# -lt 1 || $# -gt 2 ]]; then
  echo 'usage: inspect.sh CONFIGURED_HOST [CURSOR]' >&2
  exit 2
fi
inspection_args=(cache inspect --endpoint "${JUL_ADMIN_ENDPOINT:?set JUL_ADMIN_ENDPOINT}" --token-file "${JUL_ADMIN_TOKEN_FILE:?set JUL_ADMIN_TOKEN_FILE}" --host "$1" --page-size 50 --json)
if [[ $# -eq 2 ]]; then inspection_args+=(--cursor "$2"); fi
exec "${JUL_BIN:-jul}" "${inspection_args[@]}"
