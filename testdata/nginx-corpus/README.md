# NGINX migration corpus

This directory contains repository-authored, sanitized fixtures for issues
#154, #365, and #426. Every fixture is self-contained and declares its origin,
license, expected assessment results, selected candidate assertions, and safe
replay scenarios in `manifest.json`.

The #154 closed baseline contains 11 fixtures: 9 core and 2 full. #426 added 6
more bounded NGINX stream / HTTP PROXY-protocol identity fixtures (all core):
`stream-tcp-basic`, `stream-named-upstream`, `stream-udp`, `stream-sni-bounded`,
`stream-security-boundaries`, and `realip-proxy-protocol-supported`. #365 added
4 more real NGINX-vs-Jul migration E2E fixtures (all core):
`routing-precedence-runtime` (exact/prefix/regex/root location precedence),
`upstream-weighted-runtime` (deterministic weighted backend selection),
`compression-runtime` (real gzip engagement + decoded-content equivalence), and
`websocket-runtime` (H1 WebSocket upgrade/echo/close). #432 added 3 upstream
hash-affinity fixtures (all core): `upstream-hash-affinity`,
`upstream-hash-blocking`, and `upstream-hash-affinity-runtime` (real
NGINX-vs-Jul per-tenant placement). The minimum-category
contract is `coverage.json`; the deterministic, non-scoring aggregate is
`inventory.json`. Both are checked by the importer-tagged corpus lane.

The core lane never reads an external endpoint, user configuration, credential,
or private key. Runtime scenarios target loopback only and assert named response
dimensions rather than claiming global NGINX equivalence.

The pinned real-NGINX reference lane executes `core-multifile-return`,
`routing-cors-policy`, `unix-http-upstream`, `routing-precedence-runtime` and
`upstream-hash-affinity-runtime`. Other fixtures provide assessment/candidate evidence
unless a manifest explicitly adds a reviewed runtime scenario. Protocol-heavy and
stateful residual dimensions remain explicit in `coverage.json`, with rationale and revisit triggers.

See `docs/nginx-migration-corpus.md` for the image digest, isolation controls,
category inventory, protocol decisions, report commands, contribution policy,
and closure boundary.
