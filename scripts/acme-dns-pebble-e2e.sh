#!/bin/sh
# Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
# SPDX-License-Identifier: agpl

set -eu

JUL_PEBBLE_E2E=1 go test -tags 'acme http3' ./internal/server -run '^TestDNS01PebbleEndToEnd$' -count=1 -timeout 3m
