# cache-runtime

Issue #540 adds `TestNGINXCorpusTargetedCacheDifference` beside the existing
stateful test in `cmd/jul/corpus_runtime_test.go`. It reuses this candidate and
a repository-authored synthetic origin. Pinned NGINX 1.28.3 runs read-only,
unprivileged and network-disabled with a mounted local Unix socket. Six
stateful comparisons prove CDN override/no-store/zero/unknown differences,
generic `Expires` agreement and `X-Accel-Expires` divergence. These are expected
differences under `NGX_LOCATION_CACHE_VALID` (approximated), not cache equivalence.
The manifest's single-zone enablement/candidate contract, origin/license and
privacy constraints do not change. Run `make nginx-migration-e2e`.

Repository-authored, sanitized NGINX migration fixture for issue #365. It uses only synthetic local addresses, hostnames, paths, and values; it contains no production configuration, credentials, private keys, external endpoints, or user traffic.

The exact assessment contract, candidate disposition, categories, origin, and license are recorded in `manifest.json`. It proves the bounded `proxy_cache_path`/`proxy_cache` -> Jul `[cache]` translation added alongside this fixture: exactly one declared cache zone, referenced consistently by the one location that uses `proxy_cache`, translates onto Jul's single process-wide cache. The real-Jul stateful cache E2E (a real MISS-then-HIT transition through a real backend, plus a `Cache-Control: no-store` privacy-bypass request that does not disturb the already-stored entry) lives in `cmd/jul/corpus_runtime_test.go` (`TestNGINXCorpusCacheRealE2E`) rather than the generic manifest `scenarios` array, because it asserts a *sequence* of requests and their `X-Cache` disposition, not a single request/response comparison.
