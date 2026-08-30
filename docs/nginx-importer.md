# NGINX config importer

> Feature ID: **Y1-09** · Build tag: `importer` · Since v1.26

A best-effort migration aid that converts an NGINX configuration into a Jul.IA
configuration. Common directives (`http`, `server`, `location`, `listen`,
`server_name`, `root`, `index`, `proxy_pass`, `upstream`, `return`, `rewrite`,
`ssl_*`, `gzip`) are translated into equivalent TOML structures. Every parsed
directive also receives a deterministic migration-assessment result, so an
operator can locate, understand, and remediate unsupported or approximate
behavior rather than have it silently dropped.

## Usage

```bash
go build -tags importer ./cmd/jul

# Generate Jul TOML.
jul import nginx -o jul.toml /etc/nginx/nginx.conf

# Produce a human assessment without writing generated configuration.
jul import nginx --assess /etc/nginx/nginx.conf

# Navigate findings in source order.
jul import nginx --assess --source-order /etc/nginx/nginx.conf

# Produce the versioned JSON assessment with portable relative paths.
jul import nginx --json --path-style relative /etc/nginx/nginx.conf

# Generate TOML and write the assessment beside it.
jul import nginx \
  --input /etc/nginx/nginx.conf \
  --output jul.toml \
  --report migration-assessment.json
```

The conversion command prints a console summary of skipped directives (stderr)
and writes generated TOML to stdout or the `-o` file. The header of the
generated file remains a legacy TODO comment block so existing conversion
workflows and output goldens stay compatible.

The assessment is the authoritative migration-evidence surface. Schema version
2 records a source catalogue, safe path policy, start/end coordinates, bounded
context paths, stable result IDs, structured Jul target mappings, and stable
remediation codes. Relative paths are the default; absolute paths require
`--path-style absolute`. Human and JSON output share one redaction path. See
[NGINX migration assessment](nginx-assessment.md), the
[JSON Schema](nginx-assessment.schema.json), and the
[example report](nginx-assessment.example.json).

## Directive support matrix

The translator walks the nginx directive tree top-down. Directives are grouped
by the scope they appear in. A ✅ means full translation; ⚠️ means a lossy or
approximate mapping; ❌ means the directive requires manual porting. Every status
is represented explicitly in the assessment even when the legacy generated
TOML header remains terse.

### Top-level (`main` context)

| Directive | Status | Notes |
| --- | --- | --- |
| `http` | ✅ | Recursively translated; only one block is allowed (multiple render as separate servers in the output) |
| `stream` | ❌ | Reported; no L4 stream proxy translation today |
| `mail` | ❌ | Reported |
| `include` | ❌ | Blocking in the current single-file tranche; the root-file span and remediation code are reported, but no included file is read implicitly |
| `events`, `worker_processes`, `worker_rlimit_nofile`, `pid`, `user`, `daemon`, `master_process`, `load_module`, `pcre_jit`, `error_log` | ignored | Process-level directives with no per-server Jul.IA equivalent; explicit assessment results record that they have no generated effect |

### `http` block

