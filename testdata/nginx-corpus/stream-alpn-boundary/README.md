# stream-alpn-boundary

Repository-authored, sanitized #434 fixture. AGPL-3.0-only. No external source
configuration, credentials, keys or production endpoints are included.

The source uses two ordered, case-sensitive token-boundary regular expressions
over `$ssl_preread_alpn_protocols`. The importer keeps this map and its dynamic
target blocking: NGINX joins opaque ALPN identifiers with commas; a single
identifier `x,h2` therefore matches its h2 expression. Jul's native exact rule
must not match it. New code `NGX_STREAM_ALPN_MAP_AMBIGUOUS` carries provenance and
manual-redesign guidance, and claims no target mapping or ready candidate.
Broader maps, variables, word-boundary regexes, TLS termination and QUIC/UDP
assumptions remain blocking. No automatic ALPN source form is claimed equivalent.

`nginx/reference.conf` is a separate pinned-runtime fixture using NGINX stream
return backends A/B/F. `scripts/nginx-migration-e2e.sh` supplies its existing image
digest to `scripts/nginx-alpn-e2e.sh`, which isolates the container on an internal
network and publishes only ephemeral loopback ports. The Go reference test
checks conventional offers, order, absence, case and exact boundaries, and
requires the comma-containing identifier to differ (NGINX A, Jul F). Manual
native Jul rules are test setup, not importer output or a migration-equivalence
claim. Missing Docker fails the required lane, rather than skipping it as passed.
