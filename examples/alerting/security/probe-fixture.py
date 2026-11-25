# Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
# SPDX-License-Identifier: agpl

from http.server import BaseHTTPRequestHandler, HTTPServer


class ProbeFixture(BaseHTTPRequestHandler):
    def do_GET(self):
        if self.path != "/ops-probe":
            self.send_error(404)
            return
        body = b"JUL_OPS_PROBE_OK\n"
        self.send_response(200)
        self.send_header("Content-Type", "text/plain; charset=utf-8")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

HTTPServer(("127.0.0.1", 18080), ProbeFixture).serve_forever()
