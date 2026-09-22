# compression-runtime

Repository-authored, sanitized NGINX migration fixture for issue #365. It uses only synthetic local addresses, hostnames, paths, and values; it contains no production configuration, credentials, private keys, external endpoints, or user traffic.

The exact assessment contract, candidate disposition, categories, origin, and license are recorded in `manifest.json`. The real-Jul gzip E2E lives in `cmd/jul/corpus_runtime_test.go` (`TestNGINXCorpusCompressionRealE2E`) rather than the generic manifest `scenarios` array, because it inspects raw `Content-Encoding` and gunzip-decodes the body itself instead of relying on the HTTP client's transparent decompression.
