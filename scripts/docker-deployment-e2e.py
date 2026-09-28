#!/usr/bin/env python3
"""Exercise the shipped Docker profile and a managed Console recovery journey.

Runs only on an isolated Linux CI runner with Docker and passwordless sudo.
The admin API in the second container binds to host loopback, so the test can
authenticate without weakening the production rule for remote admin TLS.
"""

import json
import os
from pathlib import Path
import secrets
import shutil
import socket
import subprocess
import tempfile
import time
import tomllib
import urllib.error
import urllib.parse
import urllib.request
import uuid


def docker(*args, extra_env=None):
    env = {**os.environ, **(extra_env or {})}
    result = subprocess.run(["docker", *args], env=env, text=True, capture_output=True)
    if result.returncode:
        # Do not echo argv: `docker run` may carry environment/credential flags.
        raise AssertionError(f"Docker failed ({result.returncode}): {result.stderr[-3000:]}")
    return result.stdout.strip()


def available_port():
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        return sock.getsockname()[1]


def request(url, token=None, body=None, content_type=None):
    headers = {}
    if token:
        headers["Authorization"] = "Bearer " + token
    if content_type:
        headers["Content-Type"] = content_type
    req = urllib.request.Request(url, data=body, headers=headers)
    try:
        with urllib.request.urlopen(req, timeout=5) as response:
            return response.status, response.read()
    except urllib.error.HTTPError as error:
        return error.code, error.read()


def expect(status, got, body):
    assert got == status, f"HTTP {got}, expected {status}: {body[:500]!r}"


def wait_ready(container, traffic_url, admin_url=None):
    deadline = time.monotonic() + 90
    while time.monotonic() < deadline:
        state = json.loads(docker("inspect", container))[0]["State"]
        if not state["Running"]:
            raise AssertionError(f"container exited: {docker('logs', container)[-3000:]}")
        if state["Health"]["Status"] == "healthy":
            try:
                status, body = request(traffic_url)
                expect(200, status, body)
                if admin_url:
                    expect(200, *request(admin_url + "/readyz"))
                return body
            except (OSError, AssertionError):
                pass
        time.sleep(1)
    raise AssertionError(f"container not ready: {docker('logs', container)[-3000:]}")


def config_get(admin_url, token):
    status, body = request(admin_url + "/api/config", token)
    expect(200, status, body)
    return json.loads(body)


