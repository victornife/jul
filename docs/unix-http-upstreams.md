# HTTP reverse proxying over Unix-domain-socket upstreams

Issue #407 makes Unix-domain sockets a first-class backend network for the existing HTTP reverse-proxy and upstream stack. This is an extension of the normal upstream path, not a second proxy implementation.

## Configuration contract

Unix HTTP backends are expressed through a named upstream:

```toml
[[upstreams]]
name = "local-app"
servers = ["unix:/run/local-app.sock"]

[[servers]]
listen = ":8080"

[[servers.locations]]
match = { type = "prefix", path = "/" }
proxy_pass = "http://local-app"
```

The socket path is backend dial identity. It is not an HTTP authority. Direct spellings such as `proxy_pass = "http://unix:/run/local-app.sock"` or `https://unix:...` are deliberately unsupported and fail with guidance to create a named upstream.

The initial contract is plaintext HTTP/1.1 over AF_UNIX. TLS-over-Unix, `backend_tls` on a pool containing a Unix member, HTTP/2-over-Unix and h2c-over-Unix are not claimed. An `https://<named-upstream>` route containing a Unix member is rejected before live traffic.

## HTTP authority and connection identity

For a selected Unix backend Jul keeps three concepts separate:

- `network = "unix"` selects AF_UNIX dialing;
- `address` is the normalized filesystem path used only for dialing;
- an opaque, stable per-backend transport key isolates `net/http` keep-alive and `MaxConnsPerHost` state without exposing the path as an HTTP authority.

The incoming request Host is preserved for Unix HTTP unless `[headers].Host` explicitly overrides it. The synthetic transport key is never forwarded as Host. Raw socket paths are not used as metric labels, tracing authorities, or client-facing gateway-error strings.

The runtime/admin projections expose the bounded `network` field (`tcp` or `unix`) separately from `address`. The Console and `/api/v1/upstreams` therefore do not need to infer backend kind by parsing a filesystem path. Configured Unix addresses remain rendered canonically as `unix:/path.sock`; live runtime addresses remain normalized dial addresses plus `network = "unix"`.

## Shared resilience and lifecycle semantics

Unix HTTP runs through the existing handler-generation-owned `http.Transport`, `balancingTransport`, upstream `Pool`, admission controller, retry budget, circuit state, connection accounting and reload/retirement machinery.

Consequently the same controls apply to TCP and Unix members:

- round-robin, weighted round-robin and least-connections balancing;
- global active/pending admission limits;
- per-backend connection limits;
- retry/deadline/backoff/budget behavior, including TCP-to-Unix and Unix-to-Unix retry;
- passive failure marking and circuit open/half-open recovery;
- cancellation while waiting for admission;
- keep-alive reuse inside one backend identity and isolation across different sockets;
- SSE/chunked streaming and HTTP `101`/WebSocket passthrough;
- handler-generation retirement through `CloseIdleConnections`.

Plain HTTP pools may contain both TCP and Unix members. Backend selection is still performed by the pool; the chosen backend's network determines the dial on each attempt.

## Health checks

`health_check.type = "http"` is rejected for a pool containing a Unix member. The existing public health-check type `"tcp"` means connect/liveness rather than “force the TCP network”; for a Unix backend it calls the dialer with the backend's configured `unix` network and path.

Socket existence is intentionally not checked during configuration load. A missing socket is a runtime reachability failure handled by the normal failure/circuit path, which allows a backend process to publish its socket after Jul has started.

## NGINX migration

The importer preserves the common named-upstream shape:

```nginx
upstream local_app {
    server unix:/tmp/jul407/backend.sock;
}
server {
    listen 18086;
    location / {
        proxy_pass http://local_app;
    }
}
```

The `unix-http-upstream` migration-corpus fixture validates the imported Jul candidate against a real AF_UNIX HTTP backend and also runs the pinned NGINX reference image against an AF_UNIX HTTP backend for the selected status/body dimensions. NGINX's separate direct-Unix `proxy_pass` syntax is not copied into Jul; it produces an actionable, source-located manual-mapping finding instead.

## Platform and verification contract

The permanent `CGC-FOLLOWUP #407 Unix HTTP proxy quality` workflow runs real AF_UNIX HTTP contract tests on Linux, macOS and Windows runners. If a supported runner stops providing usable AF_UNIX semantics, that platform gate must fail rather than silently skipping the behavior. macOS fixtures use a deliberately short `/tmp` path to stay below Darwin's Unix-socket path limit.

The same workflow also runs the full configured-neighbor tag matrix, race detection, importer checks, issue-owned added-production-statement coverage, dependency integrity (`go mod verify` plus no-drift `go mod tidy`), and a same-runner TCP hot-path benchmark comparison against `main`. The benchmark reports the <=2% regression target from #407 and treats >5% as an automatic material-regression failure requiring investigation.

The implementation uses Go's existing standard-library networking and HTTP primitives and adds no new runtime third-party dependency. Dependency correctness is therefore expressed as no unexpected `go.mod`/`go.sum` drift plus the repository-wide vulnerability and dependency gates, not as an unnecessary package-version bump.
