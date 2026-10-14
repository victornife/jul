#!/usr/bin/env python3
"""Source-bound real-package CGC-ROUTE/CGC-RES/SEC-EGRESS evidence for #590."""

import argparse
import concurrent.futures
import hashlib
import http.client
import http.server
import importlib.util
import importlib.metadata
import json
import math
import os
from pathlib import Path
import platform
import secrets
import shutil
import signal
import socket
import socketserver
import subprocess
import threading
import time

import h2.config
import h2.connection
import h2.events
import h2.exceptions

SOURCE = "b12e051139f652b400cff37d9f063f5637a7d550"
FEATURES = ["CGC-ROUTE", "CGC-RES", "SEC-EGRESS"]
REQUIRED = {
    "routing", "cors", "http", "grpc", "l4", "auth_allow", "auth_deny", "auth_dns",
    "discovery", "pressure", "retry_budget", "retry_deadline", "circuit_recovery", "health_recovery", "unsafe_post",
    "committed_response", "client_cancel", "grpc_pressure_cancel", "l4_pressure",
    "l4_fault_recovery", "tighten", "dns_deny", "invalid_candidate", "rollback", "redirect",
}
LIMITS = {
    "sample_seconds": 10, "max_gap_seconds": 25, "cycle_seconds": 60,
    "max_cycle_gap_seconds": 90, "warmup_seconds": 600,
    "rss_ceiling_bytes": 1024 << 20, "rss_growth_bytes": 128 << 20,
    "rss_slope_bytes_per_hour": 16 << 20, "heap_growth_bytes": 128 << 20,
    "goroutine_growth": 96, "fd_growth": 8, "series_growth": 128,
    "series_ceiling": 3000, "artifact_ceiling_bytes": 2 << 30,
    "quiet_seconds": 120,
}


def require(condition, message):
    if not condition:
        raise RuntimeError(message)


def digest(path):
    return hashlib.sha256(Path(path).read_bytes()).hexdigest()


def utc():
    return time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime())


def resource_budget():
    group = next(line.split("::", 1)[1] for line in Path("/proc/self/cgroup").read_text().splitlines()
                 if line.startswith("0::"))
    directory = Path("/sys/fs/cgroup") / group.lstrip("/")
    cpu = (directory / "cpu.max").read_text().split()
    memory = (directory / "memory.max").read_text().strip()
    return {"cpu_max": " ".join(cpu), "memory_max": memory,
            "enforced": cpu[0] != "max" and int(cpu[0]) / int(cpu[1]) <= 2
            and memory != "max" and int(memory) <= 2 << 30}


def write_json(path, value):
    temporary = path.with_suffix(path.suffix + ".tmp")
    temporary.write_text(json.dumps(value, indent=2, sort_keys=True) + "\n")
    temporary.replace(path)


def command(*args, timeout=180, env=None):
    result = subprocess.run(list(map(str, args)), capture_output=True, text=True, timeout=timeout, env=env)
    require(result.returncode == 0, f"{Path(str(args[0])).name} failed ({result.returncode}): {result.stderr[-1000:]}")
    return result.stdout


def port():
    with socket.socket() as reservation:
        reservation.bind(("127.0.0.1", 0))
        return reservation.getsockname()[1]


def request(address, path, method="GET", data=None, headers=None, timeout=3):
    connection = http.client.HTTPConnection(address, timeout=timeout)
    try:
        connection.request(method, path, body=data, headers=headers or {})
        response = connection.getresponse()
        return response.status, response.read(), response.getheaders()
    finally:
        connection.close()


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
    body = b""
    status = None
    http_status = None
    try:
        while True:
            data = sock.recv(65536)
            require(data, "gRPC connection closed without final trailers")
            for event in connection.receive_data(data):
                if isinstance(event, (h2.events.ResponseReceived, h2.events.TrailersReceived)):
                    for name, value in event.headers:
                        if name == "grpc-status":
                            status = int(value)
                        if name == ":status":
                            http_status = int(value)
                if isinstance(event, h2.events.DataReceived):
                    body += event.data
                    connection.acknowledge_received_data(event.flow_controlled_length, event.stream_id)
                if isinstance(event, h2.events.StreamReset):
                    raise RuntimeError(f"unexpected gRPC reset {event.error_code}")
                if isinstance(event, h2.events.StreamEnded):
                    return http_status, status, body
            pending = connection.data_to_send()
            if pending:
                sock.sendall(pending)
    finally:
        sock.close()


def metrics(raw):
    result = {}
    for line in raw.decode().splitlines():
        if not line or line.startswith("#"):
            continue
        key, value = line.rsplit(" ", 1)
        if "_bucket{" not in key:
            result[key] = float(value)
    for key in ("process_resident_memory_bytes", "go_goroutines", "go_memstats_heap_inuse_bytes",
                "process_open_fds"):
        require(key in result and math.isfinite(result[key]), f"missing/invalid metric {key}")
    return result


