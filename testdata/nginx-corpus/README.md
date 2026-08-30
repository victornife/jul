# NGINX migration corpus

This directory contains repository-authored, sanitized fixtures for issue #154.
Every fixture is self-contained and declares its origin, license, expected
assessment results, selected candidate assertions, and safe replay scenarios in
`manifest.json`.

The core lane never reads an external endpoint, user configuration, credential,
or private key. Runtime scenarios target loopback only and assert named response
dimensions rather than claiming global NGINX equivalence.
