# stream-extensibility-boundaries

Repository-authored, sanitized NGINX migration fixture for issue #367 ([MIG-06] gRPC/FastCGI/uWSGI/L4 migration E2E lanes), covering the 2026-09-21 amendment's required blocking evidence for extensibility-related stream forms that stay explicit blocking findings rather than silent approximations:

- **`map $ssl_preread_server_name $backend_pool { ... }`** — an arbitrary, dynamic variable map. Jul's bounded SNI-routing model (#426) only represents a static `server_name` + `ssl_preread` merge into `sni_routes`; a `map`-driven routing table has no equivalent, so the directive and each of its assignment lines stay blocking (`NGX_STREAM_MAP`, `NGX_VARIABLE_BLOCK_UNSUPPORTED`).
- **`preread_by_lua_file`** — an `ngx_stream_lua_module` (OpenResty) directive. Jul deliberately does not embed a Lua scripting engine; this falls through to the generic unsupported-stream-directive finding (`NGX_STREAM_UNSUPPORTED`), which any other Lua directive (`content_by_lua_block`, `init_by_lua`, etc.) would equally hit.
- **`js_preread`** — an `ngx_stream_js_module` (njs) directive, a real third-party-maintained stream module bundled separately from nginx core. It hits the same generic unsupported-stream-directive finding, proving third-party stream modules are never silently accepted.

Both stream servers still translate their `proxy_pass` target normally; only the unsupported directive itself blocks, matching the same "capture what's actually unsupported, translate everything else" precedent as `stream-security-boundaries`.

The exact assessment contract, candidate disposition (`required: false` — a fixture with blocking findings has no candidate to validate), categories, origin, and license are recorded in `manifest.json`. No real-Jul E2E runs against this fixture: there is nothing to prove at runtime for a directive that translation refuses to emit.