def slope(samples, key):
    origin = samples[0]["elapsed"]
    xs = [(sample["elapsed"] - origin) / 3600 for sample in samples]
    ys = [sample["metrics"][key] for sample in samples]
    mean_x, mean_y = sum(xs) / len(xs), sum(ys) / len(ys)
    denominator = sum((x - mean_x) ** 2 for x in xs)
    require(denominator > 0, "insufficient time span for resource trend")
    return sum((x - mean_x) * (y - mean_y) for x, y in zip(xs, ys)) / denominator


def evaluate(manifest, samples, events, quiet):
    errors = []
    elapsed = samples[-1]["elapsed"] if samples else 0
    if elapsed < manifest["seconds"]:
        errors.append("measured duration shorter than declared duration")
    if len(samples) < math.floor(manifest["seconds"] / LIMITS["sample_seconds"]) + 1:
        errors.append("required sample count missing")
    if not samples or samples[0]["elapsed"] > LIMITS["max_gap_seconds"]:
        errors.append("initial measured sample missing")
    if any(b["elapsed"] - a["elapsed"] > LIMITS["max_gap_seconds"]
           or b["elapsed"] <= a["elapsed"] for a, b in zip(samples, samples[1:])):
        errors.append("recording gap/non-monotonic time")
    for name in REQUIRED:
        times = [event["elapsed"] for event in events if event["name"] == name and event["ok"]]
        if not times:
            errors.append(f"missing phase: {name}")
        elif (times[0] > LIMITS["max_cycle_gap_seconds"]
              or elapsed - times[-1] > LIMITS["max_cycle_gap_seconds"]
              or any(b - a > LIMITS["max_cycle_gap_seconds"] for a, b in zip(times, times[1:]))):
            errors.append(f"phase not continuously repeated: {name}")
    if any(not event["ok"] for event in events):
        errors.append("unexpected workload failure")
    warmup = min(LIMITS["warmup_seconds"], manifest["seconds"] / 3)
    warm = [sample for sample in samples if sample["elapsed"] <= warmup]
    active = [sample for sample in samples if sample["elapsed"] >= warmup]
    if not warm or len(active) < 2:
        errors.append("insufficient resource samples")
    else:
        baseline = {key: max(sample["metrics"][key] for sample in warm)
                    for key in ("process_resident_memory_bytes", "go_memstats_heap_inuse_bytes",
                                "go_goroutines", "process_open_fds")}
        for key, limit in (("process_resident_memory_bytes", LIMITS["rss_growth_bytes"]),
                           ("go_memstats_heap_inuse_bytes", LIMITS["heap_growth_bytes"]),
                           ("go_goroutines", LIMITS["goroutine_growth"])):
            if max(sample["metrics"][key] for sample in active) > baseline[key] + limit:
                errors.append(f"post-warmup growth: {key}")
        if max(sample["metrics"]["process_resident_memory_bytes"] for sample in samples) > LIMITS["rss_ceiling_bytes"]:
            errors.append("RSS ceiling exceeded")
        tail = [sample for sample in active if sample["elapsed"] >= manifest["seconds"] / 2]
        if manifest["seconds"] >= 3600 and (
                len(tail) < 2 or slope(tail, "process_resident_memory_bytes") > LIMITS["rss_slope_bytes_per_hour"]):
            errors.append("RSS trend exceeded/unavailable")
        initial_series = set().union(*(sample["metrics"] for sample in warm))
        all_series = set().union(*(sample["metrics"] for sample in samples))
        if len(all_series) > min(LIMITS["series_ceiling"], len(initial_series) + LIMITS["series_growth"]):
            errors.append("metric cardinality exceeded")
        if len(quiet) < 3 or quiet[-1]["elapsed"] - quiet[0]["elapsed"] < 20:
            errors.append("final quiescent evidence missing")
        else:
            for sample in quiet:
                values = sample["metrics"]
                if values["process_open_fds"] > baseline["process_open_fds"] + LIMITS["fd_growth"]:
                    errors.append("quiescent FD growth")
                if values["go_goroutines"] > baseline["go_goroutines"] + LIMITS["goroutine_growth"]:
                    errors.append("quiescent goroutine growth")
                if any(value != 0 for key, value in values.items()
                       if key.startswith(("jul_upstream_active_requests{", "jul_upstream_pending_requests{"))):
                    errors.append("quiescent admission leak")
    return {"passed": not errors, "criterion_5_candidate": not errors and manifest["seconds"] >= 28800,
            "measured_seconds": elapsed, "samples": len(samples), "errors": sorted(set(errors)),
            "resource_trend_assessed": manifest["seconds"] >= 3600,
            "features": FEATURES, "automatic_promotion": False}


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

    def http(self, host, role):
        fixture = self

        class Handler(http.server.BaseHTTPRequestHandler):
            protocol_version = "HTTP/1.1"

            def do_POST(self):
                self.do_GET()

            def do_OPTIONS(self):
                self.do_GET()

            def do_GET(self):
                path = self.path.split("?", 1)[0]
                fixture.count(role + ":" + path)
                if role == "forbidden":
                    self.reply(200, b"forbidden-contact")
                elif role == "control" and path.startswith("/v1/health/service/"):
                    if fixture.redirect:
                        self.reply(302, b"", {"Location": "http://127.0.0.2:1/blocked"})
                    else:
                        self.reply(200, json.dumps([{"Service": {"ID": "soak",
                            "Address": "127.0.0.1", "Port": fixture.discovery_port,
                            "Weights": {"Passing": 1}}}]).encode())
                elif role in ("control", "auth"):
                    original = self.headers.get("X-Forwarded-Uri", "")
                    if "redirect" in original:
                        self.reply(302, b"", {"Location": "http://127.0.0.2:1/blocked"})
                    elif "reject" in original:
                        self.reply(403, b"")
                    else:
                        self.reply(200, b"")
                elif path.endswith(("/reset", "/unsafe")):
                    if self.command == "POST":
                        self.rfile.read(int(self.headers.get("Content-Length", "0")))
                    self.connection.shutdown(socket.SHUT_RDWR)
                    self.connection.close()
                    self.close_connection = True
                elif path.endswith("/committed"):
                    self.send_response(200)
                    self.send_header("Content-Length", "8192")
                    self.end_headers()
                    self.wfile.write(b"x" * 4096)
                    self.wfile.flush()
                    time.sleep(0.05)
                    self.connection.shutdown(socket.SHUT_RDWR)
                    self.close_connection = True
                else:
                    if path == "/health" and not fixture.healthy:
                        self.reply(503, b"")
                        return
                    if path.endswith("/slow"):
                        time.sleep(1)
                    self.reply(200, b"soak590\n", {"X-Powered-By": "fixture"})

            def reply(self, status, body, headers=None):
                self.send_response(status)
                self.send_header("Content-Length", str(len(body)))
                for name, value in (headers or {}).items():
                    self.send_header(name, value)
                self.end_headers()
                try:
                    self.wfile.write(body)
                except (BrokenPipeError, ConnectionResetError):
                    fixture.count("client_cancelled")

            def log_message(self, *_):
                pass

        class IPv6Server(http.server.ThreadingHTTPServer):
            address_family = socket.AF_INET6

        server_type = IPv6Server if ":" in host else http.server.ThreadingHTTPServer
        server = server_type((host, 0), Handler)
        self.servers.append(server)
        threading.Thread(target=server.serve_forever, daemon=True).start()
        return server.server_port

    def tcp(self):
        fixture = self

        class Echo(socketserver.BaseRequestHandler):
            def handle(self):
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

        server = socketserver.ThreadingTCPServer(("127.0.0.1", 0), Echo)
        server.daemon_threads = True
        self.servers.append(server)
        threading.Thread(target=server.serve_forever, daemon=True).start()
        return server.server_address[1]

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
    for path, pool in (("/clean/", "clean"), ("/pressure/", "pressure"),
                       ("/retry/", "retry"), ("/circuit/", "circuit"), ("/deadline/", "deadline"),
                       ("/replay/", "replay"), ("/healthprobe/", "health"),
                       ("/discovery/", "discovery")):
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
    for path, host, p in (("/auth/", "[::1]", ports["auth"]),
                          ("/auth-dns/", "localhost", ports["auth_dns"]),
                          ("/auth-tight/", "127.0.0.3", ports["control"]),
                          ("/auth-denied/", "127.0.0.2", ports["forbidden"])):
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


