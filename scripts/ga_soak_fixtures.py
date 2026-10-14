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

from ga_soak_contract import (
    AUTH_DENIED_ROUTE, AUTH_DNS_ROUTE, AUTH_ROUTE, AUTH_TIGHT_ROUTE, CLEAN_ROUTE,
    DISCOVERY_ROUTE, HEALTH_ROUTE, PAYLOAD, require,
)

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


def configuration(ports, runtime):
    q = json.dumps
    raw = f'''[global]
log_level = "warn"
config_authority = "managed"
[admin]
enabled = true
console = false
listen = "127.0.0.1:{ports["admin"]}"
token = "${{env:JUL_GA_SOAK_TOKEN}}"
history_dir = {q(str(runtime / "history"))}
history_keep = 10
pprof = true
rate_limit_read_per_min = 6000
rate_limit_apply_per_min = 300
[observability.access_log]
sinks = ["file"]
file = {q(str(runtime / "access.log"))}
format = "json"
rotate_max_mb = 16
rotate_keep = 4
[egress]
enabled = true
allow = ["127.0.0.1/32", "127.0.0.3/32", "::1/128"]
'''
    for name, addresses, extra in (
        ("clean", [ports["backend_a"]], "max_active_requests = 4"),
        ("pressure", [ports["backend_a"]], '''max_active_requests = 1
max_active_per_backend = 1
max_pending_requests = 1
pending_timeout = "200ms"
max_connections_per_backend = 1'''),
        ("retry", [ports["backend_a"], ports["backend_b"]], '''max_active_requests = 4
retry_attempts = 2
retry_deadline = "500ms"
retry_backoff_initial = "5ms"
retry_backoff_max = "10ms"
retry_budget_percent = 10
max_fails = 10000'''),
        ("circuit", [ports["backend_b"]], '''max_active_requests = 2
max_fails = 1
fail_timeout = "1s"
circuit_half_open_probes = 1'''),
        ("deadline", [ports["backend_b"]], '''max_active_requests = 2
retry_deadline = "150ms"
max_fails = 10000'''),
        ("replay", [ports["backend_a"], ports["backend_b"]], "max_active_requests = 2\nretry_attempts = 2\nmax_fails = 10000"),
        ("health", [ports["backend_b"]], "max_active_requests = 2"),
        ("grpc", [ports["grpc_backend"]], "max_active_requests = 1\nmax_pending_requests = 1\npending_timeout = \"200ms\""),
        ("l4", [ports["tcp_backend"]], "max_active_requests = 2\nfail_timeout = \"1s\""),
        ("l4fault", [ports["tcp_backend"]], "max_active_requests = 1\nfail_timeout = \"1s\""),
    ):
        raw += f'\n[[upstreams]]\nname = "{name}"\nservers = {q(["127.0.0.1:" + str(p) for p in addresses])}\n'
        raw += "[upstreams.resilience]\n" + extra + "\n"
        if name == "health":
            raw += '''[upstreams.health_check]
enabled = true
type = "http"
path = "/health"
interval = "1s"
timeout = "200ms"
healthy_threshold = 1
unhealthy_threshold = 1
'''
    raw += f'''
[[upstreams]]
name = "discovery"
servers = ["127.0.0.1:{ports["backend_a"]}"]
[upstreams.discovery]
type = "consul"
refresh = "1s"
[upstreams.discovery.consul]
address = "http://127.0.0.3:{ports["control"]}"
service = "soak"
[[servers]]
listen = "127.0.0.1:{ports["http"]}"
server_names = ["localhost"]
'''
    for path, pool in ((CLEAN_ROUTE, "clean"), ("/pressure/", "pressure"),
                       ("/retry/", "retry"), ("/circuit/", "circuit"), ("/deadline/", "deadline"),
                       ("/replay/", "replay"), (HEALTH_ROUTE, "health"),
                       (DISCOVERY_ROUTE, "discovery")):
        raw += f'\n[[servers.locations]]\nmatch = {{type = "prefix", path = "{path}"}}\nproxy_pass = "http://{pool}"\n'
    raw += '''
[[servers.locations]]
proxy_pass = "http://clean"
[servers.locations.match]
type = "exact"
path = "/route/item"
methods = ["GET"]
headers = [{name = "X-Tenant", op = "exact", value = "public"}]
query = [{name = "version", op = "exact", value = "v2"}]
[[servers.locations.response_headers]]
op = "set"
name = "X-Order"
value = "first"
[[servers.locations.response_headers]]
op = "add"
name = "X-Order"
value = "second"
[[servers.locations.response_headers]]
op = "remove"
name = "X-Powered-By"
[[servers.locations.response_headers]]
op = "set"
name = "X-Generation"
value = "baseline"
[[servers.locations]]
match = {type = "prefix", path = "/route/long/"}
return = 202
[[servers.locations]]
match = {type = "regex", path = "^/route/"}
return = 203
[[servers.locations]]
match = {type = "prefix", path = "/cors/"}
proxy_pass = "http://clean"
[servers.locations.cors]
enabled = true
allowed_origins = ["https://soak.example"]
allowed_methods = ["GET", "POST"]
allowed_headers = ["X-Tenant"]
max_age = "1m"
'''
    for path, host, p in ((AUTH_ROUTE, "[::1]", ports["auth"]),
                          (AUTH_DNS_ROUTE, "localhost", ports["auth_dns"]),
                          (AUTH_TIGHT_ROUTE, "127.0.0.3", ports["control"]),
                          (AUTH_DENIED_ROUTE, "127.0.0.2", ports["forbidden"])):
        raw += f'''
[[servers.locations]]
match = {{type = "prefix", path = "{path}"}}
proxy_pass = "http://clean"
[servers.locations.auth.forward_auth]
url = "http://{host}:{p}/check"
timeout = "500ms"
'''
    raw += f'''
[[servers.locations]]
match = {{type = "prefix", path = "/"}}
return = 404
[[servers]]
listen = "127.0.0.1:{ports["grpc"]}"
h2c = true
[[servers.locations]]
match = {{type = "prefix", path = "/"}}
grpc = true
proxy_pass = "http://grpc"
[[stream]]
listen = "127.0.0.1:{ports["l4"]}"
proxy_pass = "l4"
idle_timeout = "2m"
[[stream]]
listen = "127.0.0.1:{ports["l4fault"]}"
proxy_pass = "l4fault"
idle_timeout = "2m"
'''
    return raw
