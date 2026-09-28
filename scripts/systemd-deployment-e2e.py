#!/usr/bin/env python3
"""Exercise the shipped editable and read-only systemd units on a disposable CI VM.

The Ubuntu runner must boot with systemd and grant passwordless sudo. Refuse
pre-existing Jul service resources, then remove only resources this run made.
"""

import json
from pathlib import Path
import secrets
import shutil
import socket
import subprocess
import tempfile
import time
import urllib.error
import urllib.parse
import urllib.request
import uuid


def run(*args):
    result = subprocess.run(args, text=True, capture_output=True)
    if result.returncode:
        raise AssertionError(f"{args[0]} failed ({result.returncode}): {result.stderr[-2000:]}")
    return result.stdout.strip()


def port():
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        return sock.getsockname()[1]


def request(url, token=None, body=None, content_type=None):
    headers = {}
    if token:
        headers["Authorization"] = "Bearer " + token
    if content_type:
        headers["Content-Type"] = content_type
    try:
        with urllib.request.urlopen(urllib.request.Request(url, data=body, headers=headers), timeout=5) as response:
            return response.status, response.read()
    except urllib.error.HTTPError as error:
        return error.code, error.read()


def expect(status, actual, body):
    assert actual == status, f"HTTP {actual}, expected {status}: {body[:500]!r}"


def wait_ready(unit, traffic, admin):
    deadline = time.monotonic() + 60
    while time.monotonic() < deadline:
        state = subprocess.run(["systemctl", "is-active", unit], capture_output=True, text=True).stdout.strip()
        if state == "failed":
            raise AssertionError(f"{unit} failed: {run('sudo', 'journalctl', '-u', unit, '-n', '30', '--no-pager')[-3000:]}")
        try:
            status, body = request(traffic)
            expect(200, status, body)
            expect(200, *request(admin + "/readyz"))
            if state == "active":
                return body
        except (OSError, AssertionError):
            pass
        time.sleep(1)
    raise AssertionError(f"{unit} not ready: {run('sudo', 'journalctl', '-u', unit, '-n', '30', '--no-pager')[-3000:]}")


def get_config(admin, token):
    status, body = request(admin + "/api/config", token)
    expect(200, status, body)
    return json.loads(body)


