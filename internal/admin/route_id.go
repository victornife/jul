// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"crypto/rand"
	"encoding/base32"
	"fmt"
	"io"
)

// routeIDAlphabet is a lowercase RFC 4648 base32 alphabet, chosen because
// every character it can produce is already a legal route_id byte
// (internal/config's route_id grammar: [a-z0-9_-]). No further mapping or
// stripping is needed between minting and validation.
const routeIDAlphabet = "abcdefghijklmnopqrstuvwxyz234567"

var routeIDEncoding = base32.NewEncoding(routeIDAlphabet).WithPadding(base32.NoPadding)

// routeIDRandReader is the entropy source mintRouteID reads from. It is a
// package variable, swapped only in tests, so a CSPRNG failure can be
// exercised deterministically without touching the real crypto/rand reader.
var routeIDRandReader io.Reader = rand.Reader

// mintRouteID generates a fresh durable route identity: "r-" followed by 26
// lowercase base32 characters decoded from 128 CSPRNG bits. It is called
// only from location_add, and only when the request omits route_id — no
// other code path mints one (ADR 0019 §4: minting is create-only).
//
// There is no weaker fallback when crypto/rand fails: the accepted contract
// is 128 bits read from the CSPRNG, not "128 bits, or something derived from
// the pid and clock if that is unavailable." A route creation that cannot
// get real entropy fails instead of minting a weaker identifier.
func mintRouteID() (string, error) {
	var raw [16]byte
	if _, err := io.ReadFull(routeIDRandReader, raw[:]); err != nil {
		return "", fmt.Errorf("mint route_id: crypto/rand unavailable: %w", err)
	}
	return "r-" + routeIDEncoding.EncodeToString(raw[:]), nil
}
