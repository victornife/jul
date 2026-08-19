# Getting started

This guide walks you through running Jul.IA for the first time: zero-config
mode, a static site, a reverse proxy with TLS, and a quick look at the admin
console.

## Prerequisites

- A downloaded Jul.IA binary or Go 1.26+ to build from source.
- A terminal (PowerShell on Windows, bash/zsh on Linux/macOS).
- Ports 8080 and 9090 free on your machine.

## Zero-config mode

Jul.IA can run without a config file. This is useful for quick tests and local
development.

### Serve a directory

```bash
# From the repo root or extracted archive
./jul run --serve ./public --listen :8080
```

Open http://localhost:8080 in your browser. Files in `./public` are served
with index and `try_files` defaults. Compression and sensible timeouts are
enabled automatically.

### Proxy to a backend

```bash
./jul run --proxy 127.0.0.1:3000 --listen :8080
```

Every request to `:8080` is forwarded to `127.0.0.1:3000`.

Zero-config mode never writes a config file; use `jul lint` or write a TOML
config when you need more control.

---

## Starting the server with a config file

Once you have a `server.toml`, start the server with:

```bash
jul serve                         # uses ./server.toml by default
jul serve -config /etc/jul/server.toml  # explicit path
```

`jul serve` is the explicit, discoverable form of the default bare `jul`
invocation — both are equivalent. Tab-completion, `--help`, and the usage
block all surface it.

---

## Your first config file

Create a file named `server.toml`:

```toml
[global]
log_level = "info"
log_format = "text"

[[servers]]
listen = "0.0.0.0:8080"
server_names = ["localhost"]

  [[servers.locations]]
  match = { type = "prefix", path = "/" }
  root = "./public"
  index = ["index.html"]
```

Run it:

```bash
./jul --config server.toml
```

Place an `index.html` in `./public` and reload `http://localhost:8080`.

---

## Reverse proxy with load balancing

Add an upstream and a proxy location:

```toml
[[servers]]
listen = "0.0.0.0:8080"
server_names = ["app.local"]

  [[servers.locations]]
  match = { type = "prefix", path = "/api/" }
  proxy_pass = "http://api"

[[upstreams]]
name = "api"
strategy = "round_robin"
servers = ["127.0.0.1:3000", "127.0.0.1:3001"]
```

Start two backends on ports 3000 and 3001. Jul.IA balances requests between
them and takes a backend out of rotation after `max_fails` consecutive failures
(default 1).

---

## Enable TLS

Generate a self-signed certificate for local testing:

```bash
openssl req -x509 -newkey rsa:4096 -keyout key.pem -out cert.pem -days 365 -nodes -subj "/CN=localhost"
```

Add TLS to your server block:

```toml
[[servers]]
listen = "0.0.0.0:8443"
server_names = ["localhost"]

  [servers.tls]
  enabled = true
  cert = "./cert.pem"
  key  = "./key.pem"

  [[servers.locations]]
  match = { type = "prefix", path = "/" }
  root = "./public"
```

Visit `https://localhost:8443`. In production, use real certificates or enable
ACME (see [Configuration reference](configuration.md#automatic-https-acme)).

---

## Enable the admin console

Add the `[admin]` block and rebuild with the `console` build tag:

```bash
go build -tags console -o jul ./cmd/jul
```

```toml
[admin]
enabled = true
listen = "127.0.0.1:9090"
token = "a-strong-random-token"
console = true
```

Now open http://localhost:9090 and log in with your token. The console shows:

- Live request metrics and cache hit rates.
- Upstream health graphs.
- Certificate inventory and expiry.
- Config history with one-click rollback.

Keep the admin listener on loopback and use a strong token in production.

---

## Validate and format configs

Before reloading, lint your config:

```bash
./jul lint -config server.toml
```

Exit codes: `0` = clean, `1` = errors, `2` = warnings (with `-strict`).

To rewrite a config into canonical TOML:

```bash
./jul fmt -config server.toml -w    # rewrite in place
./jul fmt -config server.toml       # print to stdout, don't write
./jul fmt -config server.toml -diff # show what would change (exit 1 if changes needed)
```

All three forms run the canonical semantic validator before formatting, so an
unknown key or invalid known value is rejected and `-w` never persists it. The
`-diff` mode is useful in CI to enforce canonical formatting without modifying
files — it exits 0 when nothing would change, 1 when changes are needed. Comments
and original formatting are not preserved.

## Version and shell completion

Check what you are running — human-readable, or `-json` for scripts and CI
(keys: `product`, `version`, `commit`, `build_date`, `dirty`, `go_version`,
`os`, `arch`):

```bash
./jul version
./jul version -json
```

Enable tab-completion for your shell (`bash`, `zsh`, `fish`, or `powershell`):

```bash
source <(jul completion bash)                                # current session
jul completion fish > ~/.config/fish/completions/jul.fish    # installed
```

---

## Next steps

- Read the full **[Configuration reference](configuration.md)** for every key
  and default.
- Explore **[examples/](../examples/)** for runnable configs: JWT auth, gRPC
  transcoding, WASM plugins, ACME auto-HTTPS, and more.
- Review **[Deployment](deployment.md)** to run Jul.IA as a systemd or Windows
  service.
- Hit a snag? See **[Troubleshooting](troubleshooting.md)** for common first-run
  and operational issues.
- Check **[Security](../SECURITY.md)** for threat model and hardening
  recommendations.
