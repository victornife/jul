# Final high-impact source batch — 2026-09-29

This stacked batch contains 60 substantive, separately reviewable commits on
top of PR #496. It follows the previous green audit batches. The changes below
are fixes backed by focused regressions, not a claim that every code path or
documentation-ledger row has been reviewed.

| Boundary | Concrete failure addressed |
| --- | --- |
| Upstream and static responses | Truncated declared health bodies, unbounded custom error pages, stale static validators and precompressed sidecars, ambiguous range/encoding fields, compression buffer growth, and unbounded directory listing. |
| gRPC transcoding | Descriptor file and reflection fanout/aggregate bounds; ambiguous credential and reserved metadata; query and message memory ceilings. |
| Admin configuration | Managed apply, watcher, restore, restart and baseline paths now bound files and state markers. Watcher digest/shutdown behavior was corrected. |
| CGI upstreams | uWSGI request framing, response header/status/content-length validation, hop-by-hop stripping, truncated body attribution, bounded stderr, and trusted forwarding/certificate identity for FastCGI and uWSGI. |
| Routing and response middleware | Host canonicalization and malformed label rejection; rate bucket memory/cardinality and header ambiguity; cache flush status capture; CORS requested-header grammar; response recorder and admin telemetry final-status handling. |
| Stream transport | Short TCP writes complete; outgoing PROXY header has a deadline; short UDP datagrams fail the session instead of being counted as delivered. |

Verification is one bounded final Go test pass with the repository's full build
tags and focused regression checks at each commit. The PR's exact-head CI is
the authoritative cross-platform and coverage evidence. No hour-long soak is
being restarted; prior qualifying duration evidence remains in its earlier PR.

Remaining audit scope: the documentation-ledger rows not yet reviewed,
production-like TLS/platform journeys, and broader semantic review outside the
named paths. The release closure and independent sign-offs remain separately
tracked in #480. This batch does not certify 100% source coverage or GA.
