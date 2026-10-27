# Bounded cache inspection

Requires the unreleased #622 API/CLI and explicit `cache:inspect` authority.
The request reads metadata and performs no application/origin request.

```sh
export JUL_ADMIN_ENDPOINT=https://admin.example.com
export JUL_ADMIN_TOKEN_FILE=/run/secrets/jul-admin.token
./examples/cache-inspection/inspect.sh portal.example.com
# Repeat the same host with next_cursor before expires_at:
./examples/cache-inspection/inspect.sh portal.example.com 'RETURNED_CURSOR'
```

Protect the token file using the CLI's normal credential-file rules. The script
never prints tokens or restarts pagination automatically. Check `inspection.state`,
`count_accuracy`, `evidence_state` and `concurrent_change`; valid partial/truncated
read responses still exit zero. For exact paths, filters, missing/legacy evidence,
retention and error recovery, see [the cache guide](../../docs/cache.md#bounded-cache-inspection).

NGINX migration impact: not applicable; no imported request semantics change.
