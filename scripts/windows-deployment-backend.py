#!/usr/bin/env python3
"""A real AF_UNIX HTTP backend for the Windows virtual-service journey."""

import argparse
import http.server
import json
import os
from pathlib import Path
import socketserver


class Handler(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        body = b"Jul CI Unix backend\n"
        self.send_response(200)
        self.send_header("Content-Type", "text/plain; charset=utf-8")
        self.send_header("Content-Length", str(len(body)))
        self.send_header("X-Backend-Host", self.headers.get("Host", ""))
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, *unused):
        pass


class Server(socketserver.ThreadingMixIn, socketserver.UnixStreamServer):
    daemon_threads = True


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--unix", type=Path, required=True)
    parser.add_argument("--ready-file", type=Path, required=True)
    args = parser.parse_args()
    assert not args.unix.exists(), "backend refuses to replace an existing socket"
    with Server(str(args.unix), Handler) as server:
        args.ready_file.write_text(json.dumps({"pid": os.getpid(), "network": "unix"}) + "\n")
        server.serve_forever()


if __name__ == "__main__":
    main()
