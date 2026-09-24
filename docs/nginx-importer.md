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
| `stream` | ⚠️ | Bounded subset translated (#426); see [`stream` block](#stream-block). |
| `mail` | ❌ | No mail translation today. |
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
| `ssl_verify_client` + `ssl_client_certificate` | ✅ (bounded) / ❌ | See [mTLS](#mtls-ssl_verify_client-and-ssl_client_certificate-366) below. |
| `ssl_crl` | ✅ | Maps to `client_auth.crl_file`, only when the bounded mTLS pairing above resolves. |
| `ssl_verify_depth`, `ssl_trusted_certificate` | ignored | No corresponding Jul knob (chain-verification depth is not independently configurable; OCSP stapling is ACME-managed). |
| `listen ... http2` (no `ssl`) | ✅ | Cleartext HTTP/2 (h2c) has no ALPN to negotiate it, so it maps directly to `h2c = true` - required for `grpc_pass`'s `grpc://` scheme to serve real gRPC traffic. Combined with `ssl`, `http2` stays approximated: a TLS listener already negotiates HTTP/2 via ALPN automatically, so there is no distinct Jul knob to set. |
| `return` | ⚠️ | Synthesizes `/`; NGINX server-level precedence differs. |
| `set_real_ip_from`, `real_ip_header`, `real_ip_recursive` | ⚠️ | See [realip](#realip-set_real_ip_from--real_ip_header). |
| `if`, server-level `rewrite` | ❌ | Reported with provenance and guidance. |
| `include` | ⚠️ | Expanded in server context only through the bounded resolver. |

### `location` block

| Directive | Status | Notes |
| --- | --- | --- |
| `proxy_pass` | ✅ (⚠️ with a URI) | Bare hosts gain `http://`; a trailing URI slash is dropped with an approximation finding. Any retained path is not a location-prefix replacement: Jul's proxy (`net/http/httputil.ProxyRequest.SetURL`) always *prepends* it to the client's full incoming request path rather than stripping the matched location prefix first, so the backend-visible path differs from nginx's whenever the location path does not exactly match the request. See `proxy-pass-uri-runtime` in the [migration corpus](nginx-migration-corpus.md) for a real end-to-end proof. |
| `fastcgi_pass` | ✅ | Maps directly. |
| `fastcgi_param` | ✅ (literal) / ❌ (variable) | A literal name/value pair accumulates into `fastcgi_params`, a static map. A variable-derived value (`$document_root` etc.) is blocking - Jul's `fastcgi_params` has no per-request substitution engine. |
| `uwsgi_pass` | ✅ | Maps directly to `uwsgi_pass`, the same bare-address pattern as `fastcgi_pass`. |
| `uwsgi_param` | ❌ | Jul has no per-parameter uWSGI configuration equivalent; always blocking. See [gRPC, FastCGI, and uWSGI gateways](#grpc-fastcgi-and-uwsgi-gateways-367) below. |
| `grpc_pass` | ✅ (bounded) / ❌ | See [gRPC, FastCGI, and uWSGI gateways](#grpc-fastcgi-and-uwsgi-gateways-367) below. |
| `root`, `index`, `try_files` | ✅ | Preserve location overrides. |
| `alias` | ⚠️ | Maps to `root`; NGINX prefix-stripping semantics differ. |
| `return` | ✅ | Status and redirect preserved; response body text is not. |
| `rewrite` | ✅ | Pattern, replacement, and recognized flags are preserved. |
| `limit_except` | ⚠️ | Only the narrow denial form maps to `match.methods`; excluded requests may become a 404 rather than NGINX's 403. |
| `proxy_connect_timeout`, `proxy_read_timeout`, `proxy_send_timeout` | ✅ | Direct duration mappings, verified end to end (`timeout-runtime`): a stalling backend is actually cut off at the configured bound and Jul returns 504. |
| `proxy_next_upstream_tries` | ✅ (bound \u2265 2) / \u274c (0 or 1) | An explicit bound of 2 or more maps to `resilience.retry_attempts = tries - 1`. nginx's `0` (unlimited) and `1` (no retry) cannot be distinguished from Jul's `retry_attempts = 0`, which means "inherit the pool default", so both stay blocking rather than silently becoming an inherited value the source never asked for. |
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
| `server ... max_fails=N fail_timeout=T` | ✅ (consistent) / ⚠️ (disagreeing) | See [passive health (`max_fails`/`fail_timeout`)](#passive-health-max_fails-fail_timeout) below. |
| `least_conn` | ✅ | Maps to `least_conn`. |
| `ip_hash`, `hash`, `random` | ⚠️ | Falls back to round robin with review guidance. |
| `keepalive`, `keepalive_timeout`, `keepalive_requests`, `zone` | ignored | Connection-pool/process tuning. |
| `include` | ⚠️ | Expanded in upstream context through the bounded resolver. |

An `upstream` block nested inside `stream {}` reuses this exact same translation.

### Passive health (`max_fails`/`fail_timeout`)

nginx's `max_fails`/`fail_timeout` are declared **per backend**; Jul's circuit
breaker ([`[upstreams.resilience]`](configuration.md#resilience)) is
**upstream-wide** — one threshold and one open duration for the whole pool.
Translation is only lossless when every backend that declares these params in
the same `upstream` block agrees on the same value:

| NGINX | Jul |
| --- | --- |
| Every `server` line agreeing on the same `max_fails=N` | `upstreams[].resilience.max_fails = N` |
| Every `server` line agreeing on the same `fail_timeout=T` | `upstreams[].resilience.fail_timeout = T` |
| Backends disagreeing on `max_fails` (respectively `fail_timeout`) | ⚠️ that one knob is left at Jul's own default; a note identifies the conflict |
| A malformed value (negative `max_fails`, unparseable `fail_timeout`) | ❌ |

The two knobs are resolved independently — one can translate while the other
falls back to Jul's default. Only a genuine *connect* failure (refused
connection, reset, read/write failure, or timeout) counts against the circuit;
an in-band 5xx from a live backend does not, matching nginx's own default
`proxy_next_upstream` scope.

### `stream` block

Bounded first-tranche translation (#426) for the intersection nginx's stream
module and Jul's `[[stream]]` L4 proxy can both represent honestly. This is not
stream-module parity — see [known limitations](#known-limitations) for the
residual boundary.

| Directive | Status | Notes |
| --- | --- | --- |
| `server` | ✅ | One `[[stream]]` entry, or merged into one when `server_name`-distinguished siblings share a listen address (see SNI below). |
| `upstream` | ✅ | Reuses the `http` upstream translation verbatim. |
| `listen` (address, `udp`) | ✅ | TCP is the default; `udp` selects Jul's datagram/session model. |
| `listen ... ssl` | ❌ | Stream TLS termination is not representable; Jul's stream listener only ever passes TLS through via SNI preread (`ssl_preread`), never terminates it. |
| `listen ... proxy_protocol` (inbound) | ❌ | Jul requires an explicit `trusted_proxies` allow-list whenever it ingests a PROXY header; nginx's stream module has no equivalent trusted-source directive to supply one, so this must be added to `[[stream]]` by hand. |
| `proxy_protocol on\|off` (outbound, `ngx_stream_proxy_module`) | ✅ (tcp) / ❌ (udp) | Maps to `proxy_protocol = "out"`. No trust-boundary concern — Jul is asserting its own peer address to its own backend. Jul only supports it for tcp streams. |
| `proxy_pass` | ✅ | A named upstream or literal `host:port`. Variable-derived (`$...`) targets are blocking. |
| `proxy_timeout`, `proxy_connect_timeout` | ✅ | Map to `idle_timeout`/`connect_timeout`. Bare digits mean seconds; `ms`/`s`/`m`/`h` suffixes are supported. nginx's `d`/`w`/`M`/`y` units are not. |
| `server_name` + `ssl_preread on` | ✅ | See SNI below. |
| `map` (including `$ssl_preread_server_name` routing) | ❌ | Arbitrary variable maps are not representable in the bounded Jul stream model; this is a deliberate non-goal, not an oversight. |
| Lua (`ngx_stream_lua_module`), njs (`ngx_stream_js_module`), any other third-party stream module, unrecognized `listen`/directive options | ❌ | Falls through to an explicit blocking finding by default; never silently accepted. See `stream-extensibility-boundaries` in the [migration corpus](nginx-migration-corpus.md). |

**Bounded SNI routing.** nginx can route a stream listener by inspecting the
TLS ClientHello (`ssl_preread on;`) and dispatching to one of several `server`
blocks sharing a `listen` address, each distinguished by `server_name`. Jul
represents that as a single `[[stream]]` entry's `sni_routes` map (a
separate `[[stream]]` per `server_name` would collide as a duplicate listener).
The importer merges such a group only when every member sets `ssl_preread on`
and at most one lacks a `server_name` (used as the fallback `proxy_pass`); a
mixed group, a missing `ssl_preread`, a duplicate `server_name`, or more than
one fallback candidate blocks the whole group rather than guessing.

**UDP.** A `udp` listener documents Jul's actual session model: one bounded,
client-address-keyed session per source, a configurable idle timeout, and a
session-count cap (`max_udp_sessions`). This is not QUIC Connection-ID-aware
load balancing, and the importer never implies otherwise.

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
| `real_ip_header proxy_protocol` | ✅ (bounded) / ❌ | See [HTTP PROXY-protocol identity](#http-proxy-protocol-identity-426) below. |
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

### HTTP PROXY-protocol identity (#426)

`real_ip_header proxy_protocol;` asserts client identity from the HAProxy PROXY
protocol rather than a forwarded header. Jul's HTTP listener supports ingesting
it (`servers[].proxy_protocol = "in"`), but only ever promotes it straight to
the canonical client address — there is no way to parse it without trusting it.
Translating it therefore requires all three source elements together, in the
same server block:

```nginx
listen 443 proxy_protocol;
set_real_ip_from 10.0.0.0/8;
real_ip_header proxy_protocol;
```

| NGINX | Jul |
| --- | --- |
| `listen ... proxy_protocol` + `real_ip_header proxy_protocol` + `set_real_ip_from` (all three) | `proxy_protocol = "in"` + `client_address.trusted_proxies` |
| Any one of the three missing | blocking |

An untrusted direct peer can never acquire trusted client identity this way:
Jul's HTTP listener never emits a broad/default trust range, and validation
rejects `proxy_protocol = "in"` without a non-empty `trusted_proxies`.

### mTLS: ssl_verify_client and ssl_client_certificate (#366)

nginx enables mutual TLS through two directives that only take effect
together: `ssl_verify_client on|optional;` alone changes nothing without a
trusted-CA bundle, and `ssl_client_certificate` alone is inert without
`ssl_verify_client`. Jul's `client_auth` mirrors that pairing — its `mode`
field always requires a non-empty `ca_file` whenever it is not `"none"` — so
translation resolves both directives together, in the same server block:

```nginx
ssl_verify_client on;
ssl_client_certificate /etc/ssl/ca.pem;
ssl_crl /etc/ssl/ca.crl;
```

| NGINX | Jul |
| --- | --- |
| `ssl_verify_client on;` + non-empty `ssl_client_certificate` | `client_auth.mode = "require"` + `ca_file` |
| `ssl_verify_client optional;` + non-empty `ssl_client_certificate` | `client_auth.mode = "request"` + `ca_file` |
| `ssl_crl` (with either mode above) | `client_auth.crl_file` |
| Either directive present without the other | blocking |
| `ssl_verify_client optional_no_ca;` | blocking |
| `ssl_verify_client off;` | ignored — matches Jul's default of no client-certificate verification |

`optional_no_ca` accepts a client certificate without validating it against
any CA at all. Jul's `client_auth` has no equivalent: once enabled, it always
validates against `ca_file`, so this form is not representable and is left
blocking rather than silently upgraded to `"request"` (which *would*
validate) or silently dropped (which would validate nothing nginx asked for
either — both are behavior changes, not migrations). See `mtls-runtime` in
the [migration corpus](nginx-migration-corpus.md) for a real end-to-end proof
of the accept/reject boundary this produces.

### gRPC, FastCGI, and uWSGI gateways (#367)

**gRPC.** nginx's `grpc_pass` proxies native gRPC over HTTP/2, selected by an
explicit scheme: `grpc://` for cleartext (h2c) or `grpcs://` for HTTP/2 with
TLS. Jul's native gRPC/HTTP-2 passthrough (`internal/handler/grpcproxy.go`,
requires a `grpc`-tagged build) reuses the ordinary `proxy_pass` field for its
target and a separate `grpc = true` flag to turn the location into a
passthrough that preserves HTTP/2 framing and trailers (`grpc-status` among
them) end to end, rather than an ordinary buffered HTTP reverse proxy.

```nginx
listen 8080 http2;
location / {
  grpc_pass grpc://grpc_backend;
}
```

| NGINX | Jul |
| --- | --- |
| `grpc_pass grpc://target;` | `proxy_pass = "http://target"` + `grpc = true` (h2c) |
| `grpc_pass grpcs://target;` | `proxy_pass = "https://target"` + `grpc = true` (HTTP/2+TLS) |
| `grpc_pass name;` (bare, no scheme) | Kept as-is, matching nginx's own `grpc://` default; resolves as a named `[[upstreams]]` pool exactly like a schemeless `proxy_pass`. |
| `grpc_pass grpc://unix:...;` (direct Unix target) | blocking — create a named `[[upstreams]]` entry with a `unix:` server instead, exactly as a direct-Unix `proxy_pass` requires. |
| `grpc_pass grpc://$backend;` (variable-derived) | blocking |
| Any other `grpc_pass` scheme | blocking |
| `listen ... http2;` (no `ssl`) | `servers[].h2c = true` — required for a `grpc://` target to actually be reachable; a TLS listener needs no equivalent flag (ALPN negotiates HTTP/2 automatically). |

A client-supplied gRPC call and its trailers survive Jul's passthrough
unmodified; see `grpc-gateway-runtime` in the
[migration corpus](nginx-migration-corpus.md) for a real end-to-end proof
against an actual `google.golang.org/grpc` client and server.

**FastCGI.** `fastcgi_pass` already mapped directly; `fastcgi_param` now
accumulates literal name/value pairs into `fastcgi_params`, a static map Jul's
real FastCGI client (`github.com/yookoala/gofast`) merges into the actual
FastCGI PARAMS record it sends the backend, overriding anything auto-derived
(`SCRIPT_NAME`, `DOCUMENT_ROOT`/`SCRIPT_FILENAME` when `root` is set). A
variable-derived value (`$document_root$fastcgi_script_name`, the common
real-world form) is not representable — Jul's map has no per-request
substitution — and stays blocking rather than emitting a static value the
source config never actually specified. See `fastcgi-gateway-runtime` in the
[migration corpus](nginx-migration-corpus.md) for a real end-to-end proof
against Go's own `net/http/fcgi` standard-library responder.

**uWSGI.** `uwsgi_pass` maps directly to Jul's `uwsgi_pass` location field,
the same bare-address pattern as `fastcgi_pass`. `uwsgi_param` has no Jul
equivalent at all — there is no per-parameter uWSGI configuration surface to
target — so it always stays an explicit blocking finding rather than being
silently dropped. Param-style propagation for a migrated uWSGI backend still
works through the same client-header path every uWSGI request already
carries (an inbound `X-Custom-Header` becomes the uwsgi var
`HTTP_X_CUSTOM_HEADER`, the CGI-standard convention FastCGI also uses); see
`uwsgi-gateway-runtime` in the [migration corpus](nginx-migration-corpus.md)
for a real end-to-end proof against a real uWSGI-protocol responder.

### `proxy_cache` and `proxy_cache_path` (#365)

nginx supports any number of independently named, independently sized cache
zones (`proxy_cache_path ... keys_zone=name:size`), selected per location
(`proxy_cache name;`). Jul's response cache
([`[cache]`](cache.md#configuration)) is a single process-wide store — there
is no per-location zone concept. Translation is therefore bounded to the case
where exactly one zone is declared and every `proxy_cache` reference in the
whole file names that same zone:

| NGINX | Jul |
| --- | --- |
| One `proxy_cache_path <path> ... keys_zone=name:size [max_size=size] ...;`, referenced consistently by every `proxy_cache name;` | `[cache].enabled = true`, `disk_path`, `disk_max_size` |
| `proxy_cache name;` (the resolved zone) | `servers[].locations[].cache = true` |
| `proxy_cache off;` | ignored — already Jul's default |
| `proxy_cache_valid [<codes>] <time>;` where `<codes>` is empty or a subset of nginx's own default cacheable set (`200`, `301`, `302`) | `[cache].default_ttl` (approximated: Jul only applies it when the upstream sends no explicit `Cache-Control`/`Expires`, while nginx's own default behavior is comparable but not identical depending on `proxy_ignore_headers`) |
| More than one distinct zone declared, or a `proxy_cache` referencing an undeclared zone | ❌ — a genuine architectural mismatch, not a scope gap; every offending reference is reported |
| `proxy_cache_bypass`, `proxy_no_cache`, `proxy_cache_key`, `proxy_ignore_headers` | ❌ — expression-driven or would silently change Jul's always-honored `Cache-Control`/`Expires` behavior |
| `proxy_cache_min_uses`, `proxy_cache_lock*`, `proxy_cache_background_update` | ignored — no Jul equivalent knob |
| `proxy_cache_revalidate`, `proxy_cache_use_stale` | ⚠️ — Jul's background revalidation/stale-serving is unconditional and cannot be selectively disabled |

The `keys_zone=name:size` size bounds nginx's in-memory *key/metadata* index,
a different quantity than Jul's `memory_max_size` (the actual response-data
cache), so it is deliberately not translated into that field; only `max_size=`
(the disk-tier data cap) maps to `disk_max_size`.

## Known limitations

1. **Traversal is explicit, not automatic.** A default import remains
   single-file. Complete multi-file migration requires `--follow-includes` and a
   root that contains every required source. NGINX deployments that intentionally
   include files outside one root must first be staged into a bounded assessment
   tree; there is no unsafe host-root bypass.
2. **No network includes or arbitrary filesystem crawl.** Only explicit files
   and globs are followed.
3. **`stream` is bounded (#426), `mail` is not translated.** See the [`stream`
   block](#stream-block) table for exactly which stream forms are supported,
   approximate, or blocking.
4. **`map`, `geo`, and `split_clients` require manual design**, including a
   `stream`-level `map` used for `ssl_preread`-driven routing — use bounded
   `server_name` + `ssl_preread on` instead (see above).
5. **Complex `if` and unsupported rewrite control flow require manual design.**
6. **Per-virtual-host realip policies cannot be represented on one listener.**
7. **Named locations such as `@fallback` are not translated.**
8. **One listen address per Jul server.** Extra NGINX listens are approximate.
9. **`proxy_pass` with a URI does not replace the matched location prefix.**
   nginx strips the matched location prefix and substitutes the `proxy_pass`
   path; Jul always prepends the `proxy_pass` path to the client's full
   incoming request path instead. A trailing URI slash on `proxy_pass` and a
   server-level `return` also have documented semantic differences.
10. **Source formatting/comments are not preserved.** Provenance belongs to the
    report, not generated TOML.

## Migration evidence corpus

The importer is continuously exercised against the versioned, sanitized
[NGINX migration compatibility corpus](nginx-migration-corpus.md). The closed
#154 baseline contains 11 repository-owned fixtures across the six minimum
categories, exact schema-v2 assessment projections, canonical candidate
validation, real-Jul loopback scenarios, and a separate digest-pinned NGINX
reference lane.

Two machine-readable files keep the evidence honest:

- `testdata/nginx-corpus/coverage.json` maps every minimum category to concrete
  fixtures and records residual dimensions plus objective revisit triggers;
- `testdata/nginx-corpus/inventory.json` is a deterministic aggregate of fixture
  tiers, categories, result classes/risks/codes, verdicts, and coverage
  dispositions. It is checked in CI and deliberately contains no compatibility
  percentage.

The baseline represents core routing, upstreams, trusted-client identity
(including bounded HTTP/stream PROXY-protocol identity, #426), TLS and other
security controls, cache/compression boundaries, FastCGI, a bounded stream
subset (direct/named-upstream TCP and UDP, outbound PROXY protocol,
server_name+ssl_preread SNI routing) plus blocked stream/mail residuals, and
operational/include behavior. Protocol-heavy or stateful dimensions such as
migration-specific H2/H3, WebSocket, gRPC/uWSGI, L4 real-runtime replay, mTLS,
cache-state, and resolver replay remain explicitly deferred in the coverage
matrix (#365/#366/#367 own that evidence). Existing product-level protocol
tests remain authoritative for Jul runtime capability; they do not turn an
unimported NGINX source into an
equivalence claim.

Fixtures can be supported, approximated, ignored, blocking, or equivalent only
for their asserted dimensions. Corpus admission rejects proprietary/user source,
private-key material, unsafe request headers, non-loopback replay, symlinks, and
unbounded files. See the corpus guide and the
[closure record](audit/old/2026-08-31-nginx-migration-corpus-closure.md) for the
category matrix, image isolation, protocol decisions, commands, and rollback
boundary.

### Corpus-discovered selected differences

The corpus currently records two reviewed differences rather than hiding them
behind a global compatibility claim:

- NGINX expands a local `return 30x /path` target to an absolute `Location` by
  default, while Jul preserves the relative target. The importer reports
  `NGX_LOCATION_RETURN_ABSOLUTE_REDIRECT` as `approximated`.
- A narrow `limit_except` denial returns `403` in NGINX. Jul translates the
  allowed-method set into route matching, so an excluded method can resolve to
  `404`; the corpus records `NGX_LOCATION_LIMIT_EXCEPT` as the expected
  difference.

See the [migration corpus guide](nginx-migration-corpus.md) for the exact
asserted dimensions and the machine-checked inventory.

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

## Maturity and delivery

This guide documents the current `main` surface, which is broader than
the released base importer. The two contracts are deliberately separate:

### Base importer — Y1-09 (`GA` / `soaked`)

The released GA record covers the single-file conversion contract and the
evidence that existed in the released line. The current support matrix above
also contains later additive mappings; those do **not** retroactively widen
the released GA contract.

| Criterion | Released base evidence |
| --- | --- |
| Translation behavior | Deterministic single-file parse/translate path and its released golden output. |
| Performance | Published parse and translate benchmark baselines. |
| Limitations | Explicit unsupported-directive and semantic-difference list; no full NGINX emulation claim. |
| Compatibility | The documented conversion CLI and generated Jul configuration behavior are governed by [compatibility.md](compatibility.md). |
| Soak / validation | [Released importer validation evidence](soak-evidence.md#2026-07-06--phase-2b-soak-preparation-local-windows-5-min-smoke--validation-scripts). |
| Runnable example | `examples/migrate/nginx.conf` through ordinary conversion mode. |
| Security | Parser failure containment, secret-safe diagnostics, and use on a trusted migration host. |
| Fuzzing | `FuzzTranslate` covers parse, translate, and marshal round trip. |
| Operable surface | `jul import nginx -o <file> <nginx.conf>`. |

### Assessment, provenance, and includes — MIG-ASSESS (`Beta` / `merged`)

Schema-v2 human/JSON assessment, stable findings and guidance, source spans,
target mappings, and bounded root-confined include traversal are merged on
current `main`. They are not contained in the older released GA record and
have not completed a separate stable-release and long-running-soak promotion.
Their machine contract and operating boundary are documented in
[nginx-assessment.md](nginx-assessment.md) and tracked explicitly in
[status.md](status.md).

## Unix HTTP upstream migration (#407)

A named NGINX upstream containing `server unix:/path.sock;` is preserved as a
Jul named upstream and can be referenced by `proxy_pass http://name;`. NGINX's
separate direct-Unix `proxy_pass` grammar is not copied into Jul: the importer
emits a source-located manual finding and omits that location rather than
producing malformed Jul configuration. Map it to a deterministic operator-chosen
`[[upstreams]]` name with `servers = ["unix:/path.sock"]`, then reference
`proxy_pass = "http://name"`.
