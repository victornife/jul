#!/usr/bin/env python3
"""Exercise the shipped editable and read-only systemd units on a disposable CI VM.

The Ubuntu runner must boot with systemd and grant passwordless sudo. Refuse
pre-existing Jul service resources, then remove only resources this run made.
"""

import argparse
import hashlib
import importlib.util
import json
from pathlib import Path
import platform
import secrets
import shutil
import socket
import ssl
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


def request(url, token=None, body=None, content_type=None, ssl_context=None, include_headers=False):
    headers = {}
    if token:
        headers["Authorization"] = "Bearer " + token
    if content_type:
        headers["Content-Type"] = content_type
    try:
        # The runner may set an HTTP proxy for external traffic; its own
        # non-loopback address must be contacted directly for the TLS check.
        opener = urllib.request.build_opener(urllib.request.ProxyHandler({}),
                                             urllib.request.HTTPSHandler(context=ssl_context))
        with opener.open(urllib.request.Request(url, data=body, headers=headers), timeout=5) as response:
            if include_headers:
                return response.status, response.read(), response.headers
            return response.status, response.read()
    except urllib.error.HTTPError as error:
        if include_headers:
            return error.code, error.read(), error.headers
        return error.code, error.read()


def expect(status, actual, body):
    assert actual == status, f"HTTP {actual}, expected {status}: {body[:500]!r}"


def wait_ready(unit, traffic, admin, ssl_context=None):
    deadline = time.monotonic() + 60
    while time.monotonic() < deadline:
        state = subprocess.run(["systemctl", "is-active", unit], capture_output=True, text=True).stdout.strip()
        if state == "failed":
            raise AssertionError(f"{unit} failed: {run('sudo', 'journalctl', '-u', unit, '-n', '30', '--no-pager')[-3000:]}")
        try:
            status, body = request(traffic)
            expect(200, status, body)
            expect(200, *request(admin + "/readyz", ssl_context=ssl_context))
            if state == "active":
                return body
        except (OSError, AssertionError):
            pass
        time.sleep(1)
    raise AssertionError(f"{unit} not ready: {run('sudo', 'journalctl', '-u', unit, '-n', '30', '--no-pager')[-3000:]}")


def get_config(admin, token, ssl_context=None):
    status, body = request(admin + "/api/config", token, ssl_context=ssl_context)
    expect(200, status, body)
    return json.loads(body)


def verify_wasm(unit, traffic, admin, token, module_digest):
    assert run("systemctl", "show", unit, "--property=MemoryDenyWriteExecute", "--value") == "yes"
    status, body = request(admin + "/api/plugins", token)
    expect(200, status, body)
    plugins = json.loads(body)
    assert plugins["compiled"] and plugins["engine"] == "interpreter", "service is not using the interpreter"
    serving = next(item for item in plugins["plugins"] if item["name"] == "systemd-probe")
    assert serving["digest"] == "sha256:" + module_digest, "service loaded a different WASM module"
    status, body, headers = request(traffic, include_headers=True)
    expect(200, status, body)
    assert headers.get("X-Plugin") == "header-inject", "WASM middleware did not execute"
    print(f"PASS: {unit} keeps MemoryDenyWriteExecute=yes and executes the pinned WASM guest with interpreter")