def main():
    suffix = uuid.uuid4().hex[:12]
    image = f"jul-docker-e2e:{suffix}"
    base = f"jul-docker-e2e-{suffix}"
    containers = [base + "-default", base + "-managed"]
    volumes = [base + "-config", base + "-state", base + "-cache", base + "-log", base + "-managed-state"]
    tmp = Path(tempfile.mkdtemp(prefix="jul-docker-e2e-"))
    config_dir = tmp / "config"
    try:
        docker("build", "--build-arg", "VERSION=docs-e2e", "-t", image, ".")
        for volume in volumes:
            docker("volume", "create", volume)

        docker("run", "-d", "--name", containers[0], "-p", "127.0.0.1::8080",
               "-v", volumes[0] + ":/etc/jul", "-v", volumes[1] + ":/var/lib/jul",
               "-v", volumes[2] + ":/var/cache/jul", "-v", volumes[3] + ":/var/log/jul", image)
        published = docker("port", containers[0], "8080/tcp").rsplit(":", 1)[1]
        body = wait_ready(containers[0], f"http://127.0.0.1:{published}/")
        assert b"Jul" in body, "baked placeholder site not served"
        docker("exec", containers[0], "/usr/local/bin/jul", "check", "--config", "/etc/jul/server.toml")
        print("PASS: default image, named-volume config, healthcheck and placeholder site")

        # Host networking permits a loopback-only admin listener. Use a separate
        # ephemeral managed config whose directory is writable by uid 65532.
        traffic_port, admin_port = available_port(), available_port()
        while traffic_port == admin_port:
            admin_port = available_port()
        token = secrets.token_urlsafe(36)
        original = Path("deploy/docker/server.toml").read_text()
        candidate = original.replace('listen = "0.0.0.0:8080"', f'listen = "127.0.0.1:{traffic_port}"', 1)
        candidate = candidate.replace('listen = "127.0.0.1:9090"', f'listen = "127.0.0.1:{admin_port}"', 1)
        candidate = candidate.replace('history_dir = "/var/lib/jul/config-history"',
                                      'token = "${env:JUL_ADMIN_TOKEN}"\n'
                                      'history_dir = "/var/lib/jul/config-history"', 1)
        assert candidate != original
        assert tomllib.loads(candidate)["admin"]["token"] == "${env:JUL_ADMIN_TOKEN}"
        config_dir.mkdir()
        (config_dir / "server.toml").write_text(candidate)
        tmp.chmod(0o755)
        config_dir.chmod(0o700)
        (config_dir / "server.toml").chmod(0o600)
        subprocess.run(["sudo", "chown", "-R", "65532:65532", str(config_dir)], check=True)
        args = ("run", "-d", "--name", containers[1], "--network", "host",
                "-e", "JUL_ADMIN_TOKEN",
                "-v", str(config_dir) + ":/etc/jul",
                "-v", volumes[4] + ":/var/lib/jul", image)
        docker(*args, extra_env={"JUL_ADMIN_TOKEN": token})
        traffic_url = f"http://127.0.0.1:{traffic_port}/"
        admin_url = f"http://127.0.0.1:{admin_port}"
        wait_ready(containers[1], traffic_url, admin_url)
        expect(401, *request(admin_url + "/api/config"))
        # A fresh managed boot has no baseline. The operator must explicitly
        # preview and adopt the seeded file before ordinary writes are allowed.
        status, body = request(admin_url + "/api/config/adopt-external/preview", token)
        expect(200, status, body)
        preview = json.loads(body)
        assert preview["ok"] and preview["origin"] == "no_baseline" and preview["observed_digest"]
        adoption = json.dumps({"observed_digest": preview["observed_digest"],
                               "base_version": preview.get("base_version", ""),
                               "mode": "hot", "confirm": True}).encode()
        status, body = request(admin_url + "/api/config/adopt-external", token,
                               adoption, "application/json")
        expect(200, status, body)
        assert json.loads(body)["ok"], "initial managed adoption failed"
        before = config_get(admin_url, token)
        assert before["base_version"] and 'log_level = "info"' in before["raw"]
        changed = before["raw"].replace('log_level = "info"', 'log_level = "debug"', 1)
        endpoint = admin_url + "/api/config/apply?base_version=" + urllib.parse.quote(before["base_version"])
        expect(200, *request(endpoint, token, changed.encode(), "application/toml"))
        after = config_get(admin_url, token)
        assert 'log_level = "debug"' in after["raw"]
        status, body = request(admin_url + "/api/config/history", token)
        expect(200, status, body)
        history = json.loads(body)
        assert history and history[0]["id"], "apply did not persist a rollback snapshot"
        rollback = json.dumps({"id": history[0]["id"]}).encode()
        for attempt in range(5):
            status, body = request(admin_url + "/api/config/rollback", token, rollback, "application/json")
            if status != 409:
                break
            time.sleep(0.5)
        assert status in (200, 204), f"rollback HTTP {status}: {body[:500]!r}"
        assert 'log_level = "info"' in config_get(admin_url, token)["raw"]
        docker("stop", containers[1])
        docker("rm", "-v", containers[1])
        docker(*args, extra_env={"JUL_ADMIN_TOKEN": token})
        wait_ready(containers[1], traffic_url, admin_url)
        assert 'log_level = "info"' in config_get(admin_url, token)["raw"]
        status, body = request(admin_url + "/api/config/history", token)
        expect(200, status, body)
        assert json.loads(body), "history lost across restart"
        print("PASS: loopback token, managed apply, history, rollback and restart persistence")
    finally:
        for container in containers:
            subprocess.run(["docker", "rm", "-fv", container], capture_output=True)
        for volume in volumes:
            subprocess.run(["docker", "volume", "rm", volume], capture_output=True)
        subprocess.run(["docker", "image", "rm", image], capture_output=True)
        if config_dir.exists():
            subprocess.run(["sudo", "chown", "-R", f"{os.getuid()}:{os.getgid()}", str(config_dir)], check=True)
        shutil.rmtree(tmp)


if __name__ == "__main__":
    main()
