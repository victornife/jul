# NGINX config importer

> Feature ID: **Y1-09** · Build tag: `importer` · Since v1.26

A best-effort migration aid that converts an NGINX configuration into a Jul.IA
configuration. Common directives (`http`, `server`, `location`, `listen`,
`server_name`, `root`, `index`, `proxy_pass`, `upstream`, `return`, `rewrite`,
`ssl_*`, `gzip`) are translated into equivalent TOML structures; every
directive that cannot be mapped is captured in a `Report` with a line reference
so an operator can port it by hand rather than have it silently dropped.

## Usage

```bash
go build -tags importer ./cmd/jul
jul import nginx -o jul.toml /etc/nginx/nginx.conf
```

The command prints a console summary of skipped directives (stderr) and writes
the generated TOML to stdout or the `-o` file.  The header of the generated file
is a comment block that lists every skipped directive as a TODO so the gap is
visible even when the console output is lost.

## Directive support matrix

The translator walks the nginx directive tree top-down.  Directives are grouped
by the scope they appear in.  A ✅ means full translation; ⚠️ means a lossy or
approximate mapping (noted in the TOML header); ❌ means the directive is
reported in `Skipped` for manual porting.

### Top-level (`main` context)

| Directive | Status | Notes |
| --- | --- | --- |
| `http` | ✅ | Recursively translated; only one block is allowed (multiple render as separate servers in the output) |
| `stream` | ❌ | Reported; no L4 stream proxy translation today |
| `mail` | ❌ | Reported |
| `include` | ❌ | Reported; operator must import each included file separately |
| `events`, `worker_processes`, `worker_rlimit_nofile`, `pid`, `user`, `daemon`, `master_process`, `load_module`, `pcre_jit`, `error_log` | ignored | Process-level directives with no per-server Jul.IA equivalent; silently ignored to keep noise low |

### `http` block

| Directive | Status | Notes |
| --- | --- | --- |
| `server` | ✅ | Translated to `[[servers]]` |
| `upstream` | ✅ | Translated to `[[upstreams]]`; supports `round_robin`, `least_conn`, and `weighted_round_robin` |
| `gzip` | ✅ | `gzip on` → `[compression] enabled = true` |
| `include` | ❌ | Reported; operator must import each included file separately |
| `map`, `geo`, `split_clients` | ❌ | Reported |

### `server` block

| Directive | Status | Notes |
| --- | --- | --- |
| `listen` | ✅ | Normalises bare ports (`80` → `:80`), wildcards (`*:80` → `:80`), IPv6-any (`[::]:80` → `:80`). TLS flag inferred. Unix sockets are reported. Only the first `listen` per server is kept; extras are noted. |
| `server_name` | ✅ | The `_` catch-all pseudo-name is dropped; all other names are kept |
| `root` | ✅ | Applied to the server; a synthetic `/` location is created when no location already matches root |
| `index` | ✅ | Inherited into the synthetic `/` location |
| `location` | ✅ | See location table below |
| `ssl_certificate` | ✅ | → `servers.tls.cert` |
| `ssl_certificate_key` | ✅ | → `servers.tls.key` |
| `ssl_protocols` | ⚠️ | Mapped to `tls.min_version` (`1.2` or `1.3`). Legacy protocols (`TLSv1`, `TLSv1.1`) raise the floor to `1.2` with a note. |
| `return` | ⚠️ | Synthesises a catch-all `/` location.  Jul.IA evaluates locations before return, so order may differ from nginx; a note is emitted. |
| `if`, `rewrite` | ❌ | Reported at server level |

### `location` block

| Directive | Status | Notes |
| --- | --- | --- |
| `proxy_pass` | ✅ | Bare host receives `http://` scheme. Trailing slash is dropped with a note (nginx rewrites the matched prefix; Jul.IA does not). |
| `fastcgi_pass` | ✅ | → `fastcgi_pass` |
| `root` | ✅ | Overrides inherited server root |
| `alias` | ⚠️ | Mapped to `root` with a note; path semantics differ slightly |
| `index` | ✅ | Overrides inherited server index |
| `try_files` | ✅ | → `try_files` (string slice) |
| `return` | ✅ | Numeric codes map directly; `return <url>` maps to `return = 302` + `redirect`. Response body text is dropped with a note. |
| `rewrite` | ✅ | `pattern`, `replacement`, and `flag` (`last`/`break`/`redirect`/`permanent`) are preserved; unknown flags are noted |
| `if`, `limit_except` | ❌ | Reported |

### `upstream` block

| Directive | Status | Notes |
| --- | --- | --- |
| `server` | ✅ | Address and `weight=N` preserved. `down` flag omits the server with a note. Other flags (`backup`, `max_fails`, …) are ignored (not reported). |
| `least_conn` | ✅ | → `strategy = "least_conn"` |
| `ip_hash`, `hash`, `random` | ⚠️ | Falls back to `round_robin` with a note |
| `keepalive`, `keepalive_timeout`, `keepalive_requests`, `zone` | ignored | Connection-pool tuning; safe to drop |

### Location modifiers

| Modifier | Jul.IA `match.type` | Notes |
| --- | --- | --- |
| (none) | `prefix` | |
| `=` | `exact` | |
| `^~` | `prefix` | Nginx priority difference is approximated |
| `~` | `regex` | |
| `~*` | `regex` | Case-insensitivity is **not** preserved; noted |

## Known limitations

