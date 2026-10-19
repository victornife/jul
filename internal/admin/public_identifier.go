// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"strings"

	"jul/internal/versionid"
)

// publicTokenID never changes authentication/operation ownership keys. A public
// token fingerprint is itself a dictionary verifier, even if truncated. Protect
// it (including OIDC fingerprints) before identity, audit or durable audit output.
func (s *Server) publicTokenID(private string) string {
	if private == "" || private == "(legacy)" {
		return private
	}
	if strings.HasPrefix(private, "cid1:") {
		if _, ok := versionid.Parse("ev1:" + strings.TrimPrefix(private, "cid1:")); ok {
			return private // already-protected retained event, possibly another context
		}
	}
	return "cid1:" + strings.TrimPrefix(s.deps.EffectiveVersions.Digest("public-credential-id", []byte(private)), "ev1:")
}

func (s *Server) protectAuditIDs(events []AuditEvent) {
	for i := range events {
		events[i].TokenID = s.publicTokenID(events[i].TokenID)
	}
}
