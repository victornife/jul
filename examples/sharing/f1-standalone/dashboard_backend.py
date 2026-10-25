#!/usr/bin/env python3
# Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
# SPDX-License-Identifier: agpl

"""Deterministic F1 dashboard HTTP/1.1 + SSE + RFC 6455 WebSocket backend (#581).

Binds only to loopback (default 127.0.0.1:3000) using Python's standard library
so an operator running a downloaded Jul binary does not need Go or Docker.

Endpoints:
  - GET / or /api/summary : ~2 KiB deterministic JSON with content marker
    "JUL-F1-RESTRICTED-DASHBOARD-MARKER-v1", private data marker
    "JUL-F1-PRIVATE-FAMILY-TELEMETRY-DO-NOT-CACHE", canonical observed path,
    and observed forwarding/identity headers (never trusted for authorization).
  - GET /events           : Server-Sent Events (SSE) stream emitting periodic
    frames until client disconnect or Jul exact-entry retirement (#642/#563).
  - GET /ws               : RFC 6455 HTTP/1.1 WebSocket upgrade supporting ping,
    data frames, and reciprocal Close (1008/1001/1000) handshakes (#642/#562).
  - GET /healthz          : 200 OK readiness probe.
  - GET /__control/counters : Bounded request/stream counters for origin
    enforcement attribution (proving denied/WAF/rate-limited requests never
    reach the origin).
  - POST /__control/reset : Reset bounded counters between test cases.
"""

from __future__ import annotations

import argparse
import base64
import hashlib
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import json
import socket
import struct
import sys
import threading
import time
from typing import Any

DASHBOARD_MARKER = "JUL-F1-RESTRICTED-DASHBOARD-MARKER-v1"
PRIVATE_CONTENT_MARKER = "JUL-F1-PRIVATE-FAMILY-TELEMETRY-DO-NOT-CACHE"
WS_GUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"

OBSERVED_HEADER_NAMES = (
    "x-forwarded-for",
    "x-real-ip",
    "x-forwarded-proto",
    "x-forwarded-host",
    "forwarded",
    "client-cert",
    "client-cert-chain",
    "x-forwarded-client-cert",
    "tailscale-user-login",
    "tailscale-user-name",
    "cf-access-authenticated-user-email",
    "cf-access-jwt-assertion",
)


class BoundedOriginState:
    def __init__(self) -> None:
        self.lock = threading.Lock()
        self.reset()

    def reset(self) -> None:
        with self.lock:
            self.total_requests = 0
            self.dashboard_requests = 0
            self.sse_opened = 0
            self.sse_active = 0
            self.sse_closed = 0
            self.ws_opened = 0
            self.ws_active = 0
            self.ws_closed = 0
            self.ws_close_codes: list[int] = []
            self.by_path: dict[str, int] = {}
            self.last_observed_headers: dict[str, str] = {}
            self.last_observed_path = ""
            self.last_observed_host = ""

    def record_request(self, path: str, host: str, headers: dict[str, str], is_dashboard: bool = True) -> int:
        with self.lock:
            self.total_requests += 1
            if is_dashboard:
                self.dashboard_requests += 1
            key = path if len(path) <= 128 else path[:128]
            if len(self.by_path) < 64 or key in self.by_path:
                self.by_path[key] = self.by_path.get(key, 0) + 1
            self.last_observed_headers = dict(headers)
            self.last_observed_path = key
            self.last_observed_host = host[:128]
            return self.dashboard_requests

    def snapshot(self) -> dict[str, Any]:
        with self.lock:
            return {
                "total_requests": self.total_requests,
                "dashboard_requests": self.dashboard_requests,
                "sse_opened": self.sse_opened,
                "sse_active": self.sse_active,
                "sse_closed": self.sse_closed,
                "ws_opened": self.ws_opened,
                "ws_active": self.ws_active,
                "ws_closed": self.ws_closed,
                "ws_close_codes": list(self.ws_close_codes[-32:]),
                "by_path": dict(self.by_path),
                "last_observed_headers": dict(self.last_observed_headers),
                "last_observed_path": self.last_observed_path,
                "last_observed_host": self.last_observed_host,
            }


STATE = BoundedOriginState()


