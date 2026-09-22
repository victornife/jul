# websocket-runtime

Repository-authored, sanitized NGINX migration fixture for issue #365. It uses only synthetic local addresses, hostnames, paths, and values; it contains no production configuration, credentials, private keys, external endpoints, or user traffic.

The exact assessment contract, candidate disposition, categories, origin, and license are recorded in `manifest.json`. The real-Jul WebSocket E2E lives in `cmd/jul/corpus_runtime_test.go` (`TestNGINXCorpusWebSocketRealE2E`) rather than the generic manifest `scenarios` array, because it needs a persistent bidirectional connection instead of a single request/response.
