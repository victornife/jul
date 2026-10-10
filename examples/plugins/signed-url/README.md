# Signed media and download URLs

This reviewed reference is `jul-abi/v1` middleware, not built-in authentication
or an nginx `secure_link` wire-format implementation. It grants GET and HEAD
access to one resource until an authenticated Unix expiry. Other methods and
all validation failures receive **403**, `Cache-Control: no-store` and the same
`Forbidden` body. Logs contain only fixed denial reasons; expired signatures
have a distinct `expired` reason after signature verification.

## ABI selection and v2 compatibility

Authorization needs only the request method, URI and configuration, so this
guest uses `jul-abi/v1`, the supported default for request-only plugins. Jul's
v2-capable runtime runs it alongside `jul-abi/v2` plugins in the same chain;
the compiled-guest tests cover both plugin orders with `v2-status-header`.
Valid GET/HEAD and Range requests reach the handler and its v2 response hook.
Denied requests stop before the handler and response hook, including when the
v2 plugin subscribed first. Authorization does not buffer the media response.

Keep `abi = "jul-abi/v1"` for `signed-url.wasm`. Changing that field to
`jul-abi/v2` rejects this module at load time: the SDK imports and ABI marker
must match the declaration. A separate v2 guest is warranted only if a new
policy needs the actual handler response; see the [ABI selection guide](../../../docs/abi.md#which-abi-should-i-use).
The `jul-signed-url/v1` MAC framing identifies the signing protocol independently
of the guest ABI.

## Build and run

Requires Go 1.26.9+ and Jul with `wasmplugins`. From the repository root:

```bash
go build -tags wasmplugins -o /tmp/jul ./cmd/jul
cd examples/plugins/signed-url
bash build.sh
# Generate once and share securely with the trusted link issuer. Do not commit it.
export SIGNED_URL_KEY="$(python3 -c 'import secrets; print(secrets.token_urlsafe(32))')"
/tmp/jul check -config jul.toml
/tmp/jul serve -config jul.toml
```

In a second shell with the same `SIGNED_URL_KEY`, from `examples/plugins`:

```bash
link="$(go run ./signed-url/sign -path /media/demo.txt -kid current -ttl 300)"
curl -i "http://127.0.0.1:8080$link"
curl -I "http://127.0.0.1:8080$link"
curl -i http://127.0.0.1:8080/media/demo.txt # 403
```

Windows: run `./build.ps1`, use `$env:SIGNED_URL_KEY` and a `jul.exe` built with
`-tags wasmplugins`; run the same Go signer. Both per-example build scripts and
the parent `build.sh DIR all` / `build.ps1 -Out DIR -All` compile this guest.
`jul check` validates Jul configuration and module-path existence; it does not
instantiate the guest or validate opaque plugin parameters. Invalid guest
parameters fail closed with 403 at invocation. Test a signed and an unsigned
request before exposing a deployment.

## Configuration and key rotation

Jul passes a flat string map to the guest. Values support `${env:NAME}` and
`${file:/path}` through Jul's existing secret resolution; files should contain
only the unpadded base64url key (Jul trims trailing line endings). Missing secret
references reject the Jul candidate. Secret source changes require a reload.

| Parameter | Default / bounds |
| --- | --- |
| `key.<kid>` | Required, 1–8 keys; each encodes 32–64 random bytes as unpadded base64url |
| key ID | 1–64 ASCII letters/digits/underscore/hyphen |
| `exp_param` | `exp`; 1–32 ASCII letters/digits/underscore/hyphen |
| `sig_param` | `sig`; same bound |
| `kid_param` | `kid`; same bound; all three names must differ |
| `clock_skew_seconds` | `0`; canonical decimal integer, 0–300 |
| `max_ttl_seconds` | `86400`; canonical decimal integer, 1–604800 |

Example rotation:

```toml
config = { "key.current" = "${env:SIGNED_URL_KEY}", "key.previous" = "${file:/run/secrets/signed-url-previous}", clock_skew_seconds = "5" }
```

First publish the new verification key alongside the old one, then switch the
issuer's `kid` and secret. Remove the old key only after outstanding links plus
skew expire. Removing a key invalidates links on the next published generation;
requests already admitted may complete. Existing generation-retirement semantics
apply; this is not a mid-stream revocation facility. Key IDs are public; keys
stay in the trusted issuer and Jul. Use distinct keys per origin/security domain
because hostname is not part of the signature. No client-IP binding is provided.

## Exact signing protocol

The signature is unpadded base64url of HMAC-SHA256 over these UTF-8 bytes,
with **no trailing newline**:

```text
jul-signed-url/v1\nGET\n<canonical escaped path>\n<canonical decimal exp>\n<kid>
```

GET links also authorize HEAD, allowing media metadata probes. `exp` is a
positive signed-64-bit Unix timestamp in seconds. Expiry is inclusive:
`now <= exp + skew` is accepted; a future expiry must be within `max_ttl_seconds`
of the verifier's current clock. Skew only extends the expiry boundary, not the
future lifetime cap. Jul supplies actual host wall time through WASI; keep the
issuer/verifier clocks synchronized. The cap restricts remaining lifetime, not
original issuance age: there is no signed issue time.

The URL must have **exactly** the three configured query parameters, once each.
Malformed query encodings, extra unsigned parameters, absent/unknown `kid`,
noncanonical expiry or signature encodings fail. The signer command uses the
three default names; customized issuers can use the shared `policy.Parse` and
`Policy.Sign` functions with their configured names.

Paths must be absolute origin-form paths using Go's canonical `URL.EscapedPath`
encoding. `/media/a%20b.mp4` is supported. Dot segments, repeated slashes,
encoded separators, backslashes, fragments, percent signs in the decoded path
and control characters are rejected. These limits avoid authorizing one path
that a downstream path cleaner or decoder maps to another. Sign the public
path before any Jul rewrite; keep it mapped to one resource and use a direct
static route or an upstream with matching path semantics. Do not attach a
rewrite or redirect to this recipe: follow-up URLs need their own signatures.

`policy.MAC` is the shared signer primitive. For another language, use the exact
framing above, decode the secret from base64url before HMAC, and encode the 32-byte
MAC without padding. Comparison in the verifier uses `hmac.Equal`.

## Deployment boundaries

- Attach the middleware to every protected route. It executes before route auth,
  rate limiting and cache access, including Jul cache hits; do not serve the same
  content through an unprotected alias. The sample attaches it at server scope.
- Valid links are bearer credentials and can be replayed/shared until expiry.
  This is not DRM or single-use authorization. Use HTTPS for public deployments,
  `Referrer-Policy: no-referrer` on the embedding page and avoid logging/sharing
  full signed URLs. Jul access logs may include the query; fixed plugin logs do
  not change access-log policy. Review access-log retention/redaction separately.
- A browser or external CDN can serve a previously cached success without
  consulting the plugin. The sample uses `private, no-store`; set any external
  caching policy within the intended authorization lifetime.
- Range requests retain normal static/proxy behavior; the signature binds the
  resource, not an individual byte range or response body. Media segments need
  individual signed URLs. Packaging, manifest rewriting and DRM are external.
- No `fetch`, filesystem, environment or KV capability is granted to the guest;
  keys arrive through config. `max_instances` bounds instance memory (#506).
  Review/pin locally built module bytes; digest pinning is content identity,
  not publisher signatures or provenance (#439 remains deferred).

## Verification

```bash
# From examples/plugins:
go test -cover ./signed-url/policy
go test -fuzz FuzzValidate -fuzztime 5s ./signed-url/policy
# From the repository root:
go test -tags wasmplugins -run TestSignedURLCompiledGuest ./internal/plugins
```

CI builds the actual guest from source, vets the WASI packages, measures the
native policy coverage and runs it through Jul's plugin manager. The runtime
matrix includes valid/expired links, GET/HEAD, tampering, unknown/rotated keys,
skew and malformed config; the native matrix adds bounds, canonicalization,
duplicate/extra parameters and overflow cases.