def build_ws_frame(opcode: int, payload: bytes = b"") -> bytes:
    header = bytearray([0x80 | (opcode & 0x0F)])
    length = len(payload)
    if length < 126:
        header.append(length)
    elif length < 65536:
        header.append(126)
        header.extend(struct.pack("!H", length))
    else:
        header.append(127)
        header.extend(struct.pack("!Q", length))
    return bytes(header) + payload


def read_ws_frame(conn: socket.socket) -> tuple[int, bytes] | None:
    def recv_exact(n: int, idle_ok_on_first_byte: bool = False) -> bytes | None:
        buf = bytearray()
        frame_deadline: float | None = None
        while len(buf) < n:
            try:
                chunk = conn.recv(n - len(buf))
            except TimeoutError:
                if len(buf) == 0 and idle_ok_on_first_byte:
                    continue
                if frame_deadline is None:
                    frame_deadline = time.monotonic() + 5.0
                if time.monotonic() < frame_deadline:
                    continue
                return None
            except OSError:
                return None
            if not chunk:
                return None
            buf.extend(chunk)
            if frame_deadline is None:
                frame_deadline = time.monotonic() + 5.0
        return bytes(buf)

    first2 = recv_exact(2, idle_ok_on_first_byte=True)
    if first2 is None:
        return None
    opcode = first2[0] & 0x0F
    masked = (first2[1] & 0x80) != 0
    length = first2[1] & 0x7F
    if length == 126:
        ext = recv_exact(2)
        if ext is None:
            return None
        length = struct.unpack("!H", ext)[0]
    elif length == 127:
        ext = recv_exact(8)
        if ext is None:
            return None
        length = struct.unpack("!Q", ext)[0]
    if length > (1 << 20):
        return None
    mask_key = b""
    if masked:
        mk = recv_exact(4)
        if mk is None:
            return None
        mask_key = mk
    payload = recv_exact(length) if length > 0 else b""
    if payload is None:
        return None
    if masked and payload:
        payload = bytes(b ^ mask_key[i % 4] for i, b in enumerate(payload))
    return opcode, payload


