# Configuration reload semantics

> Canonical reference for how Jul.IA applies a configuration change and what
> "applied" actually guarantees. It consolidates the reload behaviour that is
> also described, per feature, in [core-http.md](core-http.md),
> [stream-proxy.md](stream-proxy.md), and [console.md](console.md).

Jul.IA reloads configuration **without dropping connections**. A reload can be
triggered three ways, all of which converge on the same validated path:

- **SIGHUP** (Unix) — operator sends the signal.
- **Config file-watch** — the on-disk config file changed.
- **Admin apply** — `POST /api/config/apply` (the console "Apply changes"
  button) writes a new config and triggers a reload.

## Two states: *applied* vs *serving*

- **Applied** — the new configuration passed validation, was persisted to disk,
  and a reload was triggered. This is what the apply API confirms.
- **Serving** — the live runtime has finished swapping to the new configuration.

The swap is asynchronous, so the apply response says the configuration was
**"validated and saved; the live runtime is reloading"** rather than the
past-tense "reloaded." The gap between *applied* and *serving* is kept as small
as possible by an up-front preflight (below), and it is normally sub-second.

## The apply preflight (truthfulness gate)

Before a configuration is written to disk, the admin write path
(`WriteConfigRaw`) runs a full preflight so that a configuration which is
recorded as *applied* is guaranteed to build and bind:

1. **Parse + structural validation** — rejects malformed TOML and invalid field
   combinations.
2. **Composition-root dry-run** — builds the *entire* runtime on a clone of the
   config (plugins, static roots, proxy/gRPC/FastCGI handlers, auth, WAF, router,
   compression) without disturbing the live runtime. A build error (or panic)
   aborts the write.
3. **Stream (L4) dry-run** — builds every `[[stream]]` route set on the clone so
   a bad target is rejected too.
4. **HTTP listener bind-probe** — every **newly introduced** HTTP listen address
   is bind-probed and released, so an apply that adds an unbindable port (in use,
   privileged, invalid) is rejected before the file is written.
5. **Stream listener bind-probe** — every **newly introduced** `[[stream]]`
   listen address (TCP and UDP) is bind-probed and released, symmetric with the
   HTTP probe. Without this, an unbindable stream port would be recorded as
   applied while the asynchronous reload's bind failed and surfaced only in the
   Overview stream-status panel.

Only after all five pass is the file written and the reload triggered.

## Transactional swap

The reload itself is transactional per layer, so a failure never leaves a
partial configuration serving:

- **HTTP** — the new handler generation is built before the listener set is
  swapped; existing connections finish on their original handler.
- **Stream** — routes and pools are built and newly added listeners bound
  **before** any running state changes; a bind failure rolls back to the
  previously serving configuration (see [stream-proxy.md](stream-proxy.md#hot-reload)).

## Why there is no asynchronous reload timeout

The apply response intentionally does **not** carry a per-layer reload result
(`http_ok` / `stream_ok`) or wait on a reload deadline. The two failure modes
that such machinery would guard against are already eliminated earlier:

- A configuration that cannot **build** is caught by the composition-root and
  stream dry-runs (preflight steps 2–3).
- A configuration that cannot **bind** is caught by the HTTP and stream listener
  bind-probes (preflight steps 4–5).

Anything that survives the preflight builds and binds, and the per-layer swap is
transactional with rollback. A reload that is "accepted but never converges" is
therefore not a reachable state, so a reload deadline and an
applied-versus-converged response field would add moving parts without guarding
a real scenario. The honesty guarantee is provided at the source — by the
preflight — rather than by reporting a late failure after the file is already
written.

## Changes that require a restart

A small number of valid changes cannot take effect via hot reload and are
rejected at apply time with a `restart_required` response instead of being
silently accepted:

- **ACME issued-domain set / issuer** — frozen when the autocert manager is
  built at startup.

The operator restarts the process to apply these, rather than believing they
took effect. See [console.md](console.md) for how the console surfaces this.

## Optimistic concurrency

The admin apply path is guarded by an optimistic-concurrency token
(`base_version`): if the live configuration changed since the edit was prepared,
the apply is rejected with `409 Conflict` so a stale edit cannot clobber a
concurrent change. See [console.md](console.md) for the console's conflict
handling.
