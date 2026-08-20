# Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
# SPDX-License-Identifier: agpl

from http.server import BaseHTTPRequestHandler, HTTPServer
import json
import os

PORT = int(os.environ.get("PORT", "3001"))

class Handler(BaseHTTPRequestHandler):
    def do_GET(self):
        self._ok()

    def do_POST(self):
        self._ok()

    def do_PUT(self):
        self._ok()

    def do_DELETE(self):
        self._ok()

    def _ok(self):
        payload = {
            "backend": "python",
            "method": self.command,
            "path": self.path,
            "ok": True,
        }
        data = json.dumps(payload).encode("utf-8")
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("X-Backend", "python")
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)

    def log_message(self, format, *args):
        return

if __name__ == "__main__":
    server = HTTPServer(("127.0.0.1", PORT), Handler)
    print(f"python backend listening on http://127.0.0.1:{PORT}")
    server.serve_forever()