1. **`include` is not followed.**  The importer processes a single file.  If the
   nginx config is split across `conf.d/` snippets, import the root file and
   then import each snippet separately (they will share a single `[[upstreams]]`
   pool because TOML merges arrays of tables from separate files only when the
   operator concatenates them).

2. **`stream` / `mail` modules are skipped.**  No L4 or mail proxy translation
   exists today.

3. **`map`, `geo`, `split_clients` are skipped.**  Variable-driven routing logic
   cannot be represented directly; port it into Jul.IA middleware or upstream
   selectors.

4. **`if` and `rewrite` inside locations are skipped.**  Complex control flow
   must be rewritten using Jul.IA rewrites, redirects, or middleware.

5. **Named locations (`@fallback`) are skipped.**  Error-page / named-location
   chains have no equivalent.

6. **One listen per server.**  Jul.IA `[[servers]]` binds a single address.
   Extra `listen` directives are dropped with a note.

7. **Trailing-slash `proxy_pass` semantics differ.**  Nginx strips the matched
   location prefix when the target has a trailing slash; Jul.IA does not.  The
   importer drops the slash and emits a note asking the operator to verify the
   upstream / location path alignment.

8. **Alias semantics are approximate.**  Mapped to `root`; the path-prefix
   stripping that nginx alias performs is not replicated.

9. **Server-level `return` precedence differs.**  Nginx evaluates a server-level
   `return` before locations; Jul.IA routes through locations first.  A
   catch-all `/` location is synthesised and a note is emitted.

## Benchmarks

Run with `go test -tags importer -bench=. ./internal/migrate/nginx/`.

| Benchmark | Input | ns/op | allocs/op |
| --- | --- | --- | --- |
| `BenchmarkParse` | Full representative config (2 servers, 1 upstream, 9 locations, gzip, TLS, stream block) | ~45 000 | ~280 |
| `BenchmarkTranslate` | Same config | ~6 500 | ~40 |

Translation is ~7× faster than parsing and allocates ~6× less, because the tree
is already materialised.  All exported paths (`ImportFile` → `parseFile` →
`Translate` → `config.Marshal`) recover from panics so a malformed input never
crashes the tool.

## Threat note

The importer is a **dev-time / migration-time** tool; it does not run inside the
server request path, but it does process untrusted files and produces
configuration that the server will later load.  Risks:

1. **Malicious nginx.conf → crash / DoS.**  A crafted config may panic the
   third-party parser (`gonginx`).  Both `parseFile` and `parseString` wrap the
   parser in `recover()` and convert panics into errors, so the CLI exits
   cleanly rather than crashing the operator's shell.

2. **Path traversal in `ssl_certificate` / `root` / `proxy_pass`.**  The importer
   copies paths verbatim from the nginx file; it does not expand `..` or verify
   file existence.  A malicious config with `root /etc/` or `ssl_certificate
   /etc/shadow` would be emitted in the TOML unchanged.  These paths must be
   reviewed by the operator before the server loads the generated config.

3. **Credential leakage via `proxy_pass` hidden in upstream names.**  An upstream
   block may contain a `server` directive with embedded credentials (`server
   http://user:pass@host/`).  The importer preserves the full string, which may
   end up in generated TOML that the operator checks into VCS.  Operators should
   search generated configs for `://` URLs before committing them.

4. **Information disclosure in `Skipped` report.**  The Report header (prepended
   to the generated TOML) lists every unmapped directive with its source line.
   If the original nginx file contains sensitive directive arguments that are
   skipped, they are echoed into the output.  Review the generated file before
   sharing it.

5. **Translation correctness gaps lead to runtime misconfiguration.**  Because the
   mapping is best-effort, a translated config may have subtly different
   semantics (e.g. alias semantics, location priority, `return` ordering).  The
   operator must treat the generated TOML as a draft and run `jul lint` on it
   before deployment.

6. **The importer build tag keeps the nginx-parser dependency out of the default
   binary, but the tagged binary still links the parser library.**  Keep the
   tagged binary on a trusted build host; do not distribute it to untrusted
   execution environments if the parser contains unsafe code.

## Runnable example

`examples/migrate/nginx.conf` is a representative NGINX config (gzip, TLS,
upstream pool with weights, static + proxy servers, a stream block that is
skipped).  Import it with:

```bash
go run -tags importer ./cmd/jul import nginx -o jul.toml examples/migrate/nginx.conf
```

The expected output (`jul.toml`) is provided as `examples/migrate/imported.toml`
so users can diff against it.

## GA status

| Criterion | Status | Evidence |
| --- | --- | --- |
| 1 — Conformance / behaviour matrix | ✅ | Directive-support matrix above (top-level, http, server, location, upstream, modifiers) |
| 2 — Published benchmark numbers | ✅ | `BenchmarkParse` / `BenchmarkTranslate` in `internal/migrate/nginx/bench_test.go` |
| 3 — Known-limitations list | ✅ | 9-item limitation list above |
| 4 — Semver-guarded config/API contract | ✅ | v1 config freeze (cross-cutting) |
| 5 — Long-running soak test | ☐ | Post-GA gate per ADR-0005 |
| 6 — Runnable example + docs | ✅ | `examples/migrate/nginx.conf` + `imported.toml` + this doc |
| 7 — Security / threat note | ✅ | 6-row threat note above |
| 8 — Fuzzing where parsing is involved | ✅ | `FuzzTranslate` in `internal/migrate/nginx/fuzz_test.go` (parse + translate + marshal round-trip) |
| 9 — Self-explanatory Console surface | ✅ | CLI-only tool; operable from `jul import nginx --help` |
