# NGINX config importer

> Base importer: **Y1-09, GA/soaked** · Assessment/provenance/includes: **MIG-ASSESS, Beta/merged** · Build tag: `importer`

A best-effort migration aid that converts NGINX configuration into Jul.IA TOML.
Common HTTP, server, location, upstream, TLS, compression, static-file, proxy,
redirect, rewrite, response-header, CORS, and trusted-client-address constructs
are translated. Every parsed directive also receives a deterministic migration
assessment result so unsupported or approximate behavior is never silently
lost.

Generated TOML is a draft, not a cutover certificate. Review every blocking and
approximate result and validate the candidate before deployment.

## Usage

```bash
go build -tags importer ./cmd/jul

# Generate Jul TOML from one root file. Includes are not followed implicitly.
jul import nginx -o jul.toml /etc/nginx/nginx.conf

# Human or JSON assessment only.
jul import nginx --assess /etc/nginx/nginx.conf
jul import nginx --json /etc/nginx/nginx.conf

# Follow a real multi-file estate under an explicit root.
jul import nginx \
  --assess \
  --follow-includes \
  --root /etc/nginx \
  /etc/nginx/nginx.conf

# Generate TOML and a machine report when the complete tree is safe to read.
jul import nginx \
  --follow-includes \
  --include-root /etc/nginx \
  --input /etc/nginx/nginx.conf \
  --output jul.toml \
  --report migration-assessment.json
```

`--root` and `--include-root` are aliases. `--input` is an alternative to the
positional source path and `--output` is an alias for `-o`. Relative source
paths are the default; `--path-style absolute` is an explicit local-only choice.
`--source-order` changes only human assessment navigation.

The conversion command writes generated TOML to stdout or `-o` and prints the
legacy skipped-directive summary to stderr. The generated file's TODO header is
retained for compatibility. The versioned assessment is the authoritative
migration-evidence surface. See [NGINX migration
assessment](nginx-assessment.md), its [JSON
Schema](nginx-assessment.schema.json), and the [multi-file example
report](nginx-assessment.example.json).

## Include security

The default remains single-file and non-surprising: without
`--follow-includes`, Jul reads only the supplied root file and reports each
`include` as blocking. No hidden filesystem traversal occurs.

When traversal is enabled, Jul owns source discovery. `gonginx` parses one
already-bounded file at a time; its own include parser is not used.

### Root policy

- The assessment root defaults to the input file's directory.
- Relative includes resolve from the including file's directory.
- Absolute includes are accepted only when they remain inside the configured
  root.
- Jul checks both the cleaned lexical path and the evaluated symlink target.
- `..` escape, drive/UNC escape, and symlink escape fail closed.
- Network URLs, shell expansion, arbitrary directory crawling, and unrestricted
  host-root traversal are not supported.
- Reports use relative `/`-separated display paths by default and never expose
  an escaped host root in error text.

### Matching and order

Explicit files, standard filesystem globs, nested includes, and repeated
non-cyclic includes are supported. Glob matches are sorted deterministically.
Hidden files are excluded from wildcard matches. An invalid or unmatched glob
is blocking because the assessed source tree would otherwise be incomplete.
Repeated includes remain separate source instances with separate ancestry and
result IDs; they are not silently deduplicated.

Included directives are inserted at the include point before the existing
translator/classifier runs. A fragment included inside `http`, `server`,
`location`, or `upstream` keeps that context.

### Resource limits

| Flag | Default |
| --- | ---: |
| `--max-include-depth` | `16` |
| `--max-include-files` | `256` |
| `--max-include-file-bytes` | `4194304` |
| `--max-include-total-bytes` | `33554432` |
| `--max-include-glob-matches` | `1024` |

All values must be positive. A missing/unreadable source, malformed glob,
cycle, parse error, root/symlink escape, or limit breach produces a stable
blocking `NGX_INCLUDE_*` result at the responsible include. The report lists
sources already read and sets `source_policy.complete = false`. A followed but
incomplete tree never writes generated TOML.

## Directive support matrix

The translator walks the assembled directive tree top-down. ✅ is direct
translation, ⚠️ is approximate or conditionally supported, ❌ requires manual
porting, and **ignored** means the source controls the NGINX process rather than
the Jul request path.

### Top-level (`main` context)

| Directive | Status | Notes |
| --- | --- | --- |
| `http` | ✅ | Recursively translated. |
| `include` | ⚠️ | Blocking by default; informational after complete bounded expansion. |
| `stream`, `mail` | ❌ | No stream/mail translation today. |
| `events`, `worker_processes`, `worker_rlimit_nofile`, `pid`, `user`, `daemon`, `master_process`, `load_module`, `pcre_jit`, `error_log` | ignored | Explicit assessment results, no generated effect. |