class F1DashboardHandler(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

    def log_message(self, format: str, *args: Any) -> None:
        pass

    def _observed_headers(self) -> dict[str, str]:
        out: dict[str, str] = {}
        for name in OBSERVED_HEADER_NAMES:
            val = self.headers.get(name)
            if val is not None:
                out[name] = val
        return out

    def do_HEAD(self) -> None:
        self._handle_request(head_only=True)

    def do_GET(self) -> None:
        self._handle_request(head_only=False)

    def do_POST(self) -> None:
        content_len = int(self.headers.get("Content-Length", "0") or "0")
        if content_len > 0:
            _ = self.rfile.read(min(content_len, 2 * 1024 * 1024))
        if self.path == "/__control/reset":
            STATE.reset()
            body = json.dumps({"ok": True, "counters": STATE.snapshot()}).encode("utf-8")
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)
            return
        self._handle_request(head_only=False)

    def _handle_request(self, head_only: bool) -> None:
        if self.path == "/healthz":
            body = b'{"status":"ok","service":"f1-dashboard-backend"}\n'
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            if not head_only:
                self.wfile.write(body)
            return

        if self.path == "/__control/counters":
            body = (json.dumps(STATE.snapshot(), indent=2) + "\n").encode("utf-8")
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            if not head_only:
                self.wfile.write(body)
            return

        observed = self._observed_headers()
        seq = STATE.record_request(self.path, self.headers.get("Host", ""), observed, is_dashboard=True)

        if self.path.startswith("/events"):
            self._handle_sse(seq, head_only)
            return

        if self.path.startswith("/ws") and self.headers.get("Upgrade", "").lower() == "websocket":
            self._handle_websocket(seq)
            return

        # Standard ~2 KiB restricted dashboard JSON payload
        payload_obj: dict[str, Any] = {
            "service": "f1-restricted-dashboard",
            "marker": DASHBOARD_MARKER,
            "private_marker": PRIVATE_CONTENT_MARKER,
            "seq": seq,
            "observed_path": self.path,
            "observed_host": self.headers.get("Host", ""),
            "observed_headers": observed,
            "authorization_note": "Backend never trusts X-Forwarded-For or provider identity headers (SH-12).",
            "sensors": [
                {"id": f"greenhouse-zone-{i}", "temp_c": 21.5 + (i * 0.25), "humidity_pct": 58 + i}
                for i in range(1, 18)
            ],
        }
        raw = json.dumps(payload_obj)
        if len(raw) < 2048:
            payload_obj["padding"] = "d" * (2048 - len(raw))
            raw = json.dumps(payload_obj)
        body = (raw + "\n").encode("utf-8")

        self.send_response(200)
        self.send_header("Content-Type", "application/json; charset=utf-8")
        self.send_header("Cache-Control", "private, no-store")
        self.send_header("X-Dashboard-Origin-Seq", str(seq))
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        if not head_only:
            self.wfile.write(body)

    def _handle_sse(self, seq: int, head_only: bool) -> None:
        with STATE.lock:
            STATE.sse_opened += 1
            STATE.sse_active += 1
        try:
            self.send_response(200)
            self.send_header("Content-Type", "text/event-stream")
            self.send_header("Cache-Control", "no-cache, no-store")
            self.send_header("X-Accel-Buffering", "no")
            self.end_headers()
            if head_only:
                return
            tick = 0
            while True:
                tick += 1
                frame = (
                    f"id: {tick}\n"
                    f"event: telemetry\n"
                    f"data: {json.dumps({'marker': DASHBOARD_MARKER, 'seq': seq, 'tick': tick})}\n\n"
                ).encode("utf-8")
                self.wfile.write(frame)
                self.wfile.flush()
                time.sleep(0.25)
        except (BrokenPipeError, ConnectionResetError, OSError):
            pass
        finally:
            with STATE.lock:
                STATE.sse_active -= 1
                STATE.sse_closed += 1

    def _handle_websocket(self, seq: int) -> None:
        key = self.headers.get("Sec-WebSocket-Key", "").strip()
        if not key:
            self.send_error(400, "Missing Sec-WebSocket-Key")
            return
        accept = base64.b64encode(hashlib.sha1((key + WS_GUID).encode("ascii")).digest()).decode("ascii")
        with STATE.lock:
            STATE.ws_opened += 1
            STATE.ws_active += 1
        try:
            self.send_response(101, "Switching Protocols")
            self.send_header("Upgrade", "websocket")
            self.send_header("Connection", "Upgrade")
            self.send_header("Sec-WebSocket-Accept", accept)
            self.end_headers()
            self.wfile.flush()

            conn = self.connection
            conn.settimeout(0.3)
            # Send initial greeting text frame
            greeting = json.dumps({"marker": DASHBOARD_MARKER, "ws_seq": seq, "status": "connected"}).encode("utf-8")
            conn.sendall(build_ws_frame(0x1, greeting))

            while True:
                try:
                    frame = read_ws_frame(conn)
                except TimeoutError:
                    continue
                if frame is None:
                    break
                opcode, payload = frame
                if opcode == 0x8:  # Close frame (e.g. masked Close from Jul on retirement)
                    code = 1000
                    if len(payload) >= 2:
                        code = struct.unpack("!H", payload[:2])[0]
                    with STATE.lock:
                        STATE.ws_close_codes.append(code)
                    try:
                        conn.sendall(build_ws_frame(0x8, struct.pack("!H", code)))
                    except OSError:
                        pass
                    break
                elif opcode == 0x9:  # Ping -> Pong
                    conn.sendall(build_ws_frame(0xA, payload))
                elif opcode in (0x1, 0x2):  # Echo text/binary with dashboard marker
                    conn.sendall(build_ws_frame(opcode, payload))
        except OSError:
            pass
        finally:
            with STATE.lock:
                STATE.ws_active -= 1
                STATE.ws_closed += 1
            self.close_connection = True


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--listen", default="127.0.0.1:3000", help="Loopback bind address (host:port)")
    args = parser.parse_args()
    host, port_s = args.listen.rsplit(":", 1)
    server = ThreadingHTTPServer((host, int(port_s)), F1DashboardHandler)
    print(f"f1-dashboard-backend listening on http://{host}:{port_s}", flush=True)
    try:
        server.serve_forever()
    except KeyboardInterrupt:
        pass
    finally:
        server.server_close()
    return 0


if __name__ == "__main__":
    sys.exit(main())