class Run:
    def __init__(self, args):
        self.args = args
        self.out = args.out.resolve()
        self.runtime = self.out / "private-runtime"
        self.token = secrets.token_urlsafe(36)
        self.halt = threading.Event()
        self.events = []
        self.samples = []
        self.quiet = []
        self.lock = threading.RLock()
        self.event_lock = threading.Lock()
        self.fixture = Fixture()
        self.procs = []
        self.logs = []
        self.threads = []
        self.long_tcp = None
        self.start = None
        self.generation = "baseline"

    def event(self, name, ok=True, **details):
        event = {"utc": utc(), "elapsed": time.monotonic() - self.start, "name": name,
                 "ok": ok, **details}
        with self.event_lock:
            self.events.append(event)
            with (self.out / "events.jsonl").open("a") as output:
                output.write(json.dumps(event, sort_keys=True) + "\n")
        if not ok:
            self.halt.set()

    def api(self, path, data=None, content_type=None, expected=200):
        headers = {"Authorization": "Bearer " + self.token}
        if content_type:
            headers["Content-Type"] = content_type
        status, body, _ = request(f'127.0.0.1:{self.ports["admin"]}', path,
                                  "POST" if data is not None else "GET", data, headers)
        require(status == expected, f"admin {path.split('?')[0]} HTTP {status}, expected {expected}")
        return json.loads(body) if body else None

    def http(self, path, expected=200, **kwargs):
        status, body, headers = request(f'127.0.0.1:{self.ports["http"]}', path, **kwargs)
        require(status == expected, f"{path}: HTTP {status}, expected {expected}")
        if expected == 200:
            require(body == b"soak590\n", f"{path}: response body mismatch")
        return body, headers

    def launch(self, name, *args, env=None):
        log = (self.out / (name + ".log")).open("w")
        self.logs.append(log)
        proc = subprocess.Popen(list(map(str, args)), stdout=log, stderr=subprocess.STDOUT,
                                cwd=self.runtime, env=env)
        self.procs.append(proc)
        return proc

    def prepare(self):
        require(not self.out.exists(), "refusing to overwrite a run directory")
        self.runtime.mkdir(parents=True, mode=0o700)
        self.out.chmod(0o700)
        budget = resource_budget()
        require(self.args.seconds < 28800 or budget["enforced"],
                "eight-hour run requires an enforced <=2-CPU/2-GiB cgroup")
        verifier_spec = importlib.util.spec_from_file_location(
            "packages", Path(__file__).with_name("verify-release-packages.py"))
        verifier = importlib.util.module_from_spec(verifier_spec)
        verifier_spec.loader.exec_module(verifier)
        binary, identity = verifier.verify_archive(self.args.package_directory.resolve(), "v2.2.0",
                                                    SOURCE, "linux", self.args.arch, "full")
        self.binary = binary
        capabilities = json.loads(command(binary, "capabilities", "-json"))
        require(all(capabilities["features"].get(name) for name in ("grpc", "stream_proxy", "consul")),
                "full package missing required capabilities")
        self.ports = {name: port() for name in ("admin", "http", "grpc", "l4", "l4fault", "grpc_backend")}
        require(len(set(self.ports.values())) == len(self.ports), "listener reservation collision")
        for name, host, role in (
            ("backend_a", "127.0.0.1", "backend_a"), ("backend_b", "127.0.0.1", "backend_b"),
            ("auth", "::1", "auth"), ("auth_dns", "127.0.0.1", "auth"),
            ("control", "127.0.0.3", "control"),
            ("forbidden", "127.0.0.2", "forbidden"),
        ):
            self.ports[name] = self.fixture.http(host, role)
        self.ports["tcp_backend"] = self.fixture.tcp()
        self.fixture.discovery_port = self.ports["backend_a"]
        self.raw = configuration(self.ports, self.runtime)
        config = self.runtime / "server.toml"
        config.write_text(self.raw)
        command(binary, "check", "-config", config, env={**os.environ, "JUL_GA_SOAK_TOKEN": self.token})
        self.manifest = {
            "features": FEATURES, "seconds": self.args.seconds, "limits": LIMITS,
            "required_phases": sorted(REQUIRED), "prepared_utc": utc(),
            "package": identity, "binary_sha256": digest(binary), "capabilities": capabilities,
            "harness_source": command("git", "rev-parse", "HEAD").strip(),
            "harness_sha256": digest(__file__), "grpc_fixture_sha256": digest(self.args.grpc_backend),
            "dependencies": {name: importlib.metadata.version(name) for name in ("h2", "hpack", "hyperframe")},
            "config_sha256": digest(config), "ports": self.ports,
            "host": {"platform": platform.platform(), "boot_id": Path("/proc/sys/kernel/random/boot_id").read_text().strip(),
                     "python": platform.python_version(), "cpu_count": os.cpu_count(),
                     "localhost_addresses": sorted({address[4][0] for address in
                         socket.getaddrinfo("localhost", 80, type=socket.SOCK_STREAM)}),
                     "cgroup": Path("/proc/self/cgroup").read_text().strip()},
            "budget": {"cpu_quota": "200%", "memory_max_bytes": 2 << 30, "evidence_max_bytes": 2 << 30,
                       **budget},
            "limitations": ["WSL2 shared-host loopback topology; any >25s recording gap fails",
                            "gRPC native h2c, TCP L4; no UDP/transcoding/stream replay certification",
                            "mock Consul protocol consumer, not a real Consul cluster",
                            "system localhost DNS only; no controlled authoritative DNS/rebinding claim",
                            "forward-auth redirect relay and discovery non-following, not JWKS redirect certification",
                            "#406 outcome retry and #522 ejection remain deferred",
                            "private profiles/config/history/logs are not public attachments"],
        }
        harness = self.out / "harness"
        harness.mkdir()
        for name in ("ga-soak.py", "ga-soak-requirements.txt", "test_ga_soak.py",
                     "verify-release-packages.py", "grpc-echo-server.go"):
            shutil.copyfile(Path(__file__).with_name(name), harness / name)
        self.manifest["harness_files"] = {path.name: digest(path) for path in harness.iterdir()}
        write_json(self.out / "manifest.json", self.manifest)
        self.launch("grpc-backend", self.args.grpc_backend, "-port", self.ports["grpc_backend"])
        self.jul = self.launch("jul", binary, "-config", config,
                               env={**os.environ, "JUL_GA_SOAK_TOKEN": self.token, "GOMAXPROCS": "2"})
        deadline = time.monotonic() + 30
        while True:
            require(all(proc.poll() is None for proc in self.procs), "process exited during startup")
            try:
                self.api("/readyz")
                self.http("/clean/")
                break
            except (OSError, http.client.HTTPException):
                require(time.monotonic() < deadline, "startup timeout")
                time.sleep(0.1)
        preview = self.api("/api/config/adopt-external/preview")
        require(preview["ok"], "baseline preview failed")
        adopted = self.api("/api/config/adopt-external", json.dumps({
            "observed_digest": preview["observed_digest"], "base_version": preview.get("base_version", ""),
            "mode": "hot", "confirm": True}).encode(), "application/json")
        require(adopted["ok"], "baseline adoption failed")
        self.long_tcp = socket.create_connection(("127.0.0.1", self.ports["l4"]), timeout=3)
        self.tcp_round(self.long_tcp)
        self.profile("T0")
        self.start = time.monotonic()
        self.manifest.update(started_utc=utc(), jul_pid=self.jul.pid, process_ids=[p.pid for p in self.procs])
        write_json(self.out / "manifest.json", self.manifest)
        write_json(self.out / "live.json", {"state": "running", "started_utc": self.manifest["started_utc"],
                                         "ports": self.ports, "jul_pid": self.jul.pid})

    def profile(self, stage):
        profiles = {}
        for name in ("heap", "goroutine"):
            status, body, _ = request(f'127.0.0.1:{self.ports["admin"]}', "/debug/pprof/" + name,
                                      headers={"Authorization": "Bearer " + self.token}, timeout=10)
            require(status == 200 and len(body) > 100, f"{stage} {name} profile unavailable")
            (self.runtime / (stage + "-" + name + ".pprof")).write_bytes(body)
            profiles[name] = {"bytes": len(body), "sha256": hashlib.sha256(body).hexdigest()}
        write_json(self.out / (stage + "-profiles.json"), {"utc": utc(), "profiles": profiles})

    def tcp_round(self, sock):
        sock.sendall(b"soak590\n")
        body = b""
        while len(body) < 8:
            part = sock.recv(8 - len(body))
            require(part, "TCP echo connection unexpectedly closed")
            body += part
        require(body == b"soak590\n", "TCP echo body mismatch")

    def steady(self):
        try:
            while not self.halt.is_set():
                with self.lock:
                    _, headers = self.http("/route/item?version=v2", headers={"X-Tenant": "public"})
                    values = [value for name, value in headers if name.lower() == "x-order"]
                    require(values == ["first", "second"], "ordered response policy mismatch")
                    require(not any(name.lower() == "x-powered-by" for name, _ in headers), "removed header escaped")
                    require(dict(headers).get("X-Generation") == self.generation, "response generation mismatch")
                    for path, method, headers, expected in (
                        ("/route/item?version=v2", "POST", {"X-Tenant": "public"}, 203),
                        ("/route/item?version=v1", "GET", {"X-Tenant": "public"}, 203),
                        ("/route/item?version=v2", "GET", {"X-Tenant": "private"}, 203),
                        ("/route/long/item", "GET", {}, 202), ("/missing", "GET", {}, 404),
                    ):
                        self.http(path, expected, method=method, headers=headers)
                    self.event("routing")
                    for origin, grant in (("https://soak.example", True), ("https://denied.example", False)):
                        _, headers = self.http("/cors/", headers={"Origin": origin})
                        require(("Access-Control-Allow-Origin" in dict(headers)) == grant, "CORS grant mismatch")
                    self.http("/cors/", 204, method="OPTIONS", headers={"Origin": "https://soak.example",
                        "Access-Control-Request-Method": "POST", "Access-Control-Request-Headers": "X-Tenant"})
                    _, headers = self.http("/cors/", method="OPTIONS", headers={"Origin": "https://denied.example",
                        "Access-Control-Request-Method": "DELETE"})
                    require("Access-Control-Allow-Origin" not in dict(headers), "denied preflight granted")
                    self.event("cors")
                    self.http("/clean/")
                    self.event("http")
                    self.http("/auth/")
                    self.http("/auth/reject", 403)
                    self.event("auth_allow")
                    self.http("/auth-denied/", 503)
                    self.event("auth_deny")
                    self.http("/auth-dns/")
                    self.event("auth_dns")
                    self.http("/discovery/")
                    self.event("discovery")
                    http_status, status, body = grpc_request(self.ports["grpc"])
                    require(http_status == 200 and status == 0
                            and body == b"\x00\x00\x00\x00\x06\x0a\x04soak", "native gRPC echo mismatch")
                    self.event("grpc")
                    self.tcp_round(self.long_tcp)
                    self.event("l4")
                self.halt.wait(0.2)
        except (OSError, http.client.HTTPException, RuntimeError, h2.exceptions.H2Error) as error:
            self.event("steady_failure", False, error=str(error))

    def apply(self, raw):
        before = self.api("/api/config")
        result = self.api("/api/config/apply?base_version=" + before["base_version"], raw.encode(), "application/toml")
        require(result.get("ok"), "apply rejected or incomplete")
        require(self.api("/api/config")["raw"] == raw, "applied readback differs")

    def continuing_traffic(self):
        try:
            while not self.halt.is_set():
                _, headers = self.http("/route/item?version=v2", headers={"X-Tenant": "public"})
                require(dict(headers).get("X-Generation") in ("baseline", "tight"), "unknown live policy generation")
                self.http("/auth/")
                self.http("/auth-denied/", 503)
                self.event("traffic_during_phases")
                self.halt.wait(0.2)
        except (OSError, http.client.HTTPException, RuntimeError) as error:
            self.event("continuing_traffic_failure", False, error=str(error))

    def pool(self, name):
        pools = self.api("/api/upstreams/" + name + "/resilience")
        require(isinstance(pools, list) and pools, f"missing {name} resilience state")
        return pools[0]

    def phases(self):
        try:
            while not self.halt.is_set():
                cycle_start = time.monotonic()
                with self.lock:
                    with concurrent.futures.ThreadPoolExecutor(max_workers=4) as executor:
                        jobs = [executor.submit(request, f'127.0.0.1:{self.ports["http"]}', "/pressure/slow")
                                for _ in range(4)]
                        time.sleep(0.1)
                        state = self.pool("pressure")
                        require(state["active"] == 1 and state["pending"] == 1, "pressure did not fill declared bounds")
                        statuses = [job.result()[0] for job in jobs]
                        require(statuses.count(200) == 1 and statuses.count(503) == 3, "pressure status attribution mismatch")
                    self.event("pressure", statuses=statuses, active=state["active"], pending=state["pending"])
                    for _ in range(20):
                        self.http("/retry/ok")
                    for _ in range(15):
                        self.http("/retry/reset", 502)
                    budget = self.pool("retry")["retry_budget"]
                    require(budget["remaining"] == 0 and budget["retries"] > 0, "retry budget not exercised/exhausted")
                    self.event("retry_budget", budget=budget)
                    started = time.monotonic()
                    self.http("/deadline/slow", 504)
                    elapsed = time.monotonic() - started
                    require(0.1 <= elapsed <= 0.8, f"Jul-owned retry deadline outside bound: {elapsed}")
                    self.event("retry_deadline", seconds=elapsed)
                    before = self.fixture.snapshot()
                    self.http("/replay/unsafe", 502, method="POST", data=b"unsafe-body")
                    after = self.fixture.snapshot()
                    attempts = sum(after.get(key, 0) - before.get(key, 0)
                                   for key in ("backend_a:/replay/unsafe", "backend_b:/replay/unsafe"))
                    require(attempts == 1, "unsafe POST replayed")
                    self.event("unsafe_post", attempts=attempts)
                    before = self.fixture.snapshot()
                    try:
                        self.http("/replay/committed")
                    except http.client.IncompleteRead as error:
                        # An aborted net/http write may leave its final buffered bytes unsent.
                        require(0 < len(error.partial) <= 4096
                                and error.partial == b"x" * len(error.partial), "committed response prefix changed")
                        delivered = len(error.partial)
                    else:
                        raise RuntimeError("partial committed response was silently completed")
                    after = self.fixture.snapshot()
                    attempts = sum(after.get(key, 0) - before.get(key, 0)
                                   for key in ("backend_a:/replay/committed", "backend_b:/replay/committed"))
                    require(attempts == 1, "committed response replayed")
                    self.event("committed_response", attempts=attempts, delivered_bytes=delivered)
                    self.http("/circuit/reset", 502)
                    require(self.pool("circuit")["by_state"].get("circuit_open", 0) == 1, "circuit did not open")
                    time.sleep(1.2)
                    self.http("/circuit/ok")
                    require(self.pool("circuit")["by_state"].get("available", 0) == 1, "circuit did not recover")
                    self.event("circuit_recovery")
                    self.fixture.healthy = False
                    time.sleep(1.2)
                    require(self.pool("health")["eligible"] == 0, "active health did not exclude backend")
                    self.http("/healthprobe/", 503)
                    self.fixture.healthy = True
                    time.sleep(1.2)
                    self.http("/healthprobe/")
                    require(self.pool("health")["eligible"] == 1, "active health did not recover backend")
                    self.event("health_recovery")
                    sock = socket.create_connection(("127.0.0.1", self.ports["http"]), timeout=3)
                    sock.sendall(b"GET /pressure/slow HTTP/1.1\r\nHost: localhost\r\n\r\n")
                    time.sleep(0.1)
                    sock.close()
                    time.sleep(1.1)
                    state = self.pool("pressure")
                    require(state["active"] == 0 and state["pending"] == 0, "client cancellation leaked admission")
                    self.event("client_cancel")
                    self.grpc_pressure()
                    held = socket.create_connection(("127.0.0.1", self.ports["l4"]), timeout=3)
                    try:
                        self.tcp_round(held)
                        over = socket.create_connection(("127.0.0.1", self.ports["l4"]), timeout=3)
                        try:
                            try:
                                over.sendall(b"x")
                                require(over.recv(1) == b"", "over-limit L4 connection served")
                            except ConnectionResetError:
                                pass
                        finally:
                            over.close()
                    finally:
                        held.close()
                    self.event("l4_pressure")
                    self.fixture.tcp_fault = True
                    fault = socket.create_connection(("127.0.0.1", self.ports["l4fault"]), timeout=3)
                    try:
                        try:
                            fault.sendall(b"x")
                            require(fault.recv(1) == b"", "injected L4 fault unexpectedly echoed")
                        except ConnectionResetError:
                            pass
                    finally:
                        fault.close()
                        self.fixture.tcp_fault = False
                    time.sleep(1.2)
                    recovered = socket.create_connection(("127.0.0.1", self.ports["l4fault"]), timeout=3)
                    try:
                        self.tcp_round(recovered)
                    finally:
                        recovered.close()
                    self.event("l4_fault_recovery")
                    self.http("/auth/redirect", 302)
                    self.fixture.redirect = True
                    before = self.fixture.snapshot().get("control:/v1/health/service/soak", 0)
                    time.sleep(2.2)
                    require(self.fixture.snapshot().get("control:/v1/health/service/soak", 0) > before,
                            "discovery redirect was not requested")
                    self.http("/discovery/")
                    self.fixture.redirect = False
                    self.event("redirect")
                    original = self.api("/api/config")["raw"]
                    tight = original.replace(
                        'allow = ["127.0.0.1/32", "127.0.0.3/32", "::1/128"]',
                        'allow = ["::1/128"]').replace('value = "baseline"', 'value = "tight"')
                    self.apply(tight)
                    self.generation = "tight"
                    _, headers = self.http("/route/item?version=v2", headers={"X-Tenant": "public"})
                    require(dict(headers).get("X-Generation") == "tight", "routing policy did not publish")
                    self.http("/auth-tight/", 503)
                    self.http("/auth-dns/", 503)
                    self.event("dns_deny")
                    time.sleep(1.2)
                    before = self.fixture.snapshot().get("control:/v1/health/service/soak", 0)
                    self.fixture.discovery_port = self.ports["backend_b"]
                    time.sleep(2.2)
                    require(self.fixture.snapshot().get("control:/v1/health/service/soak", 0) == before,
                            "tightened discovery reused previous-generation connection")
                    state = self.pool("discovery")
                    require(all(backend["address"] == f'127.0.0.1:{self.ports["backend_a"]}'
                                for backend in state["backends"]), "blocked discovery changed last-good set")
                    self.event("tighten", discovery_backends=len(state["backends"]))
                    before_config = self.api("/api/config")
                    status, _, _ = request(f'127.0.0.1:{self.ports["admin"]}', "/api/config/apply",
                        "POST", tight.replace('"::1/128"', '"http://invalid"').encode(),
                        {"Authorization": "Bearer " + self.token, "Content-Type": "application/toml"})
                    require(status in (400, 422), f"invalid candidate HTTP {status}")
                    require(self.api("/api/config")["base_version"] == before_config["base_version"],
                            "rejected candidate changed generation")
                    self.http("/auth-tight/", 503)
                    self.event("invalid_candidate", status=status)
                    history = self.api("/api/config/history")
                    require(history, "rollback snapshot missing")
                    self.api("/api/config/rollback", json.dumps({"id": history[0]["id"]}).encode(), "application/json")
                    require(self.api("/api/config")["raw"] == original, "rollback did not restore exact policy")
                    self.generation = "baseline"
                    self.fixture.discovery_port = self.ports["backend_a"]
                    self.http("/auth-tight/")
                    _, headers = self.http("/route/item?version=v2", headers={"X-Tenant": "public"})
                    require(dict(headers).get("X-Generation") == "baseline", "routing rollback did not publish")
                    self.tcp_round(self.long_tcp)
                    self.event("rollback")
                    require(not any(key.startswith("forbidden:") for key in self.fixture.snapshot()),
                            "denied auxiliary destination received bytes")
                self.halt.wait(max(0, LIMITS["cycle_seconds"] - (time.monotonic() - cycle_start)))
        except (OSError, http.client.HTTPException, RuntimeError, h2.exceptions.H2Error) as error:
            self.event("phase_failure", False, error=str(error))

    def grpc_pressure(self):
        held = []
        try:
            held.append(grpc_request(self.ports["grpc"], hold=True))
            time.sleep(0.1)
            held.append(grpc_request(self.ports["grpc"], hold=True))
            time.sleep(0.1)
            state = self.pool("grpc")
            require(state["active"] == 1 and state["pending"] == 1, "gRPC active/pending pressure missing")
            http_status, status, body = grpc_request(self.ports["grpc"])
            require(http_status == 503 and status is None and body == b"503 Service Unavailable\n",
                    f"gRPC overload wire mismatch: HTTP {http_status}, grpc-status {status}")
            for sock, connection in held:
                connection.reset_stream(1, error_code=8)
                sock.sendall(connection.data_to_send())
            time.sleep(0.2)
            state = self.pool("grpc")
            require(state["active"] == 0 and state["pending"] == 0, "gRPC cancellation leaked admission")
            self.event("grpc_pressure_cancel", http_status=http_status, client_mapped_grpc_status=14)
        finally:
            for sock, _ in held:
                sock.close()

    def sample(self, quiet=False):
        require(all(proc.poll() is None for proc in self.procs), "unexpected process exit")
        status, raw, _ = request(f'127.0.0.1:{self.ports["admin"]}', "/metrics",
                                headers={"Authorization": "Bearer " + self.token})
        require(status == 200, f"metrics HTTP {status}")
        elapsed = time.monotonic() - self.start
        sample = {"utc": utc(), "elapsed": elapsed, "walltime": time.time(),
                  "boottime": time.clock_gettime(time.CLOCK_BOOTTIME),
                  "metrics": metrics(raw), "fixture_counts": self.fixture.snapshot()}
        if self.samples:
            last = self.samples[-1]
            require((sample["boottime"] - last["boottime"]) - (elapsed - last["elapsed"]) <= 5,
                    "host suspension invalidated continuous workload")
            require(abs((sample["walltime"] - last["walltime"]) - (elapsed - last["elapsed"])) <= 5,
                    "wall-clock discontinuity invalidated continuous workload")
        sample["quiet"] = quiet
        (self.quiet if quiet else self.samples).append(sample)
        with (self.out / "samples.jsonl").open("a") as output:
            output.write(json.dumps(sample, sort_keys=True) + "\n")
        size = sum(path.stat().st_size for path in self.out.rglob("*") if path.is_file())
        require(size < LIMITS["artifact_ceiling_bytes"], "evidence budget exceeded")
        write_json(self.out / "live.json", {"state": "quiescing" if quiet else "running",
            "elapsed": elapsed, "samples": len(self.samples), "events": len(self.events),
            "artifact_bytes": size, "jul_pid": self.jul.pid, "ports": self.ports,
            "unexpected_failures": sum(not e["ok"] for e in self.events)})

    def execute(self):
        require(not self.out.exists(), "refusing to overwrite a run directory")
        try:
            self.prepare()
            signal.signal(signal.SIGTERM, lambda *_: self.halt.set())
            signal.signal(signal.SIGINT, lambda *_: self.halt.set())
            self.sample()
            for work in (self.steady, self.phases, self.continuing_traffic):
                thread = threading.Thread(target=work, daemon=True)
                self.threads.append(thread)
                thread.start()
            deadline = self.start + self.args.seconds
            next_sample = self.start + LIMITS["sample_seconds"]
            while time.monotonic() < deadline and not self.halt.is_set():
                self.halt.wait(max(0, min(next_sample, deadline) - time.monotonic()))
                self.sample()
                next_sample += LIMITS["sample_seconds"]
            self.halt.set()
            for thread in self.threads:
                thread.join(timeout=30)
                require(not thread.is_alive(), "workload worker did not stop")
            self.long_tcp.close()
            self.long_tcp = None
            quiet_end = time.monotonic() + (0 if any(not e["ok"] for e in self.events)
                                           else LIMITS["quiet_seconds"])
            while time.monotonic() < quiet_end:
                time.sleep(LIMITS["sample_seconds"])
                self.sample(quiet=True)
            self.profile("Tend")
            result = evaluate(self.manifest, self.samples, self.events, self.quiet[-3:])
            result["finished_utc"] = utc()
            write_json(self.out / "result.json", result)
            require(result["passed"], "soak rejected: " + "; ".join(result["errors"]))
        except Exception as error:
            # The top-level boundary persists a failure, never a success-shaped fallback.
            write_json(self.out / "failure.json", {"utc": utc(), "error": str(error),
                                                  "criterion_5_candidate": False})
            raise
        finally:
            if self.long_tcp:
                self.long_tcp.close()
            self.halt.set()
            for proc in reversed(self.procs):
                if proc.poll() is None:
                    proc.terminate()
                    try:
                        proc.wait(timeout=10)
                    except subprocess.TimeoutExpired:
                        proc.kill()
                        proc.wait(timeout=5)
            self.fixture.close()
            for log in self.logs:
                log.close()
            if self.out.exists():
                files = {str(path.relative_to(self.out)): digest(path) for path in self.out.rglob("*")
                         if path.is_file() and path.name != "digests.json"}
                write_json(self.out / "digests.json", files)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--package-directory", type=Path, required=True)
    parser.add_argument("--grpc-backend", type=Path, required=True)
    parser.add_argument("--out", type=Path, required=True)
    parser.add_argument("--seconds", type=int, default=28800)
    parser.add_argument("--arch", choices=["arm64", "amd64"], required=True)
    args = parser.parse_args()
    require(platform.system() == "Linux" and args.seconds >= 60, "Linux and at least 60 seconds required")
    args.grpc_backend = args.grpc_backend.resolve()
    Run(args).execute()


if __name__ == "__main__":
    main()