def prepare_binary(arguments, temporary):
    if arguments.package_directory:
        # Use the same checksum/source/profile/attestation verifier as the native
        # release journey. The package stays bound to its original source even
        # when this harness is run from a later commit.
        spec = importlib.util.spec_from_file_location("release_verifier", Path(__file__).with_name("verify-release-packages.py"))
        verifier = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(verifier)
        architecture = verifier.native_architecture(platform.machine(), {}, False)
        binary, evidence = verifier.verify_archive(arguments.package_directory.resolve(), arguments.tag,
                                                   arguments.source, "linux", architecture, "full")
        evidence["kind"] = "published-package"
        return binary, evidence
    binary = temporary / "jul"
    run("go", "build", "-tags", "console,wasmplugins", "-o", str(binary), "./cmd/jul")
    return binary, {"kind": "source-build", "source": run("git", "rev-parse", "HEAD")}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--package-directory", type=Path,
                        help="Directory containing the published full Linux archive, its .sha256 and SHA256SUMS; requires gh")
    parser.add_argument("--tag", default="v2.2.0")
    parser.add_argument("--source", default="b12e051139f652b400cff37d9f063f5637a7d550")
    parser.add_argument("--wasm-module", type=Path, default=Path("testdata/plugins/v1-current-header-inject.wasm"))
    parser.add_argument("--report", type=Path)
    arguments = parser.parse_args()
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
    report = {"journey": "systemd", "evidence_class": "disposable",
              "harness_source": run("git", "rev-parse", "HEAD"),
              "host": {"os": platform.system(), "arch": platform.machine(),
                       "systemd": run("systemctl", "--version").splitlines()[0]},
              "checks": {}, "production_like": {"trusted_chain": "pending", "independent_client": "pending", "firewall": "pending"}}
    try:
        binary, report["binary"] = prepare_binary(arguments, tmp)
        report["binary"]["binary_sha256"] = hashlib.sha256(binary.read_bytes()).hexdigest()
        module = arguments.wasm_module.resolve()
        module_digest = hashlib.sha256(module.read_bytes()).hexdigest()
        report["wasm_module_sha256"] = module_digest
        run("sudo", "useradd", "--system", "--no-create-home", "--shell", "/usr/sbin/nologin", "jul")
        created["user"] = True
        created["binary"] = True
        run("sudo", "install", "-m0755", str(binary), "/usr/local/bin/jul")
        created["site"] = True
        run("sudo", "install", "-d", "-m0755", str(site))
        (tmp / "index.html").write_text("Jul systemd E2E\n")
        run("sudo", "install", "-m0644", str(tmp / "index.html"), str(site / "index.html"))
        created["config"] = True
        run("sudo", "install", "-d", "-o", "jul", "-g", "jul", "-m0700", "/etc/jul")
        run("sudo", "install", "-o", "jul", "-g", "jul", "-m0600", str(module), "/etc/jul/systemd-probe.wasm")
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
        raw = raw.replace('server_names = ["localhost"]', 'server_names = ["localhost"]\nplugins = ["systemd-probe"]', 1)
        raw += ('\n[plugins.systemd-probe]\npath = "/etc/jul/systemd-probe.wasm"\ntype = "middleware"\n'
                f'sha256 = "sha256:{module_digest}"\ntimeout = "5s"\n')
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
        verify_wasm("jul.service", traffic, admin, token, module_digest)
        report["checks"]["editable_wasm_interpreter"] = "passed"
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
        report["checks"]["editable_recovery"] = "passed"

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

        # Exercise the non-loopback admin transport gate with a real TLS
        # listener, trusted self-signed certificate and token on this VM's
        # non-loopback address. It remains isolated to this disposable runner.
        host_ips = {item[4][0] for item in socket.getaddrinfo(socket.gethostname(), None,
                    family=socket.AF_INET) if not item[4][0].startswith("127.")}
        assert host_ips, "runner has no non-loopback IPv4 address for admin TLS"
        host_ip = sorted(host_ips)[0]
        cert, key = tmp / "admin-cert.pem", tmp / "admin-key.pem"
        run("openssl", "req", "-x509", "-newkey", "rsa:2048", "-nodes", "-sha256", "-days", "1",
            "-subj", "/CN=Jul service E2E", "-addext", f"subjectAltName=IP:{host_ip}",
            "-keyout", str(key), "-out", str(cert))
        run("sudo", "install", "-o", "jul", "-g", "jul", "-m0600", str(cert), "/etc/jul/admin-cert.pem")
        run("sudo", "install", "-o", "jul", "-g", "jul", "-m0600", str(key), "/etc/jul/admin-key.pem")
        before = get_config(admin, token)
        old_listen = f'listen = "127.0.0.1:{admin_port}"'
        assert before["raw"].count(old_listen) == 1
        tls_raw = before["raw"].replace(old_listen, f'listen = "0.0.0.0:{admin_port}"', 1)
        tls_raw += '\n[admin.tls]\nenabled = true\ncert = "/etc/jul/admin-cert.pem"\nkey = "/etc/jul/admin-key.pem"\n'
        endpoint = (admin + "/api/config/apply?mode=stage_restart&confirm_admin=true&base_version="
                    + urllib.parse.quote(before["base_version"]))
        status, body = request(endpoint, token, tls_raw.encode(), "application/toml")
        expect(200, status, body)
        assert json.loads(body)["ok"], body
        run("sudo", "systemctl", "restart", "jul.service")
        tls_admin = f"https://{host_ip}:{admin_port}"
        trusted = ssl.create_default_context(cafile=str(cert))
        wait_ready("jul.service", traffic, tls_admin, trusted)
        run("sudo", "-u", "jul", "/usr/local/bin/jul", "healthcheck", "-url",
            tls_admin + "/readyz", "-ca-file", "/etc/jul/admin-cert.pem", "-quiet")
        expect(401, *request(tls_admin + "/api/config", ssl_context=trusted))
        secured = get_config(tls_admin, token, trusted)
        assert '[admin.tls]' in secured["raw"] and 'listen = "0.0.0.0:' in secured["raw"]
        print("PASS: non-loopback admin TLS certificate and token on the shipped systemd unit")
        report["checks"]["single_host_temporary_tls"] = "passed"

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
        verify_wasm("jul-readonly.service", traffic, admin, token, module_digest)
        report["checks"]["readonly_wasm_interpreter"] = "passed"
        assert run("sudo", "stat", "-c", "%U:%G %a", "/etc/jul/server.toml") == "root:jul 640"
        before = get_config(admin, token)
        candidate = before["raw"].replace('log_level = "info"', 'log_level = "debug"', 1)
        expect(409, *request(admin + "/api/config/apply", token, candidate.encode(), "application/toml"))
        assert get_config(admin, token)["raw"] == before["raw"]
        assert run("systemctl", "is-active", "jul-readonly.service") == "active"
        print("PASS: read-only unit, group-readable config and denied Apply without write")
        report["checks"]["readonly_apply_denied"] = "passed"
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
    if arguments.report:
        arguments.report.parent.mkdir(parents=True, exist_ok=True)
        arguments.report.write_text(json.dumps(report, indent=2) + "\n")


if __name__ == "__main__":
    main()
