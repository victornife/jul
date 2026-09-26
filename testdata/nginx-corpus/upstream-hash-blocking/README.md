# Upstream hash blocking fixture

Repository-authored AGPL-3.0-only fixture for #432. NGINX `hash $request_uri consistent` keys on an expression outside Jul's closed affinity key sources (client IP, one header, one cookie). The importer reports `NGX_UPSTREAM_HASH_KEY` as blocking instead of silently replacing affinity with round robin, so no candidate is generated.
