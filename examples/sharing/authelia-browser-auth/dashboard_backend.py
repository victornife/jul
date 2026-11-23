#!/usr/bin/env python3
# Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
# SPDX-License-Identifier: agpl

"""Loopback-only sample origin for the #627 browser-auth composition."""

from __future__ import annotations

from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import json
import threading
import time


class State:
    def __init__(self) -> None:
        self.lock = threading.Lock()
        self.requests = 0
        self.streams_opened = 0
        self.streams_active = 0
        self.streams_closed = 0

    def snapshot(self) -> dict[str, int]:
        with self.lock:
            return {
                "requests": self.requests,
                "streams_opened": self.streams_opened,
                "streams_active": self.streams_active,
                "streams_closed": self.streams_closed,
            }


STATE = State()


class DashboardHandler(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

    def log_message(self, _format: str, *_args: object) -> None:
        return

    def do_GET(self) -> None:
        path = self.path.partition("?")[0]
        if path == "/healthz":
            self._send_json({"status": "ok"})
            return
        if path == "/__control/counters":
            self._send_json(STATE.snapshot())
            return
        if path == "/events":
            self._stream()
            return
        if path != "/":
            self._send_json({"error": "not found"}, status=404)
            return

        with STATE.lock:
            STATE.requests += 1
            sequence = STATE.requests
        self._send_json(
            {
                "service": "jul627-dashboard",
                "marker": "JUL-627-PRIVATE-DASHBOARD",
                "sequence": sequence,
                "identity": {
                    "user": self.headers.get("Remote-User", ""),
                    "groups": self.headers.get("Remote-Groups", ""),
                    "name": self.headers.get("Remote-Name", ""),
                    "email": self.headers.get("Remote-Email", ""),
                },
                "cookie_received": bool(self.headers.get("Cookie")),
                "identity_note": "Display only; application permissions are not implemented here.",
            }
        )

    def _send_json(self, payload: dict[str, object], status: int = 200) -> None:
        body = (json.dumps(payload, sort_keys=True) + "\n").encode("utf-8")
        self.send_response(status)
        self.send_header("Content-Type", "application/json; charset=utf-8")
        self.send_header("Cache-Control", "private, no-store")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def _stream(self) -> None:
        with STATE.lock:
            STATE.streams_opened += 1
            STATE.streams_active += 1
        self.send_response(200)
        self.send_header("Content-Type", "text/event-stream")
        self.send_header("Cache-Control", "no-cache, no-store")
        self.send_header("X-Accel-Buffering", "no")
        self.end_headers()
        try:
            sequence = 0
            while True:
                sequence += 1
                self.wfile.write(f"event: sample\ndata: {sequence}\n\n".encode("ascii"))
                self.wfile.flush()
                time.sleep(1)
        except (BrokenPipeError, ConnectionResetError, OSError):
            pass
        finally:
            with STATE.lock:
                STATE.streams_active -= 1
                STATE.streams_closed += 1


def main() -> None:
    server = ThreadingHTTPServer(("127.0.0.1", 13000), DashboardHandler)
    server.daemon_threads = True
    print("sample dashboard listening on 127.0.0.1:13000")
    server.serve_forever()


if __name__ == "__main__":
    main()
