# Node.js app behind Jul.IA (proxy_pass)

A dependency-free Node.js HTTP server with Jul.IA reverse-proxying to it.

| Setting | Value |
| --- | --- |
| Backend app | `http://127.0.0.1:3032` |
| Jul.IA front | `http://127.0.0.1:8100` |
| Action | `proxy_pass` |

## Run

1. Start the Node app (no `npm install` needed — it uses only the standard
   library):

   ```bash
   node examples/node-proxy/app.js
   ```

2. In another terminal, start Jul.IA with this config:

   ```bash
   go run ./cmd/jul -config examples/node-proxy/jul.toml
   ```

3. Open <http://127.0.0.1:8100/>.

Node's `http` server handles concurrent and keep-alive connections natively, so
it works cleanly behind Jul.IA.
