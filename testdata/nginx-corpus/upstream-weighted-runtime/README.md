# upstream-weighted-runtime

Repository-authored, sanitized NGINX migration fixture for issue #365. It uses only synthetic local addresses, hostnames, paths, and values; it contains no production configuration, credentials, private keys, external endpoints, or user traffic.

The exact assessment contract, candidate disposition, categories, origin, and license are recorded in `manifest.json`. The real-Jul weighted-distribution E2E lives in `cmd/jul/corpus_runtime_test.go` (`TestNGINXCorpusWeightedUpstreamDistribution`) rather than the generic manifest `scenarios` array, because it asserts backend-selection counts across many repeated requests rather than a single request/response.
