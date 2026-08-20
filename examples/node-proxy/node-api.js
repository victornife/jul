/**
 * Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */

const http = require('http');

const port = Number(process.env.PORT || 3000);

const server = http.createServer((req, res) => {
  const body = JSON.stringify({
    backend: 'node',
    method: req.method,
    path: req.url,
    ok: true
  });
  res.writeHead(200, {
    'Content-Type': 'application/json',
    'X-Backend': 'node'
  });
  res.end(body);
});

server.listen(port, '127.0.0.1', () => {
  console.log(`node backend listening on http://127.0.0.1:${port}`);
});
