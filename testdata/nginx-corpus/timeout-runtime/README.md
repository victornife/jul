# timeout-runtime

Repository-authored, sanitized NGINX migration fixture for issue #365. It uses only synthetic local addresses, hostnames, paths, and values; it contains no production configuration, credentials, private keys, external endpoints, or user traffic.

The exact assessment contract, candidate disposition, categories, origin, and license are recorded in `manifest.json`. It proves the `proxy_connect_timeout`/`proxy_read_timeout` -> Jul `[[servers.locations]]` translation added alongside this fixture: a real Jul instance actually enforces the configured `proxy_read_timeout` against a backend that stalls its response, returning a `504` within a tight deterministic bound (per `docs/core-http.md`'s `upstream_timeout` -> 504 mapping). The real-Jul E2E lives in `cmd/jul/corpus_runtime_test.go` (`TestNGINXCorpusProxyReadTimeoutRealE2E`) because it needs a deliberately slow backend and a wall-clock bound assertion, which the generic manifest `scenarios` array does not express.
