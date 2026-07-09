# gofast vendor provenance

This directory contains a vendored copy of github.com/yookoala/gofast.

- Upstream module: github.com/yookoala/gofast
- Upstream version: v0.8.0
- Upstream source commit: b9e83d1b95620b6d780d2b02e2482cff1d10d1db
- Vendor note: the tree is maintained as a local copy via the `replace github.com/yookoala/gofast => ./third_party/gofast` directive in the repository root.

When changing this vendor tree, re-run the relevant Go tests and `govulncheck` for the repository so the vendored dependency remains reviewed and scanned.