### `http` block

| Directive | Status | Notes |
| --- | --- | --- |
| `server` | ✅ | Translated to `[[servers]]`. |
| `upstream` | ✅ | Translated to `[[upstreams]]`. |
| `gzip` | ✅ | `gzip on` enables compression. |
| `set_real_ip_from`, `real_ip_header`, `real_ip_recursive` | ⚠️ | Maps to listener-scoped client-address policy; see [realip](#realip-set_real_ip_from--real_ip_header). |
| `include` | ⚠️ | Expanded only through the bounded resolver above. |
| `map`, `geo`, `split_clients` | ❌ | Variable-driven behavior requires manual design. |

### `server` block

| Directive | Status | Notes |
| --- | --- | --- |
| `listen` | ✅ | Normalizes bare ports, wildcards, and IPv6-any; TLS flag inferred. Only the first listen is retained. |
| `server_name` | ✅ | `_` is dropped; other names are retained. |
| `root`, `index` | ✅ | Applied to the server or synthesized `/` location. |
| `location` | ✅ | See the location table. |
| `ssl_certificate`, `ssl_certificate_key` | ✅ | Map to server TLS fields. |
| `ssl_protocols` | ⚠️ | Maps to a minimum version; legacy versions raise the floor to TLS 1.2. |
| `return` | ⚠️ | Synthesizes `/`; NGINX server-level precedence differs. |
| `set_real_ip_from`, `real_ip_header`, `real_ip_recursive` | ⚠️ | See [realip](#realip-set_real_ip_from--real_ip_header). |
| `if`, server-level `rewrite` | ❌ | Reported with provenance and guidance. |
| `include` | ⚠️ | Expanded in server context only through the bounded resolver. |

### `location` block

| Directive | Status | Notes |
| --- | --- | --- |
| `proxy_pass` | ✅ | Bare hosts gain `http://`; a trailing URI slash is dropped with an approximation finding. |
| `fastcgi_pass` | ✅ | Maps directly. |
| `root`, `index`, `try_files` | ✅ | Preserve location overrides. |
| `alias` | ⚠️ | Maps to `root`; NGINX prefix-stripping semantics differ. |
| `return` | ✅ | Status and redirect preserved; response body text is not. |
| `rewrite` | ✅ | Pattern, replacement, and recognized flags are preserved. |
| `limit_except` | ⚠️ | Only the narrow denial form maps to `match.methods`; excluded requests may become a 404 rather than NGINX's 403. |
| `add_header` | ⚠️ | Static `NAME VALUE always;` maps to response-header/CORS policy. Missing `always` or variable values are blocking. |
| `if` | ❌ | Never guessed or silently converted. |
| `include` | ⚠️ | Expanded at the location include point through the bounded resolver. |

`limit_except METHODS { deny all; }` and the equivalent `{ return 403; }`
map to `match.methods` only when the block contains no other behavior. Jul makes
an excluded method fail route matching instead of returning NGINX's explicit
403, so the result remains approximate.

Static `add_header ... always` forms map to ordinary ordered response-header
operations. Static `Access-Control-*` forms can build Jul's CORS block. Jul does
not translate a non-`always` header because that would widen its appearance on
error responses. Variable-derived header values, conditional CORS, invalid
max-age, and wildcard-origin-plus-credentials remain blocking.

### `upstream` block

| Directive | Status | Notes |
| --- | --- | --- |
| `server` | ✅ | Address and weight preserved; `down` omits the member with a finding. |
| `least_conn` | ✅ | Maps to `least_conn`. |
| `ip_hash`, `hash`, `random` | ⚠️ | Falls back to round robin with review guidance. |
| `keepalive`, `keepalive_timeout`, `keepalive_requests`, `zone` | ignored | Connection-pool/process tuning. |
| `include` | ⚠️ | Expanded in upstream context through the bounded resolver. |

### Location modifiers

| Modifier | Jul `match.type` | Notes |
| --- | --- | --- |
| none | `prefix` | Direct. |
| `=` | `exact` | Direct. |
| `^~` | `prefix` | NGINX priority nuance is approximate. |
| `~` | `regex` | Direct regex form. |
| `~*` | `regex` | Case-insensitivity is not preserved. |

### realip (`set_real_ip_from` / `real_ip_header`)

NGINX realip and Jul's
[`[servers.client_address]`](configuration.md#client-address-and-trusted-proxies)
express a trusted-proxy boundary.

| NGINX | Jul |
| --- | --- |
| `set_real_ip_from <cidr\|address>` | appended to `client_address.trusted_proxies` |
| `real_ip_header X-Forwarded-For` | `forwarded_headers = ["x-forwarded-for"]` |
| `real_ip_header Forwarded` | `forwarded_headers = ["forwarded"]` |
| `real_ip_header X-Real-IP` | blocking; a single unchained value is unsupported |
| `real_ip_header proxy_protocol` | blocking |
| `real_ip_recursive on` | Jul already evaluates right to left |
| `real_ip_recursive off` | blocking |

An unsupported form emits no trust policy. `set_real_ip_from` without an
explicit supported header is blocking because NGINX defaults to `X-Real-IP`.
Jul scopes the policy to the listen address, so virtual hosts sharing a listener
must agree. A source with no realip directives never gains trust.

Jul rebuilds outbound `X-Forwarded-For` as canonical client plus direct peer.
For one proxy this matches NGINX; for longer chains intermediate trusted hops are
intentionally dropped. See [forwarded headers to the
backend](core-http.md#forwarded-headers-to-the-backend).

## Known limitations

1. **Traversal is explicit, not automatic.** A default import remains
   single-file. Complete multi-file migration requires `--follow-includes` and a
   root that contains every required source. NGINX deployments that intentionally
   include files outside one root must first be staged into a bounded assessment
   tree; there is no unsafe host-root bypass.
2. **No network includes or arbitrary filesystem crawl.** Only explicit files
   and globs are followed.
3. **`stream` and `mail` are not translated.**
4. **`map`, `geo`, and `split_clients` require manual design.**
5. **Complex `if` and unsupported rewrite control flow require manual design.**
6. **Per-virtual-host realip policies cannot be represented on one listener.**
7. **Named locations such as `@fallback` are not translated.**
8. **One listen address per Jul server.** Extra NGINX listens are approximate.
9. **Trailing-slash `proxy_pass`, `alias`, and server-level `return` have
   documented semantic differences.**
10. **Source formatting/comments are not preserved.** Provenance belongs to the
    report, not generated TOML.

## Benchmarks

Run:

```bash
go test -tags importer -bench=. ./internal/migrate/nginx/
```

The established single-file parse/translate benchmarks remain the baseline.
Include traversal adds bounded filesystem reads and one parse per source; it
performs no network work. Resource limits prevent a representative tree from
turning into unbounded memory, file-descriptor, or parser work.

## Threat note

The importer is a migration-time tool, but it processes untrusted files.

1. Parser panics are recovered and converted into errors.
2. Include reads are root-confined lexically and after symlink evaluation.
3. Cycles and file/depth/byte/glob expansion are bounded.
4. Included files must be regular files; no arbitrary device/directory read is
   accepted.
5. Assessment summaries redact include arguments, Authorization/cookie/token
   headers, URL credentials, key/credential paths, variables, maps, Lua, and
   snippets.
6. Generated TOML is separate from the redacted report and must be reviewed for
   source values intentionally preserved by translation.
7. Paths copied into generated configuration are not proof that deployment-time
   filesystem access is safe.
8. A followed incomplete tree or invalid Jul candidate is never written.
9. The `importer` build tag keeps `gonginx` out of the lean default binary; use
   the tagged binary on a trusted migration host.

## Runnable example

`examples/migrate/nginx.conf` is the existing representative root fixture.
Import it with:

```bash
go run -tags importer ./cmd/jul import nginx -o jul.toml examples/migrate/nginx.conf
```

For a multi-file estate, place the root and included files below one directory
and add `--follow-includes --root <directory>`. Use `--json` or `--assess` to
inspect evidence before writing a candidate.

## GA status

| Criterion | Status | Evidence |
| --- | --- | --- |
| Conformance/behavior matrix | ✅ | Matrix above and schema-v2 result taxonomy. |
| Published benchmark baseline | ✅ | Existing parse/translate benchmarks. |
| Known limitations | ✅ | Explicit list above. |
| Versioned contract | ✅ | Assessment schema version 2. |
| Soak evidence | ✅ | [Soak evidence](soak-evidence.md#2026-07-06--phase-2b-soak-preparation-local-windows-5-min-smoke--validation-scripts). |
| Runnable example and docs | ✅ | Example, schema, sample report, and this guide. |
| Security/threat model | ✅ | Root/symlink/bounds/redaction note above. |
| Fuzzing | ✅ | `FuzzTranslate` covers parse, translate, and marshal round trip. |
| Operable surface | ✅ | `jul import nginx --help` and deterministic human/JSON output. |
