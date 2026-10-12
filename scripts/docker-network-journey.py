#!/usr/bin/env python3
"""Verify frozen-source Docker deployment through a routed CI client."""

import argparse
import hashlib
import http.server
import json
import os
from pathlib import Path
import secrets
import shutil
import subprocess
import tarfile
import tempfile
import threading
import time
import uuid
import urllib.parse

from deployment_lab import RoutedClient, certificate_chain, command, write_report


SOURCE = "b12e051139f652b400cff37d9f063f5637a7d550"
FULL_TAGS = "brotli zstd acme console otel grpc http3 importer wasmplugins stream consul kubernetes waf"


def docker(*arguments, token=None):
    environment = {**os.environ, **({"JUL_ADMIN_TOKEN": token} if token else {})}
    result = subprocess.run(["docker", *map(str, arguments)], env=environment, text=True, capture_output=True, timeout=1200)
    if result.returncode:
        raise RuntimeError(f"Docker failed ({result.returncode}): {result.stderr[-2000:]}")
    return result.stdout.strip()


def port():
    import socket
    with socket.socket() as connection:
        connection.bind(("127.0.0.1", 0))
        return connection.getsockname()[1]


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--report", type=Path, required=True)
    args = parser.parse_args()
    suffix = uuid.uuid4().hex[:10]
    image, container, network = ["jul516-" + suffix + "-" + label for label in ("image", "server", "network")]
    volumes = ["jul516-" + suffix + "-" + label for label in ("state", "cache", "logs")]
    temporary = Path(tempfile.mkdtemp(prefix="jul516-docker-"))
    configuration = temporary / "config"
    backend = None
    worker = None
    try:
        command("git", "fetch", "--depth=1", "origin", "tag", "v2.2.0")
        assert command("git", "rev-parse", "v2.2.0^{commit}") == SOURCE
        archive = temporary / "source.tar"
        command("git", "archive", "--format=tar", "--output", archive, SOURCE)
        context = temporary / "source"
        context.mkdir()
        with tarfile.open(archive) as source:
            source.extractall(context, filter="data")
        wanted_go = next(line.split()[1] for line in (context / "go.mod").read_text().splitlines() if line.startswith("go "))
        assert (context / "Dockerfile").read_text().find("FROM golang:" + wanted_go + "-alpine@sha256:") >= 0
        docker("build", "--build-arg", "VERSION=v2.2.0", "--build-arg", "BUILD_TAGS=" + FULL_TAGS,
               "--label", "org.opencontainers.image.revision=" + SOURCE, "--tag", image, context)
        for volume in volumes:
            docker("volume", "create", volume)
        docker("network", "create", network)
        temporary.chmod(0o755)
        configuration.mkdir(mode=0o700)
        tls_directory = configuration / "tls"
        tls_directory.mkdir(mode=0o700)
        client_directory = temporary / "client"
        client_directory.mkdir(mode=0o700)
        with RoutedClient(client_directory) as client:
            pki = certificate_chain(temporary / "pki")
            for source, name, mode in ((pki["chain"], "chain.pem", 0o644), (pki["key"], "leaf.key", 0o600),
                                       (pki["root"], "root.pem", 0o644)):
                destination = tls_directory / name
                shutil.copyfile(source, destination)
                destination.chmod(mode)
            module = context / "testdata/plugins/v1-current-header-inject.wasm"
            module_digest = hashlib.sha256(module.read_bytes()).hexdigest()
            shutil.copyfile(module, configuration / "probe.wasm")
            (configuration / "probe.wasm").chmod(0o600)
            class Backend(http.server.BaseHTTPRequestHandler):
                def do_GET(self):
                    self.send_response(200)
                    self.end_headers()
                    self.wfile.write(b"Jul Docker proxy\n")

                def log_message(self, *unused):
                    pass

            backend = http.server.ThreadingHTTPServer(("0.0.0.0", 0), Backend)
            worker = threading.Thread(target=backend.serve_forever, daemon=True)
            worker.start()
            raw = f'''[global]
config_authority = "managed"
log_level = "info"
[plugins.probe]
path = "/etc/jul/probe.wasm"
type = "middleware"
sha256 = "sha256:{module_digest}"
timeout = "5s"
[[servers]]
listen = "0.0.0.0:8443"
server_names = ["jul516.test"]
plugins = ["probe"]
[servers.tls]
enabled = true
cert = "/etc/jul/tls/chain.pem"
key = "/etc/jul/tls/leaf.key"
[[servers.locations]]
match = {{type = "prefix", path = "/proxy/"}}
proxy_pass = "http://{client.gateway}:{backend.server_port}"
[[servers.locations]]
match = {{type = "prefix", path = "/"}}
root = "/var/www"
[admin]
enabled = true
listen = "0.0.0.0:9443"
token = "${{env:JUL_ADMIN_TOKEN}}"
history_dir = "/var/lib/jul/config-history"
[admin.tls]
enabled = true
cert = "/etc/jul/tls/chain.pem"
key = "/etc/jul/tls/leaf.key"
[observability.access_log]
sinks = ["file"]
file = "/var/log/jul/access.log"
'''
            (configuration / "server.toml").write_text(raw)
            (configuration / "server.toml").chmod(0o600)
            command("sudo", "chown", "-R", "65532:65532", configuration)
            traffic_port, admin_port = port(), port()
            while traffic_port == admin_port:
                admin_port = port()
            token = secrets.token_urlsafe(32)
            start = ("run", "--detach", "--name", container, "--network", network,
                     "--env", "JUL_ADMIN_TOKEN", "--env", "SSL_CERT_FILE=/etc/jul/tls/root.pem",
                     "--publish", f"{client.gateway}:{traffic_port}:8443", "--publish", f"{client.gateway}:{admin_port}:9443",
                     "--volume", str(configuration) + ":/etc/jul", "--volume", volumes[0] + ":/var/lib/jul",
                     "--volume", volumes[1] + ":/var/cache/jul", "--volume", volumes[2] + ":/var/log/jul", image)
            def wait_healthy():
                for _ in range(90):
                    state = json.loads(docker("inspect", container))[0]["State"]
                    if not state["Running"]:
                        raise RuntimeError("Container exited before healthy readiness")
                    if state["Health"]["Status"] == "healthy":
                        return
                    time.sleep(1)
                raise RuntimeError("TLS-aware baked exec healthcheck did not become healthy")

            def api(path, data=None, content_type=None):
                status, body, _ = client.fetch(admin_port, pki["root"], path=path, token=token,
                                                method="POST" if data is not None else "GET", data=data, content_type=content_type)
                assert status == 200, f"Admin operation returned HTTP {status}"
                return json.loads(body)

            docker(*start, token=token)
            wait_healthy()
            inspect = json.loads(docker("inspect", container))[0]
            assert inspect["Config"]["User"] == "nonroot:nonroot"
            capabilities = json.loads(docker("exec", container, "/usr/local/bin/jul", "capabilities", "-json"))["features"]
            assert capabilities["console"] and capabilities["waf"]
            status, body, headers = client.fetch(traffic_port, pki["root"])
            assert status == 200 and b"Jul" in body and headers.get("x-plugin") == "header-inject"
            status, body, _ = client.fetch(traffic_port, pki["root"], path="/proxy/")
            assert status == 200 and body == b"Jul Docker proxy\n"
            client.fetch(traffic_port, pki["wrong_root"], failure=60)
            assert client.fetch(admin_port, pki["root"], path="/api/config")[0] == 401
            console_status, console_body, _ = client.fetch(admin_port, pki["root"], path="/", token=token)
            assert console_status == 200 and b"<html" in console_body.lower()
            preview = api("/api/config/adopt-external/preview")
            assert preview["ok"] and preview["origin"] == "no_baseline"
            api("/api/config/adopt-external", json.dumps({"observed_digest": preview["observed_digest"],
                "base_version": preview.get("base_version", ""), "mode": "hot", "confirm": True}).encode(), "application/json")
            before = api("/api/config")
            changed = before["raw"].replace('log_level = "info"', 'log_level = "debug"', 1)
            api("/api/config/apply?base_version=" + urllib.parse.quote(before["base_version"]), changed.encode(), "application/toml")
            assert 'log_level = "debug"' in api("/api/config")["raw"]
            history = api("/api/config/history")
            assert history and history[0]["id"]
            rollback = json.dumps({"id": history[0]["id"]}).encode()
            for attempt in range(5):
                status, _, _ = client.fetch(admin_port, pki["root"], path="/api/config/rollback", token=token,
                                            method="POST", data=rollback, content_type="application/json")
                if status != 409:
                    break
                time.sleep(0.5)
            assert status in (200, 204)
            assert 'log_level = "info"' in api("/api/config")["raw"]
            cache_marker = temporary / "cache-marker"
            cache_marker.write_text("persistent cache volume\n")
            docker("cp", cache_marker, container + ":/var/cache/jul/journey-marker")
            docker("stop", container)
            old_logs = temporary / "previous-access.log"
            docker("cp", container + ":/var/log/jul/access.log", old_logs)
            assert old_logs.stat().st_size > 0
            docker("rm", "--volumes", container)
            docker(*start, token=token)
            wait_healthy()
            assert 'log_level = "info"' in api("/api/config")["raw"]
            assert history[0]["id"] in {entry["id"] for entry in api("/api/config/history")}
            restored = temporary / "restored-cache-marker"
            docker("cp", container + ":/var/cache/jul/journey-marker", restored)
            assert restored.read_bytes() == cache_marker.read_bytes()
            logs = temporary / "restored-access.log"
            docker("cp", container + ":/var/log/jul/access.log", logs)
            assert logs.read_bytes().startswith(old_logs.read_bytes())
            identity = json.loads(docker("exec", container, "/usr/local/bin/jul", "version", "-json"))
            assert identity["version"] == "v2.2.0" and identity["go_version"] == "go" + wanted_go
            write_report(args.report, {"journey": "docker", "evidence_class": "controlled-ci-lab",
                "harness_source": command("git", "rev-parse", "HEAD"), "source": SOURCE,
                "source_archive_sha256": hashlib.sha256(archive.read_bytes()).hexdigest(),
                "image_id": json.loads(docker("image", "inspect", image))[0]["Id"], "binary_metadata": identity,
                "network": client.evidence(), "root_sha256": pki["root_sha256"], "module_sha256": module_digest,
                "checks": {key: "passed" for key in ("frozen_source_full_profile", "nonroot", "routed_tls_static_proxy_wasm",
                    "wrong_ca", "remote_admin_auth_console", "exec_tls_healthcheck", "managed_apply_rollback",
                    "config_history_recreation", "cache_volume_recreation", "access_log_recreation")},
                "limits": ["Locally built pinned-source image, not a published GHCR image or signed image attestation",
                    "Dockerfile records version but not embedded source commit; source archive/image identity is recorded separately",
                    "Separate client/container namespaces share the runner kernel; no Internet/cloud firewall certification",
                    "Cache volume persistence is a marker check, not a new HTTP cache correctness test"]})
            print("PASS: frozen-source nonroot Docker image, routed TLS/admin/healthcheck, managed recovery and container-recreation persistence")
    finally:
        subprocess.run(["docker", "rm", "--force", "--volumes", container], capture_output=True)
        for volume in volumes:
            subprocess.run(["docker", "volume", "rm", volume], capture_output=True)
        subprocess.run(["docker", "network", "rm", network], capture_output=True)
        subprocess.run(["docker", "image", "rm", image], capture_output=True)
        if backend:
            backend.shutdown()
            backend.server_close()
            worker.join()
        if configuration.exists():
            command("sudo", "chown", "-R", f"{os.getuid()}:{os.getgid()}", configuration)
        shutil.rmtree(temporary)


if __name__ == "__main__":
    main()