def main():
    assert Path("/run/systemd/system").is_dir(), "CI host is not running systemd"
    paths = [Path("/usr/local/bin/jul"), Path("/etc/jul"), Path("/var/lib/jul"),
             Path("/var/cache/jul"), Path("/var/log/jul"),
             Path("/etc/systemd/system/jul.service"), Path("/etc/systemd/system/jul-readonly.service")]
    assert not any(path.exists() for path in paths), "runner has pre-existing Jul service resources"
    assert subprocess.run(["getent", "passwd", "jul"], capture_output=True).returncode != 0, "jul user already exists"

    suffix = uuid.uuid4().hex[:12]
    site = Path("/srv/jul-systemd-e2e-" + suffix)
    tmp = Path(tempfile.mkdtemp(prefix="jul-systemd-e2e-"))
    created = {"user": False, "binary": False, "config": False, "site": False, "units": False}
    try:
        # Build the same console-tagged binary the units launch. The unit's
        # fixed ExecStart path is safe to use after the pre-existing-path guard.
        run("go", "build", "-tags", "console", "-o", str(tmp / "jul"), "./cmd/jul")
        run("sudo", "useradd", "--system", "--no-create-home", "--shell", "/usr/sbin/nologin", "jul")
        created["user"] = True
        created["binary"] = True
        run("sudo", "install", "-m0755", str(tmp / "jul"), "/usr/local/bin/jul")
        created["site"] = True
        run("sudo", "install", "-d", "-m0755", str(site))
        (tmp / "index.html").write_text("Jul systemd E2E\n")
        run("sudo", "install", "-m0644", str(tmp / "index.html"), str(site / "index.html"))
        created["config"] = True
        run("sudo", "install", "-d", "-o", "jul", "-g", "jul", "-m0700", "/etc/jul")
        token = secrets.token_urlsafe(36)
        traffic_port, admin_port = port(), port()
        while admin_port == traffic_port:
            admin_port = port()
        raw = Path("deploy/docker/server.toml").read_text()
        raw = raw.replace('listen = "0.0.0.0:8080"', f'listen = "127.0.0.1:{traffic_port}"', 1)
        raw = raw.replace('listen = "127.0.0.1:9090"', f'listen = "127.0.0.1:{admin_port}"', 1)
        raw = raw.replace('root = "/var/www"', f'root = "{site}"', 1)
        raw = raw.replace('history_dir = "/var/lib/jul/config-history"',
                          f'token = "{token}"\nhistory_dir = "/var/lib/jul/config-history"', 1)
        assert raw.count(f'token = "{token}"') == 1 and f'root = "{site}"' in raw
        (tmp / "server.toml").write_text(raw)
        run("sudo", "install", "-o", "jul", "-g", "jul", "-m0600", str(tmp / "server.toml"), "/etc/jul/server.toml")
        run("sudo", "-u", "jul", "/usr/local/bin/jul", "check", "--config", "/etc/jul/server.toml")
        created["units"] = True
        for name in ("jul.service", "jul-readonly.service"):
            run("sudo", "install", "-m0644", "deploy/systemd/" + name, "/etc/systemd/system/" + name)
        run("sudo", "systemctl", "daemon-reload")
        run("sudo", "systemd-analyze", "verify", "/etc/systemd/system/jul.service",
            "/etc/systemd/system/jul-readonly.service")

        traffic = f"http://127.0.0.1:{traffic_port}/"
        admin = f"http://127.0.0.1:{admin_port}"
        run("sudo", "systemctl", "start", "jul.service")
        assert b"Jul systemd E2E" in wait_ready("jul.service", traffic, admin)
        assert run("sudo", "stat", "-c", "%U:%G %a", "/etc/jul") == "jul:jul 700"
        assert run("sudo", "stat", "-c", "%U:%G %a", "/etc/jul/server.toml") == "jul:jul 600"
        assert run("sudo", "stat", "-c", "%U:%G %a", "/var/lib/jul") == "jul:jul 700"
        expect(401, *request(admin + "/api/config"))
        status, body = request(admin + "/api/config/adopt-external/preview", token)
        expect(200, status, body)
        preview = json.loads(body)
        assert preview["ok"] and preview["origin"] == "no_baseline"
        adoption = json.dumps({"observed_digest": preview["observed_digest"],
                               "base_version": preview.get("base_version", ""),
                               "mode": "hot", "confirm": True}).encode()
        status, body = request(admin + "/api/config/adopt-external", token, adoption, "application/json")
        expect(200, status, body)
        assert json.loads(body)["ok"]
        before = get_config(admin, token)
        assert before["base_version"] and 'log_level = "info"' in before["raw"]
        changed = before["raw"].replace('log_level = "info"', 'log_level = "debug"', 1)
        endpoint = admin + "/api/config/apply?base_version=" + urllib.parse.quote(before["base_version"])
        expect(200, *request(endpoint, token, changed.encode(), "application/toml"))
        assert 'log_level = "debug"' in get_config(admin, token)["raw"]
        status, body = request(admin + "/api/config/history", token)
        expect(200, status, body)
        history = json.loads(body)
        assert history and history[0]["id"]
        rollback = json.dumps({"id": history[0]["id"]}).encode()
        for attempt in range(5):
            status, body = request(admin + "/api/config/rollback", token, rollback, "application/json")
            if status != 409:
                break
            time.sleep(0.5)
        assert status in (200, 204), f"rollback HTTP {status}: {body[:500]!r}"
        assert 'log_level = "info"' in get_config(admin, token)["raw"]
        run("sudo", "systemctl", "restart", "jul.service")
        wait_ready("jul.service", traffic, admin)
        assert 'log_level = "info"' in get_config(admin, token)["raw"]
        print("PASS: editable unit, service identity, adoption, Apply, rollback and restart")

        # A restart-bound admin resource is persisted as one staged candidate.
        # The current service keeps serving until the process actually restarts.
        new_history = "/var/lib/jul/config-history-staged"
        run("sudo", "-u", "jul", "install", "-d", "-m0700", new_history)
        before = get_config(admin, token)
        assert 'history_dir = "/var/lib/jul/config-history"' in before["raw"]
        staged_raw = before["raw"].replace('history_dir = "/var/lib/jul/config-history"',
                                           f'history_dir = "{new_history}"', 1)
        endpoint = admin + "/api/config/apply?mode=stage_restart&base_version=" + urllib.parse.quote(before["base_version"])
        status, body = request(endpoint, token, staged_raw.encode(), "application/toml")
        expect(200, status, body)
        assert json.loads(body)["ok"]
        status, body = request(admin + "/api/config/pending-restart", token)
        expect(200, status, body)
        pending = json.loads(body)
        assert pending["pending"] and pending["status"]["staged"], pending
        assert b"Jul systemd E2E" in request(traffic)[1]
        run("sudo", "systemctl", "restart", "jul.service")
        wait_ready("jul.service", traffic, admin)
        assert f'history_dir = "{new_history}"' in get_config(admin, token)["raw"]
        status, body = request(admin + "/api/config/pending-restart", token)
        expect(200, status, body)
        assert not json.loads(body)["pending"], body
        print("PASS: staged admin history path persisted and activated after restart")

        # An out-of-band edit must block managed writes until explicit adoption.
        # Write as the service identity, then force an immediate drift assessment
        # rather than racing the file watcher.
        edit = ('from pathlib import Path; import sys; p = Path(sys.argv[1]); '
                'raw = p.read_text(); old = \'log_level = "info"\'; '
                'assert raw.count(old) == 1; p.write_text(raw.replace(old, \'log_level = "debug"\', 1))')
        run("sudo", "-u", "jul", "python3", "-c", edit, "/etc/jul/server.toml")
        status, body = request(admin + "/api/config/authority/refresh", token, b"", "application/json")
        expect(200, status, body)
        authority = json.loads(body)
        assert authority["drift"] and authority["config_state"] == "managed_drift", authority
        stale = get_config(admin, token)["raw"].replace('log_level = "debug"', 'log_level = "info"', 1)
        expect(409, *request(admin + "/api/config/apply", token, stale.encode(), "application/toml"))
        status, body = request(admin + "/api/config/adopt-external/preview", token)
        expect(200, status, body)
        preview = json.loads(body)
        assert preview["ok"] and preview["origin"] == "drift", preview
        adoption = json.dumps({"observed_digest": preview["observed_digest"],
                               "base_version": preview.get("base_version", ""),
                               "mode": "hot", "confirm": True}).encode()
        expect(200, *request(admin + "/api/config/adopt-external", token, adoption, "application/json"))
        assert 'log_level = "debug"' in get_config(admin, token)["raw"]
        status, body = request(admin + "/api/config/authority/refresh", token, b"", "application/json")
        expect(200, status, body)
        assert not json.loads(body)["drift"], body
        print("PASS: external edit blocked managed Apply until confirmed adoption")

        run("sudo", "systemctl", "stop", "jul.service")
        readonly = raw.replace('config_authority = "managed"', 'config_authority = "file_owned"', 1)
        assert readonly != raw
        (tmp / "readonly.toml").write_text(readonly)
        run("sudo", "chown", "root:root", "/etc/jul")
        run("sudo", "chmod", "0755", "/etc/jul")
        run("sudo", "install", "-o", "root", "-g", "jul", "-m0640", str(tmp / "readonly.toml"), "/etc/jul/server.toml")
        run("sudo", "-u", "jul", "/usr/local/bin/jul", "check", "--config", "/etc/jul/server.toml")
        run("sudo", "systemctl", "start", "jul-readonly.service")
        assert b"Jul systemd E2E" in wait_ready("jul-readonly.service", traffic, admin)
        assert run("sudo", "stat", "-c", "%U:%G %a", "/etc/jul/server.toml") == "root:jul 640"
        before = get_config(admin, token)
        candidate = before["raw"].replace('log_level = "info"', 'log_level = "debug"', 1)
        expect(409, *request(admin + "/api/config/apply", token, candidate.encode(), "application/toml"))
        assert get_config(admin, token)["raw"] == before["raw"]
        assert run("systemctl", "is-active", "jul-readonly.service") == "active"
        print("PASS: read-only unit, group-readable config and denied Apply without write")
    finally:
        if created["units"]:
            for name in ("jul-readonly.service", "jul.service"):
                subprocess.run(["sudo", "systemctl", "stop", name], capture_output=True)
                subprocess.run(["sudo", "rm", "-f", "/etc/systemd/system/" + name], capture_output=True)
            subprocess.run(["sudo", "systemctl", "daemon-reload"], capture_output=True)
        for key, path in (("config", "/etc/jul"), ("binary", "/usr/local/bin/jul"),
                          ("site", str(site))):
            if created[key]:
                run("sudo", "rm", "-rf", "--", path)
        if created["user"]:
            for path in ("/var/lib/jul", "/var/cache/jul", "/var/log/jul"):
                run("sudo", "rm", "-rf", "--", path)
            run("sudo", "userdel", "jul")
        shutil.rmtree(tmp)


if __name__ == "__main__":
    main()
