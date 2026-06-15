# Rust app behind Jul.IA (proxy_pass)

A std-only, thread-per-connection Rust HTTP server with Jul.IA reverse-proxying
to it. No external crates.

| Setting | Value |
| --- | --- |
| Backend app | `http://127.0.0.1:3034` |
| Jul.IA front | `http://127.0.0.1:8102` |
| Action | `proxy_pass` |

## Run

1. Build and start the Rust app (pick one):

   ```bash
   # With Cargo:
   cd examples/rust-proxy && cargo run --release

   # Or without Cargo, straight from rustc:
   rustc -O examples/rust-proxy/app.rs -o examples/rust-proxy/app
   ./examples/rust-proxy/app        # Linux/macOS
   .\examples\rust-proxy\app.exe    # Windows
   ```

2. In another terminal, start Jul.IA with this config:

   ```bash
   go run ./cmd/jul -config examples/rust-proxy/jul.toml
   ```

3. Open <http://127.0.0.1:8102/>.

## Notes

- The crate is configured via `Cargo.toml` with `[[bin]] name = "app"` so
  rust-analyzer discovers the project. A `.cargo/config.toml` sets
  `check-revoke = false` to work around corporate-network TLS revocation-check
  failures on Windows (schannel `CRYPT_E_NO_REVOCATION_CHECK`).
- The server spawns a thread per connection, so it handles Jul.IA's concurrent
  keep-alive connections correctly.
