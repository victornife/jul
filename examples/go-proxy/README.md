# Go app behind Jul.IA (proxy_pass)

A small `net/http` server with Jul.IA reverse-proxying to it.

| Setting | Value |
| --- | --- |
| Backend app | `http://127.0.0.1:3033` |
| Jul.IA front | `http://127.0.0.1:8101` |
| Action | `proxy_pass` |

## Run

1. Start the Go app:

   ```bash
   go run ./examples/go-proxy/app.go
   ```

2. In another terminal, start Jul.IA with this config:

   ```bash
   go run ./cmd/jul -config examples/go-proxy/jul.toml
   ```

3. Open <http://127.0.0.1:8101/>.

The app lives in its own folder as `package main`, so it does not interfere with
`go build ./...` for the main server. Go's `net/http` handles concurrent and
keep-alive connections natively.
