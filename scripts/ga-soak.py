#!/usr/bin/env python3
"""Source-bound real-package CGC-ROUTE/CGC-RES/SEC-EGRESS evidence for #590."""

import argparse
import concurrent.futures
import hashlib
import http.client
import importlib.util
import importlib.metadata
import json
import os
from pathlib import Path
import platform
import secrets
import shutil
import signal
import socket
# CLI harness only; all execution uses reviewed argument vectors without a shell.
import subprocess  # nosec B404
import threading
import time

import h2.exceptions
from ga_soak_contract import (
    API_CONFIG, AUTH_DENIED_ROUTE, AUTH_DNS_ROUTE, AUTH_ROUTE, AUTH_TIGHT_ROUTE, BEARER_PREFIX,
    CLEAN_ROUTE, CORS_ROUTE, DISCOVERY_COUNTER, DISCOVERY_ROUTE, FEATURES, HEALTH_ROUTE,
    LIMITS, PAYLOAD, PREDICATE_ROUTE, REQUIRED, SOURCE, evaluate, metrics, require, resource_budget,
)
from ga_soak_contract import slope as slope
from ga_soak_fixtures import Fixture, configuration, grpc_request, request





def digest(path):
    return hashlib.sha256(Path(path).read_bytes()).hexdigest()


def utc():
    return time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime())




def write_json(path, value):
    temporary = path.with_suffix(path.suffix + ".tmp")
    temporary.write_text(json.dumps(value, indent=2, sort_keys=True) + "\n")
    temporary.replace(path)


def command(*args, timeout=180, env=None):
    # Only operator-owned harness executables; no shell or request-derived commands.
    result = subprocess.run(list(map(str, args)), shell=False, capture_output=True, text=True, timeout=timeout, env=env)  # nosec B603
    require(result.returncode == 0, f"{Path(str(args[0])).name} failed ({result.returncode}): {result.stderr[-1000:]}")
    return result.stdout


