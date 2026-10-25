# Scoped Cache Invalidation Example (`#621`)

This directory contains a complete configuration (`jul.toml`) and an
authenticated content-update / deployment script (`purge-on-deploy.sh`)
demonstrating Jul.IA's scoped cache invalidation workflow ([ADR 0027](../../docs/adr/0027-scoped-cache-invalidation.md),
[cache guide](../../docs/cache.md#scoped-cache-invalidation)).

## Files

- [`jul.toml`](jul.toml) — Enables the two-tier response cache with trusted
  origin tag ingestion (`tag_headers = ["Cache-Tag"]`, `strip_tag_headers = true`)
  and defines a least-privilege RBAC role (`cache_purger` with only
  `cache:purge`) for automated invalidation. Works in both `file_owned` and
  `managed` configuration authority modes because cache invalidation is a
  runtime operational action rather than a configuration-file edit.
- [`purge-on-deploy.sh`](purge-on-deploy.sh) — Runs the two-step
  `preview -> confirm -> execute` workflow via `jul cache purge`, binding
  execution to the server-issued `preview_token` and an optional
  `--idempotency-key` so network retries never broaden scope or re-purge content
  filled after the original invalidation completed.

## Prerequisites & symbolic placeholders

`jul.toml` references two environment secrets:

- `JUL_ADMIN_TOKEN`: full admin bearer token (symbolic placeholder in docs/tests).
- `JUL_CACHE_PURGE_TOKEN`: least-privilege `cache:purge` token used by
  `purge-on-deploy.sh`.

Validate the configuration locally:

```bash
JUL_ADMIN_TOKEN="replace-with-admin-token" \
JUL_CACHE_PURGE_TOKEN="replace-with-purge-token" \
jul check -config examples/cache-invalidation/jul.toml -skip-static-roots
```

## Running the content-update invalidation script

1. **Invalidate by origin grouping tag (`article:42`):**

   When the origin emits `Cache-Tag: article:42` on `/articles/42`, `/`, and
   `/categories/news`, Jul indexes those representations under
   `(portal.example.com, article:42)` and strips `Cache-Tag` before sending the
   response to clients. After an edit to article 42:

   ```bash
   ./examples/cache-invalidation/purge-on-deploy.sh \
     --endpoint http://127.0.0.1:9090 \
     --token-file /secure/path/cache-purger.token \
     --host portal.example.com \
     --tag article:42 \
     --idempotency-key article-42-edit-20261025-01
   ```

2. **Invalidate by path prefix or glob without origin changes:**

   ```bash
   ./examples/cache-invalidation/purge-on-deploy.sh \
     --endpoint http://127.0.0.1:9090 \
     --token-file /secure/path/cache-purger.token \
     --host portal.example.com \
     --prefix /articles/ \
     --idempotency-key deploy-articles-20261025-01
   ```

## Exit codes

`purge-on-deploy.sh` preserves Jul's remote automation exit contract
(`internal/adminapi.ExitCodes()`):

- `0`: invalidation succeeded (`invalidation_effective = "effective"`).
- `2`: invalid selector or missing flag.
- `4`: partial removal (`state = "partial"`, tombstoned for retry).
- `5`: broad scope blocked without `--allow-broad`, or `invalidation_effective = "uncertain"`.
- `7`: authentication, `cache:purge` RBAC denial, or non-TLS non-loopback refusal.
- `8`: connectivity or rate-limit failure (`Retry-After`).
- `9`: server, timeout, or failed invalidation.

## Owner & revalidation triggers

- **Owner:** `@victornife`
- **Revalidate when:** `/api/v1/cache/purge*` request/response schemas,
  `jul cache purge` flags, or `[cache].tag_headers` semantics change.
