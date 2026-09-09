// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package main

import "fmt"

// remoteExtendedUsage appends the durable AUTO-04 surface without changing the
// established local-command help or legacy flag behavior.
func remoteExtendedUsage() {
	extendedUsage()
	fmt.Fprint(stderr, `
Remote automation (stable /api/v1):
  jul plan --endpoint u --config candidate.toml [--base-version v] [--json]
  jul diff --endpoint u (--config candidate.toml | --history-id id) [--json]
  jul apply --endpoint u --config candidate.toml --base-version v [--idempotency-key k] [--json]
  jul stage --endpoint u --config candidate.toml --base-version v [--idempotency-key k] [--json]
  jul status --endpoint u [--apply-id id] [--json]
  jul rollback --endpoint u --history-id id [--base-version v] [--idempotency-key k] [--json]
  jul export --endpoint u [--json]
  jul diagnostics --endpoint u [--json]
  jul apply --endpoint u --adopt-external [--base-version v] [--adopt-mode hot|stage_restart]

Remote connection flags:
  --endpoint u       explicit Jul admin endpoint; otherwise profile/JUL_ENDPOINT
  --profile name     named profile from the Jul CLI profile file
  --token-file path  bearer-token file; '-' reads the token from stdin
  --ca-file path     additional CA bundle
  --client-cert path mTLS client certificate (requires --client-key)
  --client-key path  mTLS client private key
  --timeout d        ordinary request timeout
  --json             exactly one JSON object on stdout
  --verbose          bounded operation/request diagnostics; never credentials or bodies

Remote credential precedence:
  explicit flags/profile selection > named profile > JUL_ENDPOINT/JUL_TOKEN_FILE/JUL_TOKEN > no endpoint

Remote automation exit contract:
  0  successful live/read/ordinary operation
  1  validation/configuration failure
  2  usage/request error
  3  successful operation requiring restart/convergence
  4  successful degraded outcome
  5  conflict or uncertain state after failed restoration
  6  configuration-authority denial
  7  authentication/authorization/insecure-transport refusal
  8  connectivity/TLS/rate-limit failure
  9  server/capability/storage/operation-timeout/internal failure

Security:
  No raw --token argument or generic --insecure bypass exists. Plain HTTP is
  accepted only for loopback. Control-plane redirects are not followed.
`)
}
