# ADR 0008 — `gofast` / `x/tools` dependency pin (technical debt)

- **Status:** Deferred — recorded as technical-debt
- **Date:** 2026-06-30
- **Deciders:** Jul.IA maintainers
- **Applies to:** `go.mod` replace directive, FastCGI handler, supply chain
- **Source:** Post-audit review — external audit recommendation C-4

## Context

`go.mod` contains:

```go
replace golang.org/x/tools => golang.org/x/tools v0.6.0
```

The pin is necessary because `github.com/yookoala/gofast@v0.8.0` (FastCGI client
library) imports `golang.org/x/tools/godoc/vfs`, which was removed in
`x/tools v0.30.0+`.  The latest `x/tools` (v0.40.0 at time of writing) no longer
ships `godoc/vfs`, so a plain `go get` or transitive update breaks the build.

The comment in `go.mod` notes that this path is "not runtime except
gofast/vfs", i.e. the vulnerable surface is only through FastCGI/uWSGI
configuration, not the core proxy path.

## Decision

**No immediate action.** The pin is low-risk for a runtime server because:

1. The affected code path is only exercised when a location uses
   `fastcgi_pass` or `uwsgi_pass`.
2. `gofast` is a thin FastCGI protocol client; `godoc/vfs` is only used for
   helper abstractions, not for serving user content.
3. No known CVE in `x/tools v0.6.0` affects the `godoc/vfs` surface at this
time.

## Options evaluated

| Option | Effort | Pros | Cons |
|--------|--------|------|------|
| **A. Replace gofast** with a maintained FastCGI/uWSGI client | Medium | Eliminates the pin cleanly; modern dependency | Risk of protocol incompatibilities; requires migration tests |
| **B. Vendor `godoc/vfs` locally** (copy the ~300-line package into `internal/vendor/vfs`) | Low | Removes replace directive; no external dep changes | Adds maintenance burden for a dead package; licence review needed |
| **C. Gate `fastcgi_pass`/`uwsgi_pass` behind a build tag** and move `gofast` to a separate module | Medium | Keeps core binary free of the pin; fastcgi users opt-in | FastCGI becomes second-class; more build matrix complexity |
| **D. Keep the pin** | Zero (now) | Stable; no churn | Supply-chain hygiene warning; may block future `x/tools` updates |

## Consequences

- We take **Option D now** and schedule a re-evaluation whenever a security
  advisory is published that touches `x/tools v0.6.0` or when the FastCGI
  feature is next touched.
- The backlog item is linked from `go.mod` with a `TODO(ADR-0008)` comment.

## Related

- `go.mod` — `replace golang.org/x/tools` line
- `internal/handler/fastcgi.go` — the only importer of `gofast`
- `internal/handler/uwsgi.go` — also uses `gofast`
