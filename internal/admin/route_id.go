// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"fmt"
	"os"
	"time"
)

// routeIDAlphabet is a lowercase RFC 4648 base32 alphabet, chosen because
// every character it can produce is already a legal route_id byte
// (internal/config's route_id grammar: [a-z0-9_-]). No further mapping or
// stripping is needed between minting and validation.
const routeIDAlphabet = "abcdefghijklmnopqrstuvwxyz234567"

var routeIDEncoding = base32.NewEncoding(routeIDAlphabet).WithPadding(base32.NoPadding)

// mintRouteID generates a fresh durable route identity: "r-" followed by 26
// lowercase base32 characters decoded from 16 CSPRNG bytes. It is called
// only from location_add, and only when the request omits route_id — no
// other code path mints one (ADR 0019 §4: minting is create-only).
func mintRouteID() string {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err == nil {
		return "r-" + routeIDEncoding.EncodeToString(raw[:])
	}

	// Extremely defensive fallback, mirroring
	// internal/app/config_apply.go's newManagedApplyInstanceID: the OS CSPRNG
	// failing is not expected on any real deployment target, but a route_id
	// must still come out well-formed if it somehow does.
	fallback := fmt.Sprintf("%d-%d", os.Getpid(), time.Now().UTC().UnixNano())
	sum := sha256.Sum256([]byte(fallback))
	return "r-" + routeIDEncoding.EncodeToString(sum[:16])
}
