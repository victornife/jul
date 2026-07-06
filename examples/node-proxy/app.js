/**
 * Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */

// Minimal Node.js HTTP app served behind Jul via proxy_pass.
//
// Dependency-free: uses only the built-in `http` module, so there is nothing
// to install. Run it with:
//     node app.js
//
// Node's http server handles concurrent / keep-alive connections out of the
// box, so it works cleanly behind Jul's reverse proxy.

const http = require("http");

const HOST = "127.0.0.1";
const PORT = 3032;

const server = http.createServer((req, res) => {
  res.writeHead(200, { "Content-Type": "text/plain; charset=utf-8" });
  res.end("Hello from a Node.js app behind Jul (proxy_pass over HTTP)!\n");
});

server.listen(PORT, HOST, () => {
  console.log(`Serving on http://${HOST}:${PORT} (Ctrl+C to stop)`);
});
