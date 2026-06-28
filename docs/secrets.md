# Secrets references

Sensitive configuration values — admin tokens, JWT signing keys, ACME EAB keys,
Consul/Kubernetes tokens, upstream credentials — should not be literals in your
`jul.toml`, where they get committed, copied, and logged. Jul.IA lets any string
field carry a **reference** instead: `${env:NAME}` pulls a value from an
environment variable and `${file:/path}` reads it from a file. References are
resolved at serve time, and the resolved values are automatically **masked from
logs**. `jul lint` flags the most sensitive fields when they still hold a
literal.

This is **SEC-1**, in **core** — no build tag — and uses only the standard
library.

> **Maturity:** Beta (see [ADR 0003](adr/0003-maturity-and-ga.md)). `${env:}` and
> `${file:}` cover the local secret sources today; a secret-manager backend
> (Vault/KMS) is future work.

## Contents

- [Reference syntax](#reference-syntax)
- [Where references work](#where-references-work)
- [Resolution](#resolution)
- [Log redaction](#log-redaction)
- [Linting literal secrets](#linting-literal-secrets)
- [The Console](#the-console)
- [Examples](#examples)
- [Operational notes](#operational-notes)
- [Limits](#limits)
- [GA status](#ga-status)

## Reference syntax

A reference is `${scheme:body}` embedded anywhere in a string value:

| Reference | Resolves to |
| --------- | ----------- |
| `${env:NAME}` | The value of environment variable `NAME` |
| `${file:/path}` | The contents of the file at `/path` (one trailing newline trimmed) |
| `${secret:/path}` | Same as `${file:}` today — the forward-compatible spelling for a future secret-manager backend |

A value may combine literal text with one or more references
(`"Bearer ${env:API_TOKEN}"`), and a single field may contain several. An
**unknown scheme** (e.g. `${vault:…}` or a typo like `${evn:…}`) is a hard error
at resolution, so a mistake fails loudly instead of leaving a literal `${…}` in a
credential field.

## Where references work

References are resolved in **every string field of the configuration**, walked
recursively (structs, pointers, slices, arrays, and string-keyed maps). That
includes — but is not limited to:

- `[admin].token`
- `[[servers]]` TLS material paths and any header values
- JWT / forward-auth secrets and keys under auth
- ACME external-account-binding keys
- upstream service-discovery tokens (`consul.token`, the Kubernetes token)
- any other string you put a `${…}` into

## Resolution

Resolution happens **on the serving configuration**, just before the runtime is
built — and again on **every reload**:

1. Each reference is replaced with its resolved value in place.
2. Each resolved value is registered for [log redaction](#log-redaction).
3. If any reference cannot be resolved, startup (or the reload) fails with an
   error that **joins every problem**, so one run surfaces them all — e.g. a
   missing environment variable or an unreadable file.

Crucially, the **on-disk file and the admin/Console representations keep the
unresolved references**: the config loader (`TOMLSource`) returns the raw text,
and only the serving path calls `ExpandSecrets`. Secrets are therefore never
written back to disk or surfaced through the Console — only counted (below).

`${file:}`/`${secret:}` trims a single trailing newline (and surrounding
`\r`/`\n`) so a secret stored one-per-file does not pick up the editor's newline.

## Log redaction

Every value resolved from a reference is added to a process-wide redaction
registry. The logger writes through a wrapper that replaces any occurrence of a
registered secret with a fixed mask, `***`, so a secret that ends up in a log
line — an error string, a debug field — is masked wherever it appears.

Notes on the redactor:

- The registry **only grows**: secrets stay registered across reloads.
- Values shorter than the **redaction floor** (default **4 characters**) are
  deliberately **not** masked, to avoid corrupting unrelated log text with a
  too-common substring (a secret that short is not meaningfully secret). Lower
  the floor with `[global] redact_min_secret_length` (down to `1`) when your
  secrets are shorter than the default, accepting that short values may also mask
  incidental log text; `0` keeps the default.
- Redaction is best-effort defense-in-depth for logs; it is not a substitute for
  keeping secrets out of the config file in the first place (use references).

## Linting literal secrets

`jul lint` warns when the most sensitive fields hold a **literal** value instead
of a reference. It flags (only when the value is non-empty and is **not** already
a `${…}` reference):

- `[admin].token`
- each upstream `discovery.consul.token`
- each upstream Kubernetes discovery token

with a hint to externalize it, e.g.:

```
[admin].token: admin token is a literal value in the config file
  hint: reference a secret instead, e.g. token = "${env:JUL_ADMIN_TOKEN}"
        or "${file:/run/secrets/admin-token}"
```

Run it as part of your pipeline:

```sh
jul lint -config jul.toml
```

## The Console

The Console **Status** and **Security** panels report *Secret references* as
active and show **how many** references the running configuration uses
(`config.CountSecretRefs`). The count is computed from the raw, unexpanded
config — the Console never sees or resolves the secret values themselves.

## Examples

Externalize the admin token and a JWT signing key with environment variables:

```toml
[admin]
enabled = true
listen = "0.0.0.0:9000"
token = "${env:JUL_ADMIN_TOKEN}"
```

Read a Consul ACL token from a mounted secret file:

```toml
[[upstreams]]
name = "api"

  [upstreams.discovery.consul]
  address = "consul.service.consul:8500"
  service = "api"
  token = "${file:/run/secrets/consul-token}"
```

Compose a literal prefix with a reference in a forwarded header:

```toml
  [[servers.locations]]
  match = { type = "prefix", path = "/api" }
  proxy_pass = "http://127.0.0.1:9001"

    [servers.locations.headers]
    Authorization = "Bearer ${env:UPSTREAM_API_TOKEN}"
```

Then provide the values out-of-band:

```sh
export JUL_ADMIN_TOKEN="$(openssl rand -hex 32)"
export UPSTREAM_API_TOKEN="…"
jul serve -config jul.toml
```

## Operational notes

- **Resolved on serve and reload.** Editing the file and reloading re-resolves
  references, so rotating a secret is: update the env var / file, then reload (or
  restart). The new value is masked from logs automatically.
- **Fail loud.** A missing env var, an unreadable file, or an unknown scheme
  fails startup/reload with a joined error listing every unresolved reference —
  there is no silent fallback to an empty or literal value.
- **Secrets stay out of the surfaces.** Disk, admin API, and Console all keep the
  unexpanded references; only the in-memory serving config holds plaintext, and
  logs mask it.
- **On-disk config is written tightly and atomically.** A config saved by the
  admin console (or a history snapshot, or `jul import` output) is created
  `0o600` and written through a same-directory temp file that is renamed into
  place, so a freshly written file is not world-readable and a crash mid-write
  never leaves a truncated config. An existing file's mode is preserved. Prefer
  references over literals regardless — but if a literal must live on disk, this
  keeps it off other local users' eyes. See
  [SECURITY.md](../SECURITY.md#hardening-defaults--recommendations).

## Limits

- **Local sources only.** `${env:}` and `${file:}` (and `${secret:}` as a file
  alias) are supported. A managed secret-manager backend (HashiCorp Vault, cloud
  KMS) and SPIFFE/SVID identity are future work — `${secret:}` reserves the
  spelling.
- **Redaction is substring-based and skips very short values** (below the floor,
  default < 4 chars; tunable via `redact_min_secret_length`), so it
  is defense-in-depth, not a guarantee; keep secrets in references rather than
  relying on masking.
- **Lint covers the highest-risk fields** (`admin.token`, Consul/Kubernetes
  tokens). Other credential fields accept references but are not yet
  lint-flagged when literal.

## GA status

Per [ADR 0003](adr/0003-maturity-and-ga.md), secrets references are **Beta**. The
remaining GA gaps (excluding the post-GA soak test per
[ADR 0005](adr/0005-soak-post-ga-gate.md)) are tracked in the
[status matrix](status.md).

| # | GA criterion | Status |
| --- | --- | --- |
| 1 | Behaviour matrix published | ☐ reference-source matrix to expand |
| 2 | Published benchmark numbers | ☐ resolve-cost note pending |
| 3 | Documented known-limitations | ✅ [Limits](#limits) |
| 4 | Stable config/API contract (semver-guarded) | ☐ stabilising under the [compatibility policy](compatibility.md) |
| 5 | Long-running soak test passed | ☐ post-GA gate ([ADR 0005](adr/0005-soak-post-ga-gate.md)) |
| 6 | Runnable example + docs | ✅ this doc (references work in any `*.toml`) |
| 7 | Security / threat note | ☐ leak/precedence note to expand |
| 8 | Fuzzing where parsing is involved | n/a — references use the TOML/config parser (Y1-08), no new parser |
| 9 | Self-explanatory Console surface | ✅ Console **Status**/**Security** report *Secret references* with a count |
