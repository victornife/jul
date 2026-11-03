# WAF Policy Evaluation and External-Asset Recovery (`#630`)

This example demonstrates how to evaluate a candidate WAF policy against a
versioned pack of synthetic legitimate, boundary, and malicious fixtures, record
an exact policy/asset manifest, and verify or recover external SecLang files
across same-path edits and restarts.

## 1. Evaluate the policy against synthetic fixtures

From `examples/waf-policy/`:

```bash
jul waf evaluate -config jul.toml -fixtures fixtures.json
```

Emit the machine-readable evaluation report (`schema_version: 1`):

```bash
jul waf evaluate -config jul.toml -fixtures fixtures.json -json
```

Compare a candidate configuration against a baseline configuration:

```bash
jul waf evaluate -config jul.toml -baseline jul.toml -fixtures fixtures.json
```

## 2. Record the exact compiled policy and external-asset manifest

Configuration rollback (`jul rollback`) restores `server.toml`, not external
files on disk referenced by `directives_files` or `Include`. Record a local
manifest of the exact compiled bytes (`raw_policy_sha256`, `raw_external_sha256`,
and per-file `external_assets`) alongside immutable versioned rule directories:

```bash
jul waf manifest -config jul.toml -out waf-manifest.json
```

## 3. Verify external assets before promotion or after recovery

```bash
jul waf verify-assets -config jul.toml -manifest waf-manifest.json
```

If `rules/site-custom.conf` is modified in place or missing, `jul waf verify-assets`
exits `1` and reports `asset_changed` or `asset_missing` with the exact SHA-256
and size delta. Restore the immutable rule file from your versioned asset
backup and re-run `jul waf verify-assets` to confirm `status=verified`.
