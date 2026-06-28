# Automatic HTTPS with ACME (Let's Encrypt)

Jul obtains and renews TLS certificates automatically using the ACME **HTTP-01**
challenge (this example). The **TLS-ALPN-01** challenge is also supported and is
answered on the `:443` listener itself — set `challenge = "tls-alpn-01"` if you
cannot expose port 80. No `cert`/`key` files to manage.

| Setting | Value |
| --- | --- |
| HTTPS front | `https://example.com` (`:443`) |
| HTTP front | `http://example.com` (`:80`, answers the challenge + redirects) |
| Certificate authority | Let's Encrypt **staging** (switch to production when ready) |
| Certificate cache | `./jul-data/certs` |

## Prerequisites

- A binary built with the `acme` build tag (the feature is off by default):

  ```bash
  go build -tags acme -o jul ./cmd/jul
  ```

- A real domain whose DNS `A`/`AAAA` record points at this host.
- Ports **80 and 443 reachable from the internet** — the CA connects back on
  port 80 to verify the HTTP-01 challenge.

## Run

1. Edit [`jul.toml`](jul.toml) and replace `example.com` / `ops@example.com`
   with your domain and contact email.

2. Validate the config (works with any build, no tag required):

   ```bash
   ./jul -check -config examples/auto-https/jul.toml
   ```

3. Start Jul (needs privileges to bind `:80`/`:443`):

   ```bash
   ./jul -config examples/auto-https/jul.toml
   ```

   On the first request to your domain, Jul completes the ACME challenge and
   caches the issued certificate under `./jul-data/certs`. Renewals happen
   automatically well before expiry.

4. Open `https://your-domain/`.

## Going to production

The config starts on Let's Encrypt **staging**, which issues *untrusted*
certificates but has generous rate limits — ideal for verifying your setup.
Once a staging certificate is issued successfully, switch the CA to production
and remove the staging cache so a trusted certificate is requested:

```toml
[servers.tls.acme]
ca = "letsencrypt"
```

```bash
rm -rf ./jul-data/certs   # drop staging certs before the production switch
```

## Notes

- Keep the plain `:80` server block running; it answers the HTTP-01 challenge
  (and here also redirects everything else to HTTPS).
- **OCSP stapling** is enabled by default: Jul fetches and refreshes the OCSP
  response in the background and staples it to handshakes, degrading gracefully
  (served unstapled) if the responder is unreachable. Set `ocsp_stapling = false`
  under `[servers.tls.acme]` to turn it off.
- A binary built *without* `-tags acme` refuses to start with this config and
  prints a clear message, so the feature is never silently disabled.
- Wildcard certificates need the `dns-01` challenge, which is reserved for a
  future release and rejected today.
- Certificate health is exported via the `jul_tls_cert_expiry_seconds` gauge and
  `jul_acme_renewals_total` counter on the admin metrics endpoint.
