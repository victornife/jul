# Migrating from NGINX

This example shows `jul import nginx` translating a real-world NGINX
configuration into Jul.IA TOML.

> **Maturity: base importer GA; assessment and extended translation Beta.**
> The importer covers common reverse-proxy and static-file setups, never fails
> silently (every unmapped directive is
> reported), and always re-validates its own output — but it is not a complete
> NGINX emulator. Review the structured assessment and the `# TODO`/notes in
> the output before serving the result. A candidate with blocking findings is
> generated for review, but the command exits 3 (`manual_action_required`).

Published v2.0.0 includes the base importer and Beta assessment/include support.
The proposed v2.1.0 tree adds bounded stream, protocol and affinity translations
and focused migration E2E evidence; those additions are not yet published and
do not promise universal NGINX equivalence. See the
[status matrix](../../docs/status.md) for delivery and maturity.

- [`nginx.conf`](nginx.conf) — the source NGINX configuration.
- [`jul.toml`](jul.toml) — an illustrative snapshot of importer output. The
  current serializer may include additional explicit zero-valued fields.

## Build with the importer tag

The importer is gated behind the `importer` build tag so it stays out of the
default binary:

```bash
go build -tags importer -o jul ./cmd/jul
```

## Run the import

From the repository root, first assess, then convert into a separate candidate
file so the committed example remains available for comparison:

```bash
./jul import nginx --assess --report /tmp/jul-nginx-assessment.json examples/migrate/nginx.conf
./jul import nginx -o /tmp/jul-nginx-candidate.toml --report /tmp/jul-nginx-conversion.json examples/migrate/nginx.conf
```

Both commands exit 3 for this fixture because `proxy_set_header` needs manual
mapping; that is an expected blocked result, not a ready-to-serve config.
The conversion still writes the candidate. The human summary goes to stderr;
the JSON reports name the source, class, risk and location of every assessed
directive. Omit `-o` to print the generated TOML to stdout. A typical summary:

```text
imported examples/migrate/nginx.conf: 2 server(s), 1 upstream(s), 5 location(s), 0 stream(s)

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
./jul lint -config /tmp/jul-nginx-candidate.toml
```

## After importing

Review the JSON assessment and `# TODO` comments, then port the blocking
directive and any approximations relevant to your setup. Some forms of custom
headers, body-size directives and includes are supported on current `main`;
consult the [directive table](../../docs/nginx-importer.md) and
[assessment guide](../../docs/nginx-assessment.md) for exact boundaries.
`jul fmt -w` canonicalizes a config; it does not promise to omit zero-valued
fields. Validate the edited candidate again and test its behavior before use.

## Best-effort caveats

These mappings are approximate and surface as `notes:` in the report — confirm
them after importing:

- **`proxy_pass` trailing slash.** A trailing slash on `proxy_pass http://host/`
  makes NGINX rewrite the matched location prefix; Jul.IA drops the slash and
  does not rewrite, so adjust the location or upstream path if you relied on it.
- **Server-level `return`.** A `return` outside a location becomes a catch-all
  `/` location. NGINX evaluates the server-level return before locations; Jul.IA
  gives matching locations precedence, so verify the intended order.
- **Multiple `listen` directives.** A server block binds a single address — only
  the first usable `listen` is kept and any others are dropped (split them into
  separate server blocks if you need both).
