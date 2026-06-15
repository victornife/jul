# Migrating from NGINX

This example shows `jul import nginx` translating a real-world NGINX
configuration into Jul.IA TOML.

- [`nginx.conf`](nginx.conf) — the source NGINX configuration.
- [`jul.toml`](jul.toml) — the configuration produced by the importer.

## Build with the importer tag

The importer is gated behind the `importer` build tag so it stays out of the
default binary:

```bash
go build -tags importer -o jul ./cmd/jul
```

## Run the import

```bash
jul import nginx -o jul.toml examples/migrate/nginx.conf
```

This regenerates [`jul.toml`](jul.toml). Omit `-o` to print the result to stdout.
The report is always written to stderr:

```text
imported examples/migrate/nginx.conf: 2 server(s), 1 upstream(s), 5 location(s)

1 directive(s) not translated (port manually):
  line 52: proxy_set_header - unsupported location-level directive

notes:
  - location ~* "\.(jpg|png|css|js)$" at line 34: case-insensitive regex mapped to regex (case sensitivity not preserved)
  - upstream app: server 10.0.0.3:8080 (line 18) is marked down and was omitted
```

The same skipped-directive list is embedded as `# TODO` comments at the top of
the generated file, so nothing is lost silently.

## What this example demonstrates

| NGINX construct | Result in `jul.toml` |
| --------------- | -------------------- |
| `gzip on;` | `[compression] enabled = true` |
| `upstream app { least_conn; server ... weight=3; server ... down; }` | `[[upstreams]]` with `strategy = "least_conn"`, weights preserved, the `down` server omitted |
| static `server { listen 80; root ...; location / { try_files ... } }` | a `:80` server with a static location |
| `location ~* \.(jpg\|png\|css\|js)$` | a `regex` location (a note records the lost case-insensitivity) |
| `return 301 https://...;` | a location with `redirect` + `return = 301` |
| TLS `server { listen 443 ssl; ssl_certificate ...; ssl_protocols ...; }` | a `:443` server with `[servers.tls]` (`cert`, `key`, `min_version`) |
| `location / { proxy_pass http://app; }` | a location proxying to the imported upstream |
| `proxy_set_header` | **not translated** — reported for manual porting |

## Validate the result

The importer already re-parses and validates its own output, but you can confirm
with the bundled linter:

```bash
jul lint -config examples/migrate/jul.toml
```

## After importing

Review the `# TODO` comments and port anything the importer could not map
(custom headers, `client_max_body_size`, `map`/`if` blocks, `include`d files,
and so on). Then run `jul fmt -w examples/migrate/jul.toml` if you want to drop
the default zero-valued fields and tidy the file.