| Directive | Status | Notes |
| --- | --- | --- |
| `server` | ✅ | Translated to `[[servers]]` |
| `upstream` | ✅ | Translated to `[[upstreams]]`; supports `round_robin`, `least_conn`, and `weighted_round_robin` |
| `gzip` | ✅ | `gzip on` → `[compression] enabled = true` |
| `set_real_ip_from`, `real_ip_header`, `real_ip_recursive` | ⚠️ | Translated to `[servers.client_address]` and inherited by every server block — see [realip](#realip-set_real_ip_from--real_ip_header) |
| `include` | ❌ | Blocking; bounded include traversal is not part of the current single-file implementation |
| `map`, `geo`, `split_clients` | ❌ | Reported with manual conditional-routing guidance |

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
| `return` | ⚠️ | Synthesises a catch-all `/` location. Jul.IA evaluates locations before return, so order may differ from nginx; a note is emitted. |
| `set_real_ip_from`, `real_ip_header`, `real_ip_recursive` | ⚠️ | Translated to `[servers.client_address]` — see [realip](#realip-set_real_ip_from--real_ip_header) |
| `if`, `rewrite` | ❌ | Reported at server level with source provenance and guidance |

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
| `limit_except` | ⚠️ | `limit_except METHODS { deny all; }` or `{ return 403; }` maps to `match.methods`, with a note about the 403-vs-404 difference below. Any other body is reported, never guessed. |
| `add_header` | ⚠️ | `NAME VALUE always;` with a static (non-variable) value maps to `response_headers` or, for `Access-Control-*` names, to `[cors]`. Without `always`, or with a value referencing an nginx variable, it is reported instead. See below. |
| `if` | ❌ | Reported with source coordinates, context and a reason; never silently converted |

> `match.methods` can now express a method constraint (see
> [Request predicates](configuration.md#request-predicates)), and `limit_except`
> is translated for its one idiomatic, unambiguous shape:
> `limit_except METHODS { deny all; }` or `{ return 403; }`, with no other
> directive in the block. That shape means "requests using any other method are
> denied," which maps cleanly onto `match.methods = [METHODS]` — a request using
> a different method simply does not match this location. The mapping is not
> perfect: nginx returns 403 for the excluded methods, while Jul.IA makes the
> route not match them at all (typically a 404, or whichever other location
> covers the path), so a note flags the difference for review. Any other body
> inside `limit_except` — a directive meant to apply only to the *excluded*
> methods, rather than a bare denial — has no single-location equivalent in
> Jul.IA's model and is reported instead of guessed.
>
> `[[servers.locations.response_headers]]` and `[servers.locations.cors]` can
> express response-header and CORS policy (see
> [Response headers and CORS](configuration.md#response-headers-and-cors)), and
> `add_header` is translated only for cases that are actually equivalent. Plain
> `add_header` without `always` does not apply to nginx's own 4xx/5xx responses,
> while Jul's operations always apply. Translating that case would silently
> widen where the header appears on an error path, so it remains blocking.
> `add_header NAME VALUE always;` is translated unless `VALUE` references an
> nginx variable such as `$http_origin` or `$request_id`; Jul values are static,
> so a variable reference is never misrepresented as a literal.
>
> A location whose only CORS-relevant directives are always-flagged, static
> `add_header Access-Control-*` lines is translated into
> `[servers.locations.cors]`: `Allow-Origin` maps to `allowed_origins`,
> `Allow-Methods`/`Allow-Headers`/`Expose-Headers` split on commas into lists,
> and `Allow-Credentials`/`Max-Age` map directly. Any CORS header gated by `if`
> or reflecting a variable is reported instead. A source combining
> `Access-Control-Allow-Origin "*"` with
> `Access-Control-Allow-Credentials "true"` is also reported rather than emitted
> as a block that would fail Jul validation.

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

### realip (`set_real_ip_from` / `real_ip_header`)

nginx's realip module and Jul's
[`[servers.client_address]`](configuration.md#client-address-and-trusted-proxies)
express the same idea — which proxies may assert a client address — so the
mapping is direct. Jul's `$remote_addr` is the derived client and
`$realip_remote_addr` is the socket peer, exactly as in nginx with realip
configured, so `proxy_set_header X-Real-IP $remote_addr` and
`X-Forwarded-For $proxy_add_x_forwarded_for` keep their meaning.

| nginx | Jul.IA |
| --- | --- |
| `set_real_ip_from <cidr\|address>` | appended to `client_address.trusted_proxies` |
| `real_ip_header X-Forwarded-For` | `forwarded_headers = ["x-forwarded-for"]` |
| `real_ip_header Forwarded` | `forwarded_headers = ["forwarded"]` |
| `real_ip_header X-Real-IP` (nginx's default) | **not supported** — reported |
| `real_ip_header proxy_protocol` | **not supported** — reported |
| `real_ip_recursive on` | already the default: the chain is always evaluated right to left |
| `real_ip_recursive off` | **not supported** — reported |

Four behaviours are worth knowing before importing:

1. **An unsupported form emits no policy at all.** If any realip directive in a
   scope cannot be expressed, the importer reports it and writes no
   `[servers.client_address]` block rather than substituting a different rule.
   A migrated server then keeps peer-only identity, which is the safe outcome.
2. **`real_ip_header` defaults to `X-Real-IP` in nginx**, which Jul does not
   support because a single address carries no chain to evaluate against a trust
   boundary. A config with `set_real_ip_from` but no `real_ip_header` is
   therefore reported, not silently translated to `X-Forwarded-For`.
3. **The policy is hoisted to the listen address.** Jul derives the client
   address before the `Host` header selects a server block, so one address has
   one policy. A policy on any block is applied to every block on that address
   and the hoist is noted. If two blocks on one address declare different realip
   policies, neither is emitted and the conflict is reported.
4. **A source without realip never gains trust.** No policy is invented.

Outbound emission differs in one documented way: nginx's
`$proxy_add_x_forwarded_for` appends to the inbound chain, while Jul rebuilds it
as `<canonical client>, <direct peer>`. For a single proxy the result is
identical; for a longer chain Jul drops intermediate trusted hops deliberately
(see [core-http.md](core-http.md#forwarded-headers-to-the-backend)).

## Known limitations

1. **`include` is not followed in the current single-file tranche.** The
   assessment reads only the supplied root file, records each `include` as a
   blocking finding with exact root-file provenance, and reports
   `source_policy.follow_includes = false`. Do not import snippets independently
   and assume concatenation preserves NGINX include context, ancestry, or order.
   Root-confined traversal, deterministic globs, cycle detection, symlink
   protection, and byte/file/depth limits are a separate implementation tranche.

2. **`stream` / `mail` modules are skipped.** No L4 or mail proxy translation
   exists today.

3. **`map`, `geo`, `split_clients` are skipped.** Variable-driven routing logic
   cannot be represented directly; port it into Jul.IA middleware or upstream
   selectors.

4. **`if` and unsupported rewrite control flow are skipped.** Complex control
   flow must be rewritten using Jul.IA matches, rewrites, redirects, or
   middleware.

5. **Per-virtual-host realip policies cannot be represented.** Jul scopes the
   trusted-proxy policy to the listen address, so a source config whose server
   blocks on one port disagree about `set_real_ip_from` is reported instead of
   translated. Reconcile the policies or split the listeners before importing.

6. **Named locations (`@fallback`) are skipped.** Error-page/named-location
   chains have no equivalent.

7. **One listen per server.** Jul.IA `[[servers]]` binds a single address. Extra
   `listen` directives are dropped with an approximate finding.

8. **Trailing-slash `proxy_pass` semantics differ.** Nginx strips the matched
   location prefix when the target has a trailing slash; Jul.IA does not. The
   importer drops the slash and asks the operator to verify backend paths.

9. **Alias semantics are approximate.** `alias` maps to `root`; the path-prefix
   stripping that nginx performs is not replicated.

10. **Server-level `return` precedence differs.** Nginx evaluates a server-level
    `return` before locations; Jul.IA routes through locations first. A catch-all
    `/` location is synthesised and the difference is reported.

## Benchmarks

Run with `go test -tags importer -bench=. ./internal/migrate/nginx/`.

| Benchmark | Input | ns/op | allocs/op |
| --- | --- | --- | --- |
| `BenchmarkParse` | Full representative config (2 servers, 1 upstream, 9 locations, gzip, TLS, stream block) | ~45 000 | ~280 |
| `BenchmarkTranslate` | Same config | ~6 500 | ~40 |

Translation is ~7× faster than parsing and allocates ~6× less, because the tree
is already materialised. All exported paths (`ImportFile` → `parseFile` →
`Translate` → `config.Marshal`) recover from panics so a malformed input never
crashes the tool.

## Threat note

The importer is a **dev-time / migration-time** tool; it does not run inside the
server request path, but it processes untrusted files and produces configuration
that the server may later load.

1. **Malicious nginx.conf → crash / DoS.** A crafted config may panic the
   third-party parser (`gonginx`). `parseFile` and `parseString` recover and
   convert panics into errors, so the CLI exits cleanly.

2. **Path traversal in `ssl_certificate` / `root` / `proxy_pass`.** The importer
   copies generated-config paths according to existing translation behavior; it
   does not prove that a path is safe to load. Review generated TOML before use.

3. **Credential leakage in generated configuration.** A source target may carry
   embedded credentials that must not be committed to VCS. The schema v2
   assessment redacts such values, but that does not remove them from an
   explicitly generated configuration where the translation itself preserves
   them.

4. **Legacy TODO header versus assessment output.** The legacy generated-TOML
   TODO header is retained for output compatibility and may contain more source
   detail than the assessment. Human and JSON assessment output instead use one
   bounded redaction function for headers, URL userinfo, credential/private-key
   paths, include arguments, variables, and Lua/snippet content. Prefer the
   assessment for sharing and automation, and review generated TOML separately.

5. **Translation correctness gaps lead to runtime misconfiguration.** Treat the
   generated TOML as a draft. Resolve every blocking/approximate finding and run
   Jul's authoritative validation before deployment.

6. **The importer build tag keeps the nginx-parser dependency out of the default
   binary, but the tagged binary still links the parser library.** Keep the
   tagged binary on a trusted build host.

## Runnable example

`examples/migrate/nginx.conf` is a representative NGINX config (gzip, TLS,
upstream pool with weights, static and proxy servers, and a stream block that is
reported). Import it with:

```bash
go run -tags importer ./cmd/jul import nginx -o jul.toml examples/migrate/nginx.conf
```

The expected output (`jul.toml`) is provided as `examples/migrate/imported.toml`
so users can diff against it. Use `--json` or `--assess` to inspect the migration
evidence without changing that generated-TOML golden.

## GA status

| Criterion | Status | Evidence |
| --- | --- | --- |
| 1 — Conformance / behaviour matrix | ✅ | Directive-support matrix above plus schema v2 result taxonomy/provenance |
| 2 — Published benchmark numbers | ✅ | `BenchmarkParse` / `BenchmarkTranslate` in `internal/migrate/nginx/bench_test.go` |
| 3 — Known-limitations list | ✅ | 10-item limitation list above |
| 4 — Semver-guarded config/API contract | ✅ | v1 config freeze and versioned assessment schema |
| 5 — Long-running soak test | ✅ | validated via `test-nginx-importer.ps1` 2026-07-06 — [evidence](soak-evidence.md#2026-07-06--phase-2b-soak-preparation-local-windows-5-min-smoke--validation-scripts) |
| 6 — Runnable example + docs | ✅ | `examples/migrate/nginx.conf`, `imported.toml`, assessment schema/example, and this guide |
| 7 — Security / threat note | ✅ | 6-row threat note above |
| 8 — Fuzzing where parsing is involved | ✅ | `FuzzTranslate` in `internal/migrate/nginx/fuzz_test.go` (parse + translate + marshal round-trip) |
| 9 — Self-explanatory Console surface | ✅ | CLI-only tool; operable from `jul import nginx --help` |