def port():
    with socket.socket() as reservation:
        reservation.bind(("127.0.0.1", 0))
        return reservation.getsockname()[1]
















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
        headers = {"Authorization": BEARER_PREFIX + self.token}
        if content_type:
            headers["Content-Type"] = content_type
        status, body, _ = request(f'127.0.0.1:{self.ports["admin"]}', path,
                                  method="POST" if data is not None else "GET", data=data, headers=headers)
        require(status == expected, f"admin {path.split('?')[0]} HTTP {status}, expected {expected}")
        return json.loads(body) if body else None

    def http(self, path, expected=200, **kwargs):
        status, body, headers = request(f'127.0.0.1:{self.ports["http"]}', path, **kwargs)
        require(status == expected, f"{path}: HTTP {status}, expected {expected}")
        if expected == 200:
            require(body == PAYLOAD, f"{path}: response body mismatch")
        return body, headers

    def launch(self, name, *args, env=None):
        log = (self.out / (name + ".log")).open("w")
        self.logs.append(log)
        # Verified product or operator-built fixture; no request supplies executable arguments.
        proc = subprocess.Popen(list(map(str, args)), shell=False, stdout=log, stderr=subprocess.STDOUT,  # nosec B603
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
        for name in ("ga-soak.py", "ga_soak_contract.py", "ga_soak_fixtures.py",
                     "ga-soak-requirements.txt", "test_ga_soak.py",
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
                self.http(CLEAN_ROUTE)
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
                                      headers={"Authorization": BEARER_PREFIX + self.token}, timeout=10)
            require(status == 200 and len(body) > 100, f"{stage} {name} profile unavailable")
            (self.runtime / (stage + "-" + name + ".pprof")).write_bytes(body)
            profiles[name] = {"bytes": len(body), "sha256": hashlib.sha256(body).hexdigest()}
        write_json(self.out / (stage + "-profiles.json"), {"utc": utc(), "profiles": profiles})

    def tcp_round(self, sock):
        sock.sendall(PAYLOAD)
        body = b""
        while len(body) < 8:
            part = sock.recv(8 - len(body))
            require(part, "TCP echo connection unexpectedly closed")
            body += part
        require(body == PAYLOAD, "TCP echo body mismatch")

    def steady(self):
        try:
            while not self.halt.is_set():
                with self.lock:
                    _, headers = self.http(PREDICATE_ROUTE, headers={"X-Tenant": "public"})
                    values = [value for name, value in headers if name.lower() == "x-order"]
                    require(values == ["first", "second"], "ordered response policy mismatch")
                    require(not any(name.lower() == "x-powered-by" for name, _ in headers), "removed header escaped")
                    require(dict(headers).get("X-Generation") == self.generation, "response generation mismatch")
                    for path, method, headers, expected in (
                        (PREDICATE_ROUTE, "POST", {"X-Tenant": "public"}, 203),
                        ("/route/item?version=v1", "GET", {"X-Tenant": "public"}, 203),
                        (PREDICATE_ROUTE, "GET", {"X-Tenant": "private"}, 203),
                        ("/route/long/item", "GET", {}, 202), ("/missing", "GET", {}, 404),
                    ):
                        self.http(path, expected, method=method, headers=headers)
                    self.event("routing")
                    for origin, grant in (("https://soak.example", True), ("https://denied.example", False)):
                        _, headers = self.http(CORS_ROUTE, headers={"Origin": origin})
                        require(("Access-Control-Allow-Origin" in dict(headers)) == grant, "CORS grant mismatch")
                    self.http(CORS_ROUTE, 204, method="OPTIONS", headers={"Origin": "https://soak.example",
                        "Access-Control-Request-Method": "POST", "Access-Control-Request-Headers": "X-Tenant"})
                    _, headers = self.http(CORS_ROUTE, method="OPTIONS", headers={"Origin": "https://denied.example",
                        "Access-Control-Request-Method": "DELETE"})
                    require("Access-Control-Allow-Origin" not in dict(headers), "denied preflight granted")
                    self.event("cors")
                    self.http(CLEAN_ROUTE)
                    self.event("http")
                    self.http(AUTH_ROUTE)
                    self.http("/auth/reject", 403)
                    self.event("auth_allow")
                    self.http(AUTH_DENIED_ROUTE, 503)
                    self.event("auth_deny")
                    self.http(AUTH_DNS_ROUTE)
                    self.event("auth_dns")
                    self.http(DISCOVERY_ROUTE)
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
        before = self.api(API_CONFIG)
        result = self.api("/api/config/apply?base_version=" + before["base_version"], raw.encode(), "application/toml")
        require(result.get("ok"), "apply rejected or incomplete")
        require(self.api(API_CONFIG)["raw"] == raw, "applied readback differs")

    def continuing_traffic(self):
        try:
            while not self.halt.is_set():
                _, headers = self.http(PREDICATE_ROUTE, headers={"X-Tenant": "public"})
                require(dict(headers).get("X-Generation") in ("baseline", "tight"), "unknown live policy generation")
                self.http(AUTH_ROUTE)
                self.http(AUTH_DENIED_ROUTE, 503)
                self.event("traffic_during_phases")
                self.halt.wait(0.2)
        except (OSError, http.client.HTTPException, RuntimeError) as error:
            self.event("continuing_traffic_failure", False, error=str(error))

    def pool(self, name):
        pools = self.api("/api/upstreams/" + name + "/resilience")
        require(isinstance(pools, list) and pools, f"missing {name} resilience state")
        return pools[0]

    def pressure_phase(self):
        with concurrent.futures.ThreadPoolExecutor(max_workers=4) as executor:
            jobs = [executor.submit(request, f'127.0.0.1:{self.ports["http"]}', "/pressure/slow")
                    for _ in range(4)]
            time.sleep(0.1)
            state = self.pool("pressure")
            require(state["active"] == 1 and state["pending"] == 1, "pressure did not fill declared bounds")
            statuses = [job.result()[0] for job in jobs]
            require(statuses.count(200) == 1 and statuses.count(503) == 3, "pressure status attribution mismatch")
        self.event("pressure", statuses=statuses, active=state["active"], pending=state["pending"])

    def retry_phase(self):
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

    def replay_phase(self):
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

    def health_phase(self):
        self.http("/circuit/reset", 502)
        require(self.pool("circuit")["by_state"].get("circuit_open", 0) == 1, "circuit did not open")
        time.sleep(1.2)
        self.http("/circuit/ok")
        require(self.pool("circuit")["by_state"].get("available", 0) == 1, "circuit did not recover")
        self.event("circuit_recovery")
        self.fixture.healthy = False
        time.sleep(1.2)
        require(self.pool("health")["eligible"] == 0, "active health did not exclude backend")
        self.http(HEALTH_ROUTE, 503)
        self.fixture.healthy = True
        time.sleep(1.2)
        self.http(HEALTH_ROUTE)
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

    def l4_phase(self):
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

    def redirect_phase(self):
        self.http("/auth/redirect", 302)
        self.fixture.redirect = True
        before = self.fixture.snapshot().get(DISCOVERY_COUNTER, 0)
        time.sleep(2.2)
        require(self.fixture.snapshot().get(DISCOVERY_COUNTER, 0) > before,
                "discovery redirect was not requested")
        self.http(DISCOVERY_ROUTE)
        self.fixture.redirect = False
        self.event("redirect")

    def tighten_phase(self):
        original = self.api(API_CONFIG)["raw"]
        tight = original.replace(
            'allow = ["127.0.0.1/32", "127.0.0.3/32", "::1/128"]',
            'allow = ["::1/128"]').replace('value = "baseline"', 'value = "tight"')
        self.apply(tight)
        self.generation = "tight"
        _, headers = self.http(PREDICATE_ROUTE, headers={"X-Tenant": "public"})
        require(dict(headers).get("X-Generation") == "tight", "routing policy did not publish")
        self.http(AUTH_TIGHT_ROUTE, 503)
        self.http(AUTH_DNS_ROUTE, 503)
        self.event("dns_deny")
        time.sleep(1.2)
        before = self.fixture.snapshot().get(DISCOVERY_COUNTER, 0)
        self.fixture.discovery_port = self.ports["backend_b"]
        time.sleep(2.2)
        require(self.fixture.snapshot().get(DISCOVERY_COUNTER, 0) == before,
                "tightened discovery reused previous-generation connection")
        state = self.pool("discovery")
        require(all(backend["address"] == f'127.0.0.1:{self.ports["backend_a"]}'
                    for backend in state["backends"]), "blocked discovery changed last-good set")
        self.event("tighten", discovery_backends=len(state["backends"]))
        return original, tight

    def invalid_candidate_phase(self, tight):
        before_config = self.api(API_CONFIG)
        status, _, _ = request(f'127.0.0.1:{self.ports["admin"]}', "/api/config/apply",
            method="POST", data=tight.replace('"::1/128"', '"http://invalid"').encode(),
            headers={"Authorization": BEARER_PREFIX + self.token, "Content-Type": "application/toml"})
        require(status in (400, 422), f"invalid candidate HTTP {status}")
        require(self.api(API_CONFIG)["base_version"] == before_config["base_version"],
                "rejected candidate changed generation")
        self.http(AUTH_TIGHT_ROUTE, 503)
        self.event("invalid_candidate", status=status)

    def rollback_phase(self, original):
        history = self.api("/api/config/history")
        require(history, "rollback snapshot missing")
        self.api("/api/config/rollback", json.dumps({"id": history[0]["id"]}).encode(), "application/json")
        require(self.api(API_CONFIG)["raw"] == original, "rollback did not restore exact policy")
        self.generation = "baseline"
        self.fixture.discovery_port = self.ports["backend_a"]
        self.http(AUTH_TIGHT_ROUTE)
        _, headers = self.http(PREDICATE_ROUTE, headers={"X-Tenant": "public"})
        require(dict(headers).get("X-Generation") == "baseline", "routing rollback did not publish")
        self.tcp_round(self.long_tcp)
        self.event("rollback")

    def phases(self):
        try:
            while not self.halt.is_set():
                cycle_start = time.monotonic()
                with self.lock:
                    self.pressure_phase()
                    self.retry_phase()
                    self.replay_phase()
                    self.health_phase()
                    self.grpc_pressure()
                    self.l4_phase()
                    self.redirect_phase()
                    original, tight = self.tighten_phase()
                    self.invalid_candidate_phase(tight)
                    self.rollback_phase(original)
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
                                headers={"Authorization": BEARER_PREFIX + self.token})
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

    def measure_workload(self):
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

    def quiesce(self):
        quiet_end = time.monotonic() + (0 if any(not e["ok"] for e in self.events) else LIMITS["quiet_seconds"])
        while time.monotonic() < quiet_end:
            time.sleep(LIMITS["sample_seconds"])
            self.sample(quiet=True)

    def cleanup(self):
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

    def execute(self):
        require(not self.out.exists(), "refusing to overwrite a run directory")
        try:
            self.prepare()
            signal.signal(signal.SIGTERM, lambda *_: self.halt.set())
            signal.signal(signal.SIGINT, lambda *_: self.halt.set())
            self.measure_workload()
            self.quiesce()
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
            self.cleanup()


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
