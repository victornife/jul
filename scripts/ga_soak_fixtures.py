"""Owned loopback HTTP/TCP fixtures and native h2c probes for #590."""

import http.client
import http.server
import json
import socket
import socketserver
import threading
import time

import h2.config
import h2.connection
import h2.events

from ga_soak_contract import PAYLOAD, require

REDIRECT_HEADERS = {"Location": "http://127.0.0.2:1/blocked"}


def request(address, path, *, method="GET", data=None, headers=None, timeout=3):
    connection = http.client.HTTPConnection(address, timeout=timeout)
    try:
        connection.request(method, path, body=data, headers=headers or {})
        response = connection.getresponse()
        return response.status, response.read(), response.getheaders()
    finally:
        connection.close()


def grpc_headers(headers, state):
    values = dict(headers)
    if "grpc-status" in values:
        state["status"] = int(values["grpc-status"])
    if ":status" in values:
        state["http_status"] = int(values[":status"])


def grpc_events(connection, events, state):
    for event in events:
        if isinstance(event, (h2.events.ResponseReceived, h2.events.TrailersReceived)):
            grpc_headers(event.headers, state)
        if isinstance(event, h2.events.DataReceived):
            state["body"] += event.data
            connection.acknowledge_received_data(event.flow_controlled_length, event.stream_id)
        if isinstance(event, h2.events.StreamReset):
            raise RuntimeError(f"unexpected gRPC reset {event.error_code}")
        if isinstance(event, h2.events.StreamEnded):
            return True
    return False


def grpc_request(address, *, hold=False, message=b"soak", timeout=3):
    sock = socket.create_connection(("127.0.0.1", address), timeout=timeout)
    connection = h2.connection.H2Connection(config=h2.config.H2Configuration(
        client_side=True, header_encoding="utf-8"))
    connection.initiate_connection()
    connection.send_headers(1, [
        (":method", "POST"), (":scheme", "http"), (":authority", "localhost"),
        (":path", "/echo.EchoService/Echo"), ("content-type", "application/grpc"),
        ("te", "trailers"),
    ])
    if not hold:
        require(len(message) < 128, "fixture protobuf string must fit one varint byte")
        protobuf = b"\x0a" + bytes([len(message)]) + message
        connection.send_data(1, b"\x00" + len(protobuf).to_bytes(4, "big") + protobuf, end_stream=True)
    sock.sendall(connection.data_to_send())
    if hold:
        return sock, connection
    state = {"status": None, "http_status": None, "body": b""}
    try:
        while True:
            data = sock.recv(65536)
            require(data, "gRPC connection closed without final trailers")
            if grpc_events(connection, connection.receive_data(data), state):
                return state["http_status"], state["status"], state["body"]
            pending = connection.data_to_send()
            if pending:
                sock.sendall(pending)
    finally:
        sock.close()


class FixtureHandler(http.server.BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

    def do_POST(self):
        self.do_GET()

    def do_OPTIONS(self):
        self.do_GET()

    def do_GET(self):
        path = self.path.split("?", 1)[0]
        self.server.fixture.count(self.server.role + ":" + path)
        if self.server.role == "forbidden":
            self.reply(200, b"forbidden-contact")
        elif self.server.role == "control" and path.startswith("/v1/health/service/"):
            self.discovery_response()
        elif self.server.role in ("control", "auth"):
            self.auth_response()
        else:
            self.backend_response(path)

    def discovery_response(self):
        fixture = self.server.fixture
        if fixture.redirect:
            self.reply(302, b"", REDIRECT_HEADERS)
            return
        body = [{"Service": {"ID": "soak", "Address": "127.0.0.1",
                             "Port": fixture.discovery_port, "Weights": {"Passing": 1}}}]
        self.reply(200, json.dumps(body).encode())

    def auth_response(self):
        original = self.headers.get("X-Forwarded-Uri", "")
        if "redirect" in original:
            self.reply(302, b"", REDIRECT_HEADERS)
        elif "reject" in original:
            self.reply(403, b"")
        else:
            self.reply(200, b"")

    def backend_response(self, path):
        if path.endswith(("/reset", "/unsafe")):
            if self.command == "POST":
                self.rfile.read(int(self.headers.get("Content-Length", "0")))
            self.connection.shutdown(socket.SHUT_RDWR)
            self.connection.close()
            self.close_connection = True
        elif path.endswith("/committed"):
            self.committed_response()
        elif path == "/health" and not self.server.fixture.healthy:
            self.reply(503, b"")
        else:
            if path.endswith("/slow"):
                time.sleep(1)
            self.reply(200, PAYLOAD, {"X-Powered-By": "fixture"})

    def committed_response(self):
        self.send_response(200)
        self.send_header("Content-Length", "8192")
        self.end_headers()
        self.wfile.write(b"x" * 4096)
        self.wfile.flush()
        time.sleep(0.05)
        self.connection.shutdown(socket.SHUT_RDWR)
        self.close_connection = True

    def reply(self, status, body, headers=None):
        self.send_response(status)
        self.send_header("Content-Length", str(len(body)))
        for name, value in (headers or {}).items():
            self.send_header(name, value)
        self.end_headers()
        try:
            self.wfile.write(body)
        except (BrokenPipeError, ConnectionResetError):
            self.server.fixture.count("client_cancelled")

    def log_message(self, *_):
        # Fixture counts, not request logging, are the retained workload evidence.
        return


class IPv6Server(http.server.ThreadingHTTPServer):
    address_family = socket.AF_INET6


class EchoHandler(socketserver.BaseRequestHandler):
    def handle(self):
        fixture = self.server.fixture
        fixture.count("tcp_connections")
        if fixture.tcp_fault:
            return
        self.request.settimeout(90)
        while True:
            try:
                data = self.request.recv(1024)
                if not data:
                    return
                self.request.sendall(data)
                fixture.count("tcp_rounds")
            except (TimeoutError, ConnectionResetError, BrokenPipeError):
                return


class Fixture:
    def __init__(self):
        self.lock = threading.Lock()
        self.counts = {}
        self.discovery_port = 0
        self.redirect = False
        self.tcp_fault = False
        self.healthy = True
        self.servers = []

    def count(self, name):
        with self.lock:
            self.counts[name] = self.counts.get(name, 0) + 1

    def snapshot(self):
        with self.lock:
            return dict(self.counts)

    def start_server(self, server, role=None):
        server.fixture = self
        server.role = role
        self.servers.append(server)
        threading.Thread(target=server.serve_forever, daemon=True).start()
        return server.server_address[1]

    def http(self, host, role):
        server_type = IPv6Server if ":" in host else http.server.ThreadingHTTPServer
        return self.start_server(server_type((host, 0), FixtureHandler), role)

    def tcp(self):
        server = socketserver.ThreadingTCPServer(("127.0.0.1", 0), EchoHandler)
        server.daemon_threads = True
        return self.start_server(server)

    def close(self):
        for server in self.servers:
            server.shutdown()
            server.server_close()
