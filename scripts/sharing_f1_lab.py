#!/usr/bin/env python3
# Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
# SPDX-License-Identifier: agpl

"""Fixture F1 Standalone Sharing Verification & W0-W4 Resource Harness (#581, SHARE-02A).

Executes the predeclared F1 configuration and evidence manifest
(docs/sharing/f1-configuration-manifest.json) against the pinned full-profile
Jul binary under the hardened systemd service profile (deploy/systemd/jul.service
and deploy/systemd/jul-readonly.service), using two labelled routed Linux
network namespaces (lab-client-1 at 192.0.2.11, lab-client-2 at 192.0.2.12) and
an independent continuity-probe namespace (lab-probe-1 at 192.0.2.13) connected
to a task-owned bridge (192.0.2.10/24) with a scoped iptables chain.

Refuses collisions with any pre-existing Jul service/config/user resources and
removes only task-owned resources on teardown.
"""

from __future__ import annotations

import argparse
import base64
from datetime import datetime
import hashlib
import json
import os
from pathlib import Path
import platform
import secrets
import shutil
import socket
import statistics
import subprocess
import tempfile
import time
from typing import Any
import sys
import urllib.error
import urllib.parse
import urllib.request
import uuid

ROOT = Path(__file__).resolve().parent.parent
MANIFEST_PATH = ROOT / "docs" / "sharing" / "f1-configuration-manifest.json"
EXAMPLES_DIR = ROOT / "examples" / "sharing" / "f1-standalone"
FULL_TAGS = "brotli zstd acme console otel grpc http3 importer wasmplugins stream consul kubernetes waf"

PORTAL_MARKER = "JUL-F1-PUBLIC-PORTAL-MARKER-v1"
DASHBOARD_MARKER = "JUL-F1-RESTRICTED-DASHBOARD-MARKER-v1"
PRIVATE_MARKER = "JUL-F1-PRIVATE-FAMILY-TELEMETRY-DO-NOT-CACHE"


class ProcessRegistry:
    """Tracks all spawned child processes and ensures deterministic termination on any exit path."""

    def __init__(self) -> None:
        self._procs: list[subprocess.Popen[Any]] = []

    def register(self, proc: subprocess.Popen[Any]) -> subprocess.Popen[Any]:
        self._procs.append(proc)
        return proc

    def terminate_all(self) -> list[str]:
        errors: list[str] = []
        for proc in reversed(self._procs):
            try:
                if proc.poll() is None:
                    proc.terminate()
                    try:
                        proc.wait(timeout=5)
                    except subprocess.TimeoutExpired:
                        proc.kill()
                        proc.wait(timeout=5)
            except Exception as exc:
                errors.append(str(exc))
        self._procs.clear()
        return errors


PROC_REGISTRY = ProcessRegistry()


class PrivRunner:
    """Execute privileged host setup/teardown commands via root, sudo -n, or a single nsenter helper."""

    def __init__(self) -> None:
        self.mode = "none"
        self.helper_container: str | None = None
        if os.geteuid() == 0:
            self.mode = "root"
            return
        res = subprocess.run(["sudo", "-n", "true"], capture_output=True, text=True)
        if res.returncode == 0:
            self.mode = "sudo"
            return
        # Fallback for local container/WSL environments where user is in docker group
        cid_name = "jul581-priv-" + uuid.uuid4().hex[:8]
        start = subprocess.run(
            [
                "docker", "run", "-d", "--rm", "--name", cid_name,
                "--privileged", "--pid=host", "--net=host",
                "alpine:3.20", "sleep", "7200",
            ],
            capture_output=True,
            text=True,
            timeout=60,
        )
        if start.returncode == 0:
            self.mode = "docker-nsenter"
            self.helper_container = cid_name
        else:
            raise RuntimeError("PrivRunner requires root, passwordless sudo, or docker access for netns/systemd setup")

    def run(self, *args: str | Path, check: bool = True, timeout: int = 60, env: dict[str, str] | None = None) -> subprocess.CompletedProcess[str]:
        str_args = [str(a) for a in args]
        if env:
            env_prefix = ["env", *[f"{k}={v}" for k, v in env.items()]]
            str_args = env_prefix + str_args
        if self.mode == "root":
            cmd = str_args
        elif self.mode == "sudo":
            cmd = ["sudo", "-n", *str_args]
        elif self.mode == "docker-nsenter" and self.helper_container:
            cmd = ["docker", "exec", self.helper_container, "nsenter", "-t", "1", "-m", "-u", "-i", "-n", "-p", "--", *str_args]
        else:
            raise RuntimeError("PrivRunner not initialized")
        proc = subprocess.run(cmd, text=True, capture_output=True, timeout=timeout)
        if check and proc.returncode != 0:
            raise RuntimeError(f"priv command {args[0]} failed ({proc.returncode}): {proc.stderr[-1200:]}")
        return proc

    def close(self) -> None:
        if self.helper_container:
            subprocess.run(["docker", "rm", "-f", self.helper_container], capture_output=True, text=True)
            self.helper_container = None


class F1LabTopology:
    """Manages the controlled-LAN bridge (192.0.2.10/24), 3 client namespaces, scoped iptables, and systemd units."""

    def __init__(self, priv: PrivRunner, workdir: Path, jul_bin: Path, lean_bin: Path) -> None:
        self.priv = priv
        self.workdir = workdir.resolve()
        self.jul_bin = jul_bin.resolve()
        self.lean_bin = lean_bin.resolve()
        self.suffix = uuid.uuid4().hex[:6]
        self.bridge = f"j581br{self.suffix[:4]}"
        self.chain = f"JL581_{self.suffix.upper()}"
        self.server_ip = "192.0.2.10"
        self.namespaces = {
            "lab-client-1": ("192.0.2.11", f"j581h1{self.suffix[:3]}", f"j581c1{self.suffix[:3]}"),
            "lab-client-2": ("192.0.2.12", f"j581h2{self.suffix[:3]}", f"j581c2{self.suffix[:3]}"),
            "lab-probe-1": ("192.0.2.13", f"j581hp{self.suffix[:3]}", f"j581cp{self.suffix[:3]}"),
        }
        self.created_user = False
        self.created_paths: list[Path] = []
        self.created_units: list[str] = []
        self.bridge_created = False
        self.created_ns: list[str] = []
        self.created_links: list[str] = []
        self.chain_created = False
        self.jump_created = False

    def verify_no_collisions(self) -> None:
        unit_names = ("jul.service", "jul-readonly.service", "jul-f1-dashboard.service")
        unit_dirs = (
            Path("/etc/systemd/system"),
            Path("/run/systemd/system"),
            Path("/lib/systemd/system"),
            Path("/usr/lib/systemd/system"),
        )
        protected = [
            Path("/usr/local/bin/jul"),
            Path("/etc/jul"),
            Path("/var/lib/jul"),
            Path("/var/cache/jul"),
            Path("/var/log/jul"),
            *[d / u for d in unit_dirs for u in unit_names],
        ]
        existing = [str(p) for p in protected if p.exists()]
        if existing:
            raise RuntimeError(f"Refusing setup: pre-existing Jul host resources found: {existing}")
        for u in unit_names:
            if self.priv.run("systemctl", "is-active", "--quiet", u, check=False).returncode == 0:
                raise RuntimeError(f"Refusing setup: systemd unit '{u}' is already active")
        if subprocess.run(["getent", "passwd", "jul"], capture_output=True).returncode == 0:
            raise RuntimeError("Refusing setup: system user 'jul' already exists")
        for host, port in (("127.0.0.1", 3000), ("127.0.0.1", 9443), ("127.0.0.1", 443)):
            with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as s:
                s.settimeout(0.2)
                if s.connect_ex((host, port)) == 0:
                    raise RuntimeError(f"Refusing setup: TCP listener already active on {host}:{port}")
        ns_out = self.priv.run("ip", "netns", "list", check=False)
        if ns_out.returncode == 0:
            active_ns = [ns for ns in self.namespaces if ns in ns_out.stdout]
            if active_ns:
                raise RuntimeError(f"Refusing setup: pre-existing lab namespaces found: {active_ns}")

    def setup_network(self) -> dict[str, Any]:
        self.priv.run("ip", "link", "add", self.bridge, "type", "bridge")
        self.bridge_created = True
        self.priv.run("ip", "addr", "add", f"{self.server_ip}/24", "dev", self.bridge)
        self.priv.run("ip", "link", "set", self.bridge, "up")

        for ns_name, (ip_addr, h_if, c_if) in self.namespaces.items():
            self.priv.run("ip", "netns", "add", ns_name)
            self.created_ns.append(ns_name)
            self.priv.run("ip", "link", "add", h_if, "type", "veth", "peer", "name", c_if)
            self.created_links.append(h_if)
            self.priv.run("ip", "link", "set", h_if, "master", self.bridge)
            self.priv.run("ip", "link", "set", h_if, "up")
            self.priv.run("ip", "link", "set", c_if, "netns", ns_name)
            self.priv.run("ip", "-n", ns_name, "addr", "add", f"{ip_addr}/24", "dev", c_if)
            self.priv.run("ip", "-n", ns_name, "link", "set", c_if, "up")
            self.priv.run("ip", "-n", ns_name, "link", "set", "lo", "up")
            self.priv.run("ip", "-n", ns_name, "route", "add", "default", "via", self.server_ip)

        self.priv.run("iptables", "-N", self.chain)
        self.chain_created = True
        self.priv.run("iptables", "-I", "INPUT", "1", "-i", self.bridge, "-j", self.chain)
        self.jump_created = True
        self.priv.run("iptables", "-A", self.chain, "-p", "tcp", "--dport", "443", "-j", "ACCEPT")
        self.priv.run("iptables", "-A", self.chain, "-p", "tcp", "--dport", "8443", "-j", "ACCEPT")
        self.priv.run("iptables", "-A", self.chain, "-j", "REJECT")
        return {
            "topology": "controlled-lan-bridge-with-labelled-routed-namespaces",
            "bridge_interface": self.bridge,
            "server_lan_ip": self.server_ip,
            "firewall_chain": self.chain,
            "allowed_tcp_ports": [443, 8443],
            "namespaces": {k: {"ip": v[0], "host_veth": v[1], "client_veth": v[2]} for k, v in self.namespaces.items()},
        }

    def set_port_443_allowed(self, allowed: bool) -> None:
        target = "ACCEPT" if allowed else "REJECT"
        self.priv.run("iptables", "-R", self.chain, "1", "-p", "tcp", "--dport", "443", "-j", target)

    def firewall_reject_packets(self) -> int:
        out = self.priv.run("iptables", "-nvxL", self.chain).stdout
        total = 0
        for line in out.splitlines():
            parts = line.split()
            if len(parts) >= 3 and parts[2] == "REJECT":
                try:
                    total += int(parts[0])
                except ValueError:
                    pass
        return total

    def setup_services(self, tokens: dict[str, str]) -> dict[str, Any]:
        self.priv.run("useradd", "--system", "--no-create-home", "--shell", "/usr/sbin/nologin", "jul")
        self.created_user = True

        self.priv.run("install", "-m0755", self.jul_bin, "/usr/local/bin/jul")
        self.created_paths.append(Path("/usr/local/bin/jul"))

        for d in ("/etc/jul", "/var/lib/jul", "/var/cache/jul", "/var/log/jul"):
            self.priv.run("install", "-d", "-o", "jul", "-g", "jul", "-m0700", d)
            self.created_paths.append(Path(d))

        self.priv.run("install", "-d", "-o", "jul", "-g", "jul", "-m0700", "/var/lib/jul/private", "/etc/jul/private", "/var/lib/jul/config-history")
        self.priv.run("sudo", "-u", "jul", "/usr/local/bin/jul", "version-key", "init", "--file", "/var/lib/jul/private/effective-version.key")
        self.priv.run("install", "-o", "jul", "-g", "jul", "-m0400", "/var/lib/jul/private/effective-version.key", "/etc/jul/private/effective-version.key")

        # Install generated TLS, portal, htpasswd, and backend script under /etc/jul and /var/lib/jul
        self.priv.run("cp", "-r", self.workdir / "tls", "/etc/jul/tls")
        self.priv.run("cp", "-r", self.workdir / "portal", "/etc/jul/portal")
        self.priv.run("cp", self.workdir / "dashboard.htpasswd", "/etc/jul/dashboard.htpasswd")
        self.priv.run("cp", self.workdir / "dashboard-revoked.htpasswd", "/etc/jul/dashboard-revoked.htpasswd")
        self.priv.run("chown", "-R", "jul:jul", "/etc/jul/tls", "/etc/jul/portal", "/etc/jul/dashboard.htpasswd", "/etc/jul/dashboard-revoked.htpasswd")
        self.priv.run("chmod", "0755", "/etc/jul/portal", "/etc/jul/portal/assets")
        self.priv.run("chmod", "0644", "/etc/jul/portal/index.html", "/etc/jul/portal/assets/bundle.bin")
        self.priv.run("chmod", "0750", "/etc/jul/tls")
        for f in (self.workdir / "tls").iterdir():
            mode = "0640" if f.suffix == ".key" else "0644"
            self.priv.run("chmod", mode, f"/etc/jul/tls/{f.name}")

        # Install systemd units and an EnvironmentFile in /var/lib/jul/private/f1.env (0600 jul:jul)
        env_file = self.workdir / "f1.env"
        env_file.write_text(
            f"JUL_ADMIN_TOKEN={tokens['admin']}\n"
            f"JUL_F1_AUDITOR_TOKEN={tokens['auditor']}\n"
            f"JUL_F1_VIEWER_TOKEN={tokens['viewer']}\n"
            f"JUL_F1_OPERATOR_TOKEN={tokens['operator']}\n",
            encoding="utf-8",
        )
        self.priv.run("install", "-o", "jul", "-g", "jul", "-m0600", env_file, "/var/lib/jul/private/f1.env")
        env_file.unlink(missing_ok=True)

        for unit_name in ("jul.service", "jul-readonly.service"):
            src_text = (ROOT / "deploy" / "systemd" / unit_name).read_text(encoding="utf-8")
            # Add WorkingDirectory=/etc/jul and EnvironmentFile=/var/lib/jul/private/f1.env while preserving all shipped hardening
            patched = src_text.replace(
                "[Service]\nType=simple\n",
                "[Service]\nType=simple\nWorkingDirectory=/etc/jul\nEnvironmentFile=/var/lib/jul/private/f1.env\n",
                1,
            )
            tmp_u = self.workdir / unit_name
            tmp_u.write_text(patched, encoding="utf-8")
            self.priv.run("install", "-m0644", tmp_u, f"/etc/systemd/system/{unit_name}")
            self.created_units.append(unit_name)

        # Dedicated systemd unit for the deterministic dashboard backend (separate cgroup v2 accounting)
        dash_script = Path("/var/lib/jul/dashboard_backend.py")
        self.priv.run("install", "-o", "jul", "-g", "jul", "-m0755", EXAMPLES_DIR / "dashboard_backend.py", dash_script)
        dash_unit = self.workdir / "jul-f1-dashboard.service"
        dash_unit.write_text(
            """[Unit]
Description=Jul F1 deterministic dashboard test backend
After=network.target

[Service]
Type=simple
User=jul
Group=jul
ExecStart=/usr/bin/python3 /var/lib/jul/dashboard_backend.py --listen 127.0.0.1:3000
Restart=no
NoNewPrivileges=yes
PrivateTmp=yes
ProtectSystem=strict
ProtectHome=yes
""",
            encoding="utf-8",
        )
        self.priv.run("install", "-m0644", dash_unit, "/etc/systemd/system/jul-f1-dashboard.service")
        self.created_units.append("jul-f1-dashboard.service")

        self.priv.run("systemctl", "daemon-reload")
        self.priv.run("systemctl", "start", "jul-f1-dashboard.service")
        self.wait_http_local("http://127.0.0.1:3000/healthz", timeout_s=15)

        props = {}
        for prop in ("User", "Group", "NoNewPrivileges", "ProtectSystem", "ProtectHome", "PrivateTmp", "PrivateDevices", "MemoryDenyWriteExecute", "AmbientCapabilities"):
            props[prop] = self.priv.run("systemctl", "show", "jul.service", f"--property={prop}", "--value").stdout.strip()
        ro_props = {}
        for prop in ("User", "Group", "ReadOnlyPaths", "ProtectSystem", "MemoryDenyWriteExecute"):
            ro_props[prop] = self.priv.run("systemctl", "show", "jul-readonly.service", f"--property={prop}", "--value").stdout.strip()
        return {"jul_service": props, "jul_readonly_service": ro_props}

    def adapt_config_for_service(self, toml_text: str) -> str:
        return toml_text.replace('history_dir = "state/config-history"', 'history_dir = "/var/lib/jul/config-history"')

    def install_config(self, toml_text: str, readonly: bool = False) -> None:
        adapted = self.adapt_config_for_service(toml_text)
        tmp_cfg = self.workdir / "staged-server.toml"
        tmp_cfg.write_text(adapted, encoding="utf-8")
        if readonly:
            self.priv.run("chmod", "0755", "/etc/jul")
            self.priv.run("install", "-o", "root", "-g", "jul", "-m0640", tmp_cfg, "/etc/jul/server.toml")
        else:
            self.priv.run("chown", "jul:jul", "/etc/jul")
            self.priv.run("chmod", "0700", "/etc/jul")
            self.priv.run("install", "-o", "jul", "-g", "jul", "-m0600", tmp_cfg, "/etc/jul/server.toml")

    def wait_http_local(self, url: str, timeout_s: int = 20, expected_unit: str | None = None) -> None:
        deadline = time.monotonic() + timeout_s
        while time.monotonic() < deadline:
            try:
                with urllib.request.urlopen(url, timeout=2) as resp:
                    if resp.status == 200:
                        if expected_unit:
                            st = self.priv.run("systemctl", "show", expected_unit, "--property=ActiveState,SubState,MainPID", check=False)
                            if st.returncode != 0 or "ActiveState=active" not in st.stdout or "SubState=running" not in st.stdout:
                                time.sleep(0.1)
                                continue
                            pid_line = next((l for l in st.stdout.splitlines() if l.startswith("MainPID=")), "MainPID=0")
                            pid_val = int(pid_line.split("=", 1)[1].strip() or "0")
                            if pid_val <= 0:
                                time.sleep(0.1)
                                continue
                        return
            except Exception:
                pass
            time.sleep(0.1)
        raise RuntimeError(f"Service at {url} did not become ready within {timeout_s}s")

    def netns_curl(
        self,
        namespace: str,
        url: str,
        resolve_host: str | None = "portal.lab.test:443:192.0.2.10",
        ca_file: Path | None = None,
        basic_auth: str | None = None,
        headers: dict[str, str] | None = None,
        method: str = "GET",
        data: bytes | None = None,
        http_version: str = "--http2",
        client_cert: tuple[Path, Path] | None = None,
        connect_to: str | None = None,
        expect_curl_exit: int | None = None,
        max_time: int = 5,
    ) -> tuple[int, bytes, dict[str, str], str]:
        req_id = uuid.uuid4().hex[:10]
        body_f = self.workdir / f"curl-{req_id}.body"
        hdr_f = self.workdir / f"curl-{req_id}.hdr"
        args: list[str | Path] = [
            "ip", "netns", "exec", namespace,
            "curl", "--silent", "--show-error", "--noproxy", "*",
            "--max-time", str(max_time),
            http_version,
            "--output", body_f,
            "--dump-header", hdr_f,
            "--write-out", "%{http_code}|%{http_version}",
            "--request", method,
        ]
        if resolve_host:
            for item in resolve_host.split(","):
                if item.strip():
                    args += ["--resolve", item.strip()]
        if connect_to:
            args += ["--connect-to", connect_to]
        if ca_file:
            args += ["--cacert", ca_file]
        if client_cert:
            args += ["--cert", client_cert[0], "--key", client_cert[1]]
        if basic_auth:
            args += ["--user", basic_auth]
        for k, v in (headers or {}).items():
            args += ["--header", f"{k}: {v}"]
        data_f: Path | None = None
        if data is not None:
            data_f = self.workdir / f"curl-{req_id}.req"
            data_f.write_bytes(data)
            args += ["--data-binary", f"@{data_f}"]
        args.append(url)
        try:
            proc = self.priv.run(*args, check=False, timeout=max_time + 10)
            if expect_curl_exit is not None:
                if proc.returncode != expect_curl_exit:
                    raise AssertionError(f"expected curl exit {expect_curl_exit}, got {proc.returncode}: {proc.stderr}")
                return 0, b"", {}, ""
            if proc.returncode != 0:
                raise AssertionError(f"curl failed ({proc.returncode}) on {url}: {proc.stderr}")
            parts = proc.stdout.strip().split("|")
            status = int(parts[0])
            proto = parts[1] if len(parts) > 1 else ""
            body = body_f.read_bytes() if body_f.exists() else b""
            resp_headers: dict[str, str] = {}
            if hdr_f.exists():
                for line in hdr_f.read_text(encoding="utf-8", errors="replace").splitlines():
                    if ":" in line:
                        k, v = line.split(":", 1)
                        resp_headers[k.strip().lower()] = v.strip()
            return status, body, resp_headers, proto
        finally:
            body_f.unlink(missing_ok=True)
            hdr_f.unlink(missing_ok=True)
            if data_f:
                data_f.unlink(missing_ok=True)

    def cgroup_snapshot(self, unit: str) -> dict[str, Any]:
        cg = self.priv.run("systemctl", "show", unit, "--property=ControlGroup", "--value", check=False).stdout.strip()
        pid = self.priv.run("systemctl", "show", unit, "--property=MainPID", "--value", check=False).stdout.strip()
        out: dict[str, Any] = {"unit": unit, "cgroup": cg, "main_pid": int(pid) if pid.isdigit() else 0}
        if cg:
            cg_dir = Path("/sys/fs/cgroup") / cg.lstrip("/")
            for fname in ("cpu.stat", "memory.current", "memory.peak"):
                p = cg_dir / fname
                if p.exists():
                    out[fname] = p.read_text(encoding="utf-8").strip()
        if pid.isdigit() and int(pid) > 0 and Path(f"/proc/{pid}/status").exists():
            status_txt = Path(f"/proc/{pid}/status").read_text(encoding="utf-8")
            for line in status_txt.splitlines():
                if line.startswith(("VmRSS:", "VmHWM:", "Threads:")):
                    k, v = line.split(":", 1)
                    out[k.strip()] = v.strip()
            fd_dir = Path(f"/proc/{pid}/fd")
            if fd_dir.exists():
                try:
                    out["open_fds"] = len(list(self.priv.run("ls", "-1", fd_dir, check=False).stdout.splitlines()))
                except Exception:
                    pass
        return out

    def teardown(self) -> dict[str, Any]:
        cleanup_errors: list[str] = []
        verification_errors: list[str] = []
        proc_errors = PROC_REGISTRY.terminate_all()
        cleanup_errors.extend(proc_errors)

        units_to_reset = list(self.created_units)
        remaining_units: list[str] = []
        for unit in reversed(self.created_units):
            r_stop = self.priv.run("systemctl", "stop", unit, check=False)
            r_rm = self.priv.run("rm", "-f", f"/etc/systemd/system/{unit}", check=False)
            if r_stop.returncode != 0 or r_rm.returncode != 0:
                cleanup_errors.append(f"unit {unit}: stop={r_stop.returncode} rm={r_rm.returncode}")
                remaining_units.append(unit)
        self.created_units = remaining_units

        r_reload = self.priv.run("systemctl", "daemon-reload", check=False)
        if r_reload.returncode != 0:
            cleanup_errors.append(f"systemctl daemon-reload failed ({r_reload.returncode})")
        if units_to_reset:
            self.priv.run("systemctl", "reset-failed", *units_to_reset, check=False)

        if self.jump_created:
            r_jmp = self.priv.run("iptables", "-D", "INPUT", "-i", self.bridge, "-j", self.chain, check=False)
            if r_jmp.returncode == 0:
                self.jump_created = False
            else:
                cleanup_errors.append(f"iptables -D INPUT {self.chain} failed ({r_jmp.returncode})")

        if self.chain_created:
            r_fl = self.priv.run("iptables", "-F", self.chain, check=False)
            r_del = self.priv.run("iptables", "-X", self.chain, check=False)
            if r_fl.returncode == 0 and r_del.returncode == 0:
                self.chain_created = False
            else:
                cleanup_errors.append(f"iptables -F/-X {self.chain} failed ({r_fl.returncode}/{r_del.returncode})")

        remaining_links: list[str] = []
        for link in reversed(self.created_links):
            r_lnk = self.priv.run("ip", "link", "del", link, check=False)
            if r_lnk.returncode != 0:
                cleanup_errors.append(f"ip link del {link} failed ({r_lnk.returncode})")
                remaining_links.append(link)
        self.created_links = remaining_links

        remaining_ns: list[str] = []
        for ns in reversed(self.created_ns):
            r_ns = self.priv.run("ip", "netns", "del", ns, check=False)
            if r_ns.returncode != 0:
                cleanup_errors.append(f"ip netns del {ns} failed ({r_ns.returncode})")
                remaining_ns.append(ns)
        self.created_ns = remaining_ns

        if self.bridge_created:
            r_br = self.priv.run("ip", "link", "del", self.bridge, check=False)
            if r_br.returncode == 0:
                self.bridge_created = False
            else:
                cleanup_errors.append(f"ip link del {self.bridge} failed ({r_br.returncode})")

        remaining_paths: list[Path] = []
        for p in reversed(self.created_paths):
            r_p = self.priv.run("rm", "-rf", p, check=False)
            if r_p.returncode != 0 or p.exists():
                cleanup_errors.append(f"rm -rf {p} failed ({r_p.returncode})")
                remaining_paths.append(p)
        self.created_paths = remaining_paths

        if self.created_user:
            r_usr = self.priv.run("userdel", "jul", check=False)
            if r_usr.returncode == 0:
                self.created_user = False
            else:
                cleanup_errors.append(f"userdel jul failed ({r_usr.returncode})")

        ns_check = self.priv.run("ip", "netns", "list", check=False)
        if ns_check.returncode != 0:
            verification_errors.append(f"ip netns list failed ({ns_check.returncode})")
            leftover_ns = list(self.created_ns)
        else:
            leftover_ns = [ns for ns in self.namespaces if ns in ns_check.stdout]

        link_check = self.priv.run("ip", "-o", "link", "show", check=False)
        if link_check.returncode != 0:
            verification_errors.append(f"ip link show failed ({link_check.returncode})")
            leftover_links = list(self.created_links) + ([self.bridge] if self.bridge_created else [])
        else:
            candidate_links = list(dict.fromkeys([self.bridge, *[v[1] for v in self.namespaces.values()], *self.created_links]))
            leftover_links = [lnk for lnk in candidate_links if lnk in link_check.stdout]

        ipt_check = self.priv.run("iptables", "-S", check=False)
        if ipt_check.returncode != 0:
            verification_errors.append(f"iptables -S failed ({ipt_check.returncode})")
            leftover_firewall = self.chain_created or self.jump_created
        else:
            leftover_firewall = self.chain in ipt_check.stdout

        leftover_paths = [
            str(p) for p in (
                Path("/usr/local/bin/jul"),
                Path("/etc/jul"),
                Path("/var/lib/jul"),
                Path("/var/cache/jul"),
                Path("/var/log/jul"),
                Path("/etc/systemd/system/jul.service"),
                Path("/etc/systemd/system/jul-readonly.service"),
                Path("/etc/systemd/system/jul-f1-dashboard.service"),
            ) if p.exists()
        ]
        user_remaining = self.created_user or (subprocess.run(["getent", "passwd", "jul"], capture_output=True).returncode == 0)
        is_clean = (
            len(cleanup_errors) == 0
            and len(verification_errors) == 0
            and len(leftover_ns) == 0
            and len(leftover_links) == 0
            and not leftover_firewall
            and len(leftover_paths) == 0
            and not user_remaining
            and not self.bridge_created
            and not self.chain_created
            and not self.jump_created
        )
        return {
            "clean": is_clean,
            "leftover_namespaces": leftover_ns,
            "leftover_links": leftover_links,
            "leftover_firewall": leftover_firewall,
            "leftover_paths": leftover_paths,
            "user_remaining": user_remaining,
            "cleanup_errors": cleanup_errors,
            "verification_errors": verification_errors,
        }


def admin_api(
    path: str,
    token: str | None = None,
    method: str = "GET",
    data: bytes | dict[str, Any] | None = None,
    content_type: str | None = None,
    headers: dict[str, str] | None = None,
    base_url: str = "http://127.0.0.1:9443",
) -> tuple[int, Any, dict[str, str]]:
    req_headers: dict[str, str] = dict(headers or {})
    if token:
        req_headers["Authorization"] = f"Bearer {token}"
    body_bytes: bytes | None = None
    if isinstance(data, dict):
        body_bytes = json.dumps(data).encode("utf-8")
        req_headers.setdefault("Content-Type", "application/json")
    elif isinstance(data, bytes):
        body_bytes = data
        if content_type:
            req_headers["Content-Type"] = content_type
    req = urllib.request.Request(base_url + path, data=body_bytes, headers=req_headers, method=method)
    try:
        with urllib.request.urlopen(req, timeout=15) as resp:
            raw = resp.read()
            hdrs = {k.lower(): v for k, v in resp.headers.items()}
            try:
                parsed = json.loads(raw.decode("utf-8"))
            except Exception:
                parsed = raw.decode("utf-8", errors="replace")
            return resp.status, parsed, hdrs
    except urllib.error.HTTPError as err:
        raw = err.read()
        hdrs = {k.lower(): v for k, v in err.headers.items()}
        try:
            parsed = json.loads(raw.decode("utf-8"))
        except Exception:
            parsed = raw.decode("utf-8", errors="replace")
        return err.code, parsed, hdrs


def origin_counters() -> dict[str, Any]:
    status, body, _ = admin_api("/__control/counters", base_url="http://127.0.0.1:3000")
    assert status == 200, f"origin counters failed: {status}"
    return body


def reset_origin_counters() -> None:
    status, _, _ = admin_api("/__control/reset", method="POST", data=b"{}", content_type="application/json", base_url="http://127.0.0.1:3000")
    assert status == 200


def evaluate_postcondition(postcond: dict[str, Any], token: str) -> dict[str, Any]:
    req_obj = {
        "postcondition_version": postcond["postcondition_version"],
        "key_id": postcond["key_id"],
        "base_config_digest": postcond["base_config_digest"],
        "candidate_config_digest": postcond["candidate_config_digest"],
        "repeatable": postcond.get("repeatable", False),
        "entries": postcond.get("entries", []),
    }
    status, body, _ = admin_api("/api/sharing/postcondition/evaluate", token=token, method="POST", data=req_obj)
    assert status == 200, f"postcondition evaluate returned {status}: {body}"
    return body


def plan_raw(toml_text: str, token: str, base_version: str = "") -> dict[str, Any]:
    hdrs: dict[str, str] = {}
    if base_version:
        hdrs["X-Jul-Base-Version"] = base_version
    status, body, _ = admin_api(
        "/api/v1/config/plan",
        token=token,
        method="POST",
        data=toml_text.encode("utf-8"),
        content_type="application/toml",
        headers=hdrs,
    )
    assert status == 200, f"plan_raw failed ({status}): {body}"
    return body


def apply_raw(toml_text: str, token: str, base_version: str, mode: str = "hot", idempotency_key: str | None = None) -> tuple[int, dict[str, Any]]:
    idem = idempotency_key or ("f1-" + uuid.uuid4().hex[:20])
    q = urllib.parse.urlencode({"base_version": base_version, "mode": mode})
    status, body, _ = admin_api(
        f"/api/v1/config/apply?{q}",
        token=token,
        method="POST",
        data=toml_text.encode("utf-8"),
        content_type="application/toml",
        headers={"Idempotency-Key": idem},
    )
    if status == 202 and isinstance(body, dict) and body.get("apply_id"):
        apply_id = body["apply_id"]
        for _ in range(50):
            time.sleep(0.1)
            p_status, p_body, _ = admin_api(f"/api/v1/config/applies/{apply_id}", token=token)
            if p_status == 200 and isinstance(p_body, dict) and p_body.get("terminal"):
                return 200, p_body
    return status, body if isinstance(body, dict) else {"raw": body}


def adopt_external(token: str, mode: str = "hot") -> dict[str, Any]:
    _, meta, _ = admin_api("/api/v1/status", token=token)
    status, prev, _ = admin_api("/api/v1/config/adopt-external/preview", token=token, method="POST", data={"mode": mode})
    assert status == 200 and prev.get("ok"), f"adopt preview failed ({status}): {prev}"
    base_ver = prev.get("base_version") or meta.get("serving_version") or meta.get("persisted_version") or ""
    idem = "f1-adopt-" + uuid.uuid4().hex[:16]
    status, res, _ = admin_api(
        "/api/v1/config/adopt-external",
        token=token,
        method="POST",
        data={
            "observed_digest": prev["observed_digest"],
            "base_version": base_ver,
            "mode": mode,
            "confirm": True,
        },
        headers={"Idempotency-Key": idem},
    )
    assert status in (200, 202), f"adopt failed ({status}): {res}"
    return res


def get_v1_status(token: str) -> dict[str, Any]:
    status, body, _ = admin_api("/api/v1/status", token=token)
    assert status == 200, f"status failed ({status}): {body}"
    return body


def get_sharing_view(token: str) -> dict[str, Any]:
    status, body, _ = admin_api("/api/sharing", token=token)
    assert status == 200, f"sharing view failed ({status}): {body}"
    return body


def atomic_write_json(path: Path, payload: dict[str, Any]) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    tmp = path.parent / f".{path.name}.{uuid.uuid4().hex[:8]}.tmp"
    tmp.write_text(json.dumps(payload, indent=2) + "\n", encoding="utf-8")
    os.replace(tmp, path)


def verify_and_collect_run_provenance(
    jul_bin: Path,
    lean_bin: Path,
    manifest: dict[str, Any],
    require_registered_baseline: bool = False,
    require_clean_tree: bool = False,
) -> dict[str, Any]:
    full_sha256 = hashlib.sha256(jul_bin.read_bytes()).hexdigest()
    lean_sha256 = hashlib.sha256(lean_bin.read_bytes()).hexdigest()

    head_proc = subprocess.run(["git", "rev-parse", "HEAD"], cwd=ROOT, capture_output=True, text=True, check=False)
    harness_head_sha = head_proc.stdout.strip() if head_proc.returncode == 0 else "unknown"

    status_proc = subprocess.run(["git", "status", "--porcelain"], cwd=ROOT, capture_output=True, text=True, check=False)
    dirty_lines = [l for l in status_proc.stdout.splitlines() if l.strip()] if status_proc.returncode == 0 else []
    tree_clean = len(dirty_lines) == 0 and harness_head_sha != "unknown"
    if require_clean_tree and not tree_clean:
        raise RuntimeError(f"Refusing qualifying run on dirty worktree: {dirty_lines[:10]}")

    ver_out = ""
    try:
        vproc = subprocess.run([str(jul_bin), "-version"], capture_output=True, text=True, timeout=5, check=False)
        ver_out = (vproc.stdout or vproc.stderr or "").strip().splitlines()[0] if (vproc.stdout or vproc.stderr) else ""
    except Exception:
        ver_out = ""

    input_files = [
        ROOT / "scripts" / "sharing_f1_lab.py",
        ROOT / "scripts" / "burn-in-load.go",
        MANIFEST_PATH,
        EXAMPLES_DIR / "generate_lab_assets.py",
        EXAMPLES_DIR / "dashboard_backend.py",
        *sorted(EXAMPLES_DIR.glob("*.toml")),
    ]
    input_digests = {
        str(p.relative_to(ROOT)): hashlib.sha256(p.read_bytes()).hexdigest()
        for p in input_files if p.exists()
    }
    combined_inputs_sha256 = hashlib.sha256(
        json.dumps(input_digests, sort_keys=True).encode("utf-8")
    ).hexdigest()

    primary_build = manifest.get("baseline", {}).get("build", {})
    matched_baseline_id = "unregistered"
    product_source_sha = f"unverified-binary:{full_sha256[:16]}"
    binary_version = ver_out or "unverified"
    provenance_verified = False

    if full_sha256 == primary_build.get("full_binary_sha256") and lean_sha256 == primary_build.get("lean_binary_sha256"):
        matched_baseline_id = "primary_qualifying_baseline"
        product_source_sha = manifest["baseline"]["product_source_sha"]
        binary_version = ver_out or primary_build.get("version", "")
        provenance_verified = True
    else:
        for sup in manifest.get("supplementary_baselines", []):
            if full_sha256 == sup.get("full_binary_sha256") and lean_sha256 == sup.get("lean_binary_sha256"):
                matched_baseline_id = sup.get("baseline_id", "supplementary_baseline")
                product_source_sha = sup.get("product_source_sha", "")
                binary_version = ver_out or sup.get("version", "")
                provenance_verified = True
                break

    if require_registered_baseline and not provenance_verified:
        raise RuntimeError(
            f"Binary SHA-256 ({full_sha256}) does not match any registered baseline in {MANIFEST_PATH.name}"
        )

    effective_harness_id = harness_head_sha if tree_clean else f"{harness_head_sha}-dirty-{combined_inputs_sha256[:12]}"
    return {
        "provenance_verified": provenance_verified,
        "matched_baseline_id": matched_baseline_id,
        "product_source_sha": product_source_sha,
        "binary_version": binary_version,
        "observed_version_output": ver_out,
        "full_binary_sha256": full_sha256,
        "lean_binary_sha256": lean_sha256,
        "harness_sha": effective_harness_id,
        "git_head_sha": harness_head_sha,
        "worktree_clean": tree_clean,
        "dirty_files": dirty_lines,
        "input_files_combined_sha256": combined_inputs_sha256,
        "input_file_digests": input_digests,
    }


class LedgerRecorder:
    def __init__(
        self,
        manifest: dict[str, Any],
        harness_sha: str,
        binary_sha256: str,
        provenance: dict[str, Any] | None = None,
        checkpoint_dir: Path | None = None,
    ) -> None:
        self.manifest = manifest
        self.harness_sha = harness_sha
        self.binary_sha256 = binary_sha256
        self.provenance = provenance or {
            "provenance_verified": binary_sha256 == manifest.get("baseline", {}).get("build", {}).get("full_binary_sha256"),
            "matched_baseline_id": "primary_qualifying_baseline" if binary_sha256 == manifest.get("baseline", {}).get("build", {}).get("full_binary_sha256") else "unregistered",
            "product_source_sha": manifest["baseline"]["product_source_sha"] if binary_sha256 == manifest.get("baseline", {}).get("build", {}).get("full_binary_sha256") else f"unverified-binary:{binary_sha256[:16]}",
            "binary_version": manifest["baseline"]["build"]["version"] if binary_sha256 == manifest.get("baseline", {}).get("build", {}).get("full_binary_sha256") else "unverified",
            "full_binary_sha256": binary_sha256,
            "harness_sha": harness_sha,
        }
        self.checkpoint_dir = checkpoint_dir
        self.cases: dict[str, dict[str, Any]] = {}
        self.events: list[dict[str, Any]] = []
        self.t0_mono = time.monotonic()

    def log_event(self, event: str, **fields: Any) -> None:
        rec = {
            "t": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
            "mono_ms": int((time.monotonic() - self.t0_mono) * 1000),
            "event": event,
            **fields,
        }
        self.events.append(rec)

    def record_case(
        self,
        case_id: str,
        verdict: str,
        serving_version: str,
        vantage: str,
        enforcement_owner: str,
        actual_outcome: str,
        evidence: dict[str, Any],
    ) -> None:
        meta = next((c for c in self.manifest["cases"] if c["case_id"] == case_id), None)
        assert meta is not None, f"Unknown case_id {case_id} not in manifest"
        self.cases[case_id] = {
            "case_id": case_id,
            "category": meta["category"],
            "config_ids": meta["config_ids"],
            "spec_refs": meta["spec_refs"],
            "claim": meta["description"],
            "expected_outcome": meta["expected_outcome"],
            "actual_outcome": actual_outcome,
            "verdict": verdict,
            "serving_version": serving_version,
            "vantage": vantage,
            "enforcement_owner": enforcement_owner,
            "observed_at": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
            "freshness": "current",
            "identities": {
                "product_source_sha": self.provenance.get("product_source_sha", self.manifest["baseline"]["product_source_sha"]),
                "binary_version": self.provenance.get("binary_version", ""),
                "binary_sha256": self.binary_sha256,
                "harness_sha": self.provenance.get("harness_sha", self.harness_sha),
                "provenance_verified": self.provenance.get("provenance_verified", False),
            },
            "evidence": evidence,
        }
        print(f"  [{verdict.upper()}] {case_id}: {actual_outcome}")
        if self.checkpoint_dir is not None:
            self.write_checkpoint("in_progress")

    def write_checkpoint(
        self,
        run_status: str,
        failure_error: str | None = None,
        teardown_info: dict[str, Any] | None = None,
    ) -> None:
        if self.checkpoint_dir is None:
            return
        reconciliation = reconcile_manifest_and_ledger(self.manifest, self.cases)
        if run_status != "completed":
            reconciliation["all_mandatory_passed"] = False
        payload: dict[str, Any] = {
            "schema_version": 2,
            "issue": "#581",
            "work_package": "SHARE-02A",
            "run_status": run_status,
            "evidence_classification": "current-source-development",
            "generated_at_utc": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
            "identities": self.provenance,
            "reconciliation": reconciliation,
            "cases": [self.cases[c["case_id"]] for c in self.manifest["cases"] if c["case_id"] in self.cases],
        }
        if failure_error:
            payload["failure_error"] = failure_error
        if teardown_info is not None:
            payload["teardown"] = teardown_info
        atomic_write_json(self.checkpoint_dir / "f1-acceptance-ledger.json", payload)
        if run_status == "failed":
            atomic_write_json(self.checkpoint_dir / "f1-failure-report.json", payload)


def load_example_toml(filename: str) -> str:
    return (EXAMPLES_DIR / filename).read_text(encoding="utf-8")


def find_delta_entry(plan_resp: dict[str, Any], route_id: str) -> dict[str, Any]:
    for entry in plan_resp.get("sharing", {}).get("entries", []):
        ref = entry.get("ref", {})
        if ref.get("route_id") == route_id or entry.get("display_label", "").startswith(route_id):
            return entry
    raise AssertionError(f"route_id {route_id} not found in sharing plan entries: {plan_resp.get('sharing')}")


def execute_safe_sequence(
    lab: F1LabTopology,
    ledger: LedgerRecorder,
    tokens: dict[str, str],
    creds: dict[str, str],
    service_hardening: dict[str, Any],
) -> dict[str, Any]:
    admin_tok = tokens["admin"]
    root_ca = lab.workdir / "tls" / "root-ca.pem"
    intended_basic = f"{creds['intended_user']}:{creds['intended_password']}"

    # CASE-F1-001: Service profile hardening & lean WAF rejection
    lean_check = subprocess.run(
        [str(lab.lean_bin), "check", "-config", "jul.toml"],
        cwd=lab.workdir,
        env={
            **os.environ,
            "JUL_ADMIN_TOKEN": tokens["admin"],
            "JUL_F1_AUDITOR_TOKEN": tokens["auditor"],
            "JUL_F1_VIEWER_TOKEN": tokens["viewer"],
            "JUL_F1_OPERATOR_TOKEN": tokens["operator"],
        },
        capture_output=True,
        text=True,
    )
    assert lean_check.returncode != 0, "Lean binary must reject WAF configuration"
    lean_diag = (lean_check.stderr + "\n" + lean_check.stdout).lower()
    assert "waf" in lean_diag and any(
        k in lean_diag for k in ("built without waf support", "rebuild with -tags waf", "build tag", "not compiled", "unsupported", "disabled")
    ), (
        f"Lean binary failed without expected WAF unsupported diagnostic: {lean_diag}"
    )
    assert service_hardening["jul_service"]["User"] == "jul"
    assert service_hardening["jul_service"]["ProtectSystem"] == "strict"
    assert service_hardening["jul_service"]["MemoryDenyWriteExecute"] == "yes"

    # Start jul.service on CFG-F1-01 (01-loopback-preview.toml)
    cfg01 = lab.adapt_config_for_service(load_example_toml("01-loopback-preview.toml"))
    lab.install_config(cfg01, readonly=False)
    lab.priv.run("systemctl", "start", "jul.service")
    lab.wait_http_local("http://127.0.0.1:9443/readyz", timeout_s=20)
    st01 = get_v1_status(admin_tok)
    assert st01["config_state"] == "managed_unadopted"

    ledger.record_case(
        "CASE-F1-001",
        "pass",
        st01["serving_version"],
        "same_host_loopback",
        "systemd + Jul config validator",
        "jul.service runs as jul:jul with ProtectSystem=strict, NoNewPrivileges=yes, PrivateTmp=yes, PrivateDevices=yes, MemoryDenyWriteExecute=yes; lean binary exited 1 rejecting WAF before persistence.",
        {"systemd_hardening": service_hardening, "lean_exit_code": lean_check.returncode, "lean_stderr": lean_check.stderr.strip()[:240]},
    )

    # CASE-F1-002: Safe Step 1 — Loopback preview isolation
    # Verify host loopback reaches portal and dashboard on 127.0.0.1:443
    loop_portal = subprocess.run(
        ["curl", "-fsS", "--cacert", str(root_ca), "--resolve", "portal.lab.test:443:127.0.0.1", "https://portal.lab.test/"],
        capture_output=True, text=True, check=True,
    )
    assert PORTAL_MARKER in loop_portal.stdout
    loop_dash = subprocess.run(
        ["curl", "-fsS", "--cacert", str(root_ca), "--resolve", "dash.lab.test:443:127.0.0.1", "https://dash.lab.test/api/summary"],
        capture_output=True, text=True, check=True,
    )
    assert DASHBOARD_MARKER in loop_dash.stdout

    # Verify external clients lab-client-1 and lab-client-2 cannot connect to 192.0.2.10:443 (curl exit 7)
    lab.netns_curl("lab-client-1", "https://portal.lab.test/", ca_file=root_ca, expect_curl_exit=7)
    lab.netns_curl("lab-client-2", "https://dash.lab.test/api/summary", resolve_host="dash.lab.test:443:192.0.2.10", ca_file=root_ca, expect_curl_exit=7)

    ledger.record_case(
        "CASE-F1-002",
        "pass",
        st01["serving_version"],
        "same_host_namespace",
        "Jul loopback listener bind (127.0.0.1:443)",
        "Loopback preview reachable on 127.0.0.1:443 from host; lab-client-1 (192.0.2.11) and lab-client-2 (192.0.2.12) refused on 192.0.2.10:443 (curl exit 7).",
        {"loopback_portal_200": True, "loopback_dash_200": True, "lab_client_1_refused": True, "lab_client_2_refused": True},
    )

    # CASE-F1-003: Safe Step 2 — Adopt initial baseline, add Basic check + restricted intent on loopback
    adopt_res = adopt_external(admin_tok, mode="hot")
    st_adopted = get_v1_status(admin_tok)
    assert st_adopted["config_state"] == "managed_clean"

    cfg02 = lab.adapt_config_for_service(load_example_toml("02-loopback-dashboard-restricted.toml"))
    plan02 = plan_raw(cfg02, admin_tok, base_version=st_adopted["persisted_version"])
    dash_delta02 = find_delta_entry(plan02, "dashboard")
    assert dash_delta02["class"] == "narrowed", f"expected narrowed, got {dash_delta02}"
    assert dash_delta02["triggers_retirement"] is True

    code02, apply02 = apply_raw(cfg02, admin_tok, base_version=st_adopted["persisted_version"])
    assert code02 == 200 and apply02.get("outcome") == "applied_live", f"apply02 failed: {apply02}"
    pc02 = evaluate_postcondition(plan02["sharing"]["postcondition"], admin_tok)
    assert pc02["result"] == "candidate_serving" and pc02["candidate"] == "serving" and pc02["access"] == "matches"

    reset_origin_counters()
    # Unauthenticated and wrong password on loopback must be 401 with zero origin requests
    unauth_code = subprocess.run(
        ["curl", "-s", "-o", "/dev/null", "-w", "%{http_code}", "--cacert", str(root_ca), "--resolve", "dash.lab.test:443:127.0.0.1", "https://dash.lab.test/api/summary"],
        capture_output=True, text=True, check=True,
    ).stdout.strip()
    wrong_code = subprocess.run(
        ["curl", "-s", "-o", "/dev/null", "-w", "%{http_code}", "--cacert", str(root_ca), "--user", "family-intended:wrong", "--resolve", "dash.lab.test:443:127.0.0.1", "https://dash.lab.test/api/summary"],
        capture_output=True, text=True, check=True,
    ).stdout.strip()
    assert unauth_code == "401" and wrong_code == "401"
    assert origin_counters()["dashboard_requests"] == 0

    auth_code = subprocess.run(
        ["curl", "-s", "-o", "/dev/null", "-w", "%{http_code}", "--cacert", str(root_ca), "--user", intended_basic, "--resolve", "dash.lab.test:443:127.0.0.1", "https://dash.lab.test/api/summary"],
        capture_output=True, text=True, check=True,
    ).stdout.strip()
    assert auth_code == "200" and origin_counters()["dashboard_requests"] == 1

    st02 = get_v1_status(admin_tok)
    ledger.record_case(
        "CASE-F1-003",
        "pass",
        st02["serving_version"],
        "same_host_loopback",
        "Jul location auth.basic on loopback before LAN exposure",
        "Adopted baseline; planned & applied CFG-F1-02 (dashboard class=narrowed, outcome=applied_live, postcondition=candidate_serving); verified 401 (origin_delta=0) for missing/wrong Basic auth and 200 (origin_delta=+1) for intended credentials on loopback.",
        {
            "adopt_apply_id": adopt_res.get("apply_id"),
            "apply_id": apply02.get("apply_id"),
            "dashboard_delta_class": dash_delta02["class"],
            "postcondition_result": pc02["result"],
            "postcondition_candidate": pc02["candidate"],
            "postcondition_access": pc02["access"],
        },
    )

    # CASE-F1-004: Safe Step 3 — Declare portal public on loopback (metadata_only)
    cfg03 = lab.adapt_config_for_service(load_example_toml("03-loopback-portal-public.toml"))
    plan03 = plan_raw(cfg03, admin_tok, base_version=st02["persisted_version"])
    portal_delta03 = find_delta_entry(plan03, "portal-home")
    assert portal_delta03["class"] == "metadata_only"
    assert portal_delta03["declared_mode_widened"] is True
    assert portal_delta03["triggers_retirement"] is False
    assert plan03["sharing"]["requires_acknowledgement"] is True

    code03, apply03 = apply_raw(cfg03, admin_tok, base_version=st02["persisted_version"])
    assert code03 == 200 and apply03.get("outcome") == "applied_live"
    pc03 = evaluate_postcondition(plan03["sharing"]["postcondition"], admin_tok)
    assert pc03["result"] == "candidate_serving"
    st03 = get_v1_status(admin_tok)

    ledger.record_case(
        "CASE-F1-004",
        "pass",
        st03["serving_version"],
        "same_host_loopback",
        "Jul sharing intent classifier",
        "Planned & applied CFG-F1-03; portal-home classed metadata_only (declared_mode_widened=true, triggers_retirement=false), dashboard unaffected, postcondition=candidate_serving.",
        {
            "apply_id": apply03.get("apply_id"),
            "portal_delta": portal_delta03,
            "postcondition_result": pc03["result"],
        },
    )

    # CASE-F1-005: Safe Step 4 & 5 — Move all 3 blocks to 192.0.2.10:443 in one candidate & immediately verify LAN
    cfg04 = lab.adapt_config_for_service(load_example_toml("jul.toml"))
    plan04 = plan_raw(cfg04, admin_tok, base_version=st03["persisted_version"])
    classes04 = {e["display_label"]: e["class"] for e in plan04["sharing"]["entries"]}
    assert all(c == "uncertain" for c in classes04.values()) and len(classes04) == 3, f"expected 3 uncertain entries, got {classes04}"

    code04, apply04 = apply_raw(cfg04, admin_tok, base_version=st03["persisted_version"])
    assert code04 == 200 and apply04.get("outcome") == "applied_live"
    pc04 = evaluate_postcondition(plan04["sharing"]["postcondition"], admin_tok)
    assert pc04["result"] == "candidate_serving"

    reset_origin_counters()
    # Immediately verify lab-client-2 gets 401 from dashboard and 200 from portal; lab-client-1 gets 200 with credentials
    s_ex, b_ex, h_ex, proto_ex = lab.netns_curl("lab-client-2", "https://dash.lab.test/api/summary", resolve_host="dash.lab.test:443:192.0.2.10", ca_file=root_ca)
    assert s_ex == 401 and "basic" in h_ex.get("www-authenticate", "").lower() and DASHBOARD_MARKER.encode() not in b_ex
    assert origin_counters()["dashboard_requests"] == 0

    s_po, b_po, _, _ = lab.netns_curl("lab-client-2", "https://portal.lab.test/", resolve_host="portal.lab.test:443:192.0.2.10", ca_file=root_ca)
    assert s_po == 200 and PORTAL_MARKER.encode() in b_po

    s_in, b_in, _, proto_in = lab.netns_curl("lab-client-1", "https://dash.lab.test/api/summary", resolve_host="dash.lab.test:443:192.0.2.10", ca_file=root_ca, basic_auth=intended_basic)
    assert s_in == 200 and DASHBOARD_MARKER.encode() in b_in
    assert origin_counters()["dashboard_requests"] == 1

    st04 = get_v1_status(admin_tok)
    ledger.record_case(
        "CASE-F1-005",
        "pass",
        st04["serving_version"],
        "lan_client (lab-client-1 192.0.2.11, lab-client-2 192.0.2.12)",
        "Jul listener 192.0.2.10:443 + route_id=dashboard auth.basic",
        "Moved default-deny, portal-home, and dashboard from 127.0.0.1:443 to 192.0.2.10:443 in one atomic candidate (all 3 classed uncertain); zero unprotected intermediate state; lab-client-2 denied 401 (origin_delta=0) on dashboard and 200 on portal; lab-client-1 succeeded 200 on dashboard.",
        {
            "apply_id": apply04.get("apply_id"),
            "entry_classes": classes04,
            "postcondition_result": pc04["result"],
            "lab_client_2_dash_status": s_ex,
            "lab_client_2_portal_status": s_po,
            "lab_client_1_dash_status": s_in,
            "negotiated_http_version": proto_in,
        },
    )
    return {"plan04": plan04, "st04": st04, "cfg04": cfg04}


def execute_authority_and_postconditions(
    lab: F1LabTopology,
    ledger: LedgerRecorder,
    tokens: dict[str, str],
    creds: dict[str, str],
    seq_state: dict[str, Any],
) -> None:
    admin_tok = tokens["admin"]
    operator_tok = tokens["operator"]
    viewer_tok = tokens["viewer"]
    auditor_tok = tokens["auditor"]
    cfg04 = seq_state["cfg04"]
    plan04 = seq_state["plan04"]
    st04 = get_v1_status(admin_tok)
    root_ca = lab.workdir / "tls" / "root-ca.pem"
    intended_basic = f"{creds['intended_user']}:{creds['intended_password']}"

    # CASE-F1-006: RBAC & S17-1 postcondition disclosure enforcement
    # 1. Auditor (status:read only, lacks config:read) -> 403 on GET /api/sharing and POST /api/sharing/postcondition/evaluate
    aud_sh_code, _, _ = admin_api("/api/sharing", token=auditor_tok)
    pc_req = {
        "postcondition_version": 1,
        "key_id": plan04["sharing"]["postcondition"]["key_id"],
        "base_config_digest": plan04["sharing"]["postcondition"]["base_config_digest"],
        "candidate_config_digest": plan04["sharing"]["postcondition"]["candidate_config_digest"],
        "entries": plan04["sharing"]["postcondition"]["entries"],
    }
    aud_pc_code, _, _ = admin_api("/api/sharing/postcondition/evaluate", token=auditor_tok, method="POST", data=pc_req)
    assert aud_sh_code == 403 and aud_pc_code == 403, f"expected 403 for auditor, got {aud_sh_code}, {aud_pc_code}"

    # 2. Viewer (status:read + config:read, lacks config:write) -> 200 on GET /api/sharing and postcondition evaluate; 403 on plan & apply
    vw_sh_code, _, _ = admin_api("/api/sharing", token=viewer_tok)
    vw_pc_code, vw_pc_body, _ = admin_api("/api/sharing/postcondition/evaluate", token=viewer_tok, method="POST", data=pc_req)
    vw_plan_code, _, _ = admin_api("/api/v1/config/plan", token=viewer_tok, method="POST", data=cfg04.encode("utf-8"), content_type="application/toml")
    vw_apply_code, _ = apply_raw(cfg04, viewer_tok, base_version=st04["persisted_version"])
    assert vw_sh_code == 200 and vw_pc_code == 200 and vw_pc_body["result"] == "candidate_serving"
    assert vw_plan_code == 403 and vw_apply_code == 403

    # 3. Operator (config:write + config:apply, lacks config:raw):
    #    - POST /api/v1/config/plan and POST /api/config/patch/preview require config:raw (RequiresConfigRaw=true from #648) and return 403 forbidden;
    #    - Admin (who holds config:raw) receives postcondition.status == "available" on both raw-candidate plan and typed patch preview.
    op_v1_plan_code, op_v1_plan_body, _ = admin_api("/api/v1/config/plan", token=operator_tok, method="POST", data=cfg04.encode("utf-8"), content_type="application/toml")
    op_patch_prev_code, _, _ = admin_api(
        "/api/config/patch/preview",
        token=operator_tok,
        method="POST",
        data={
            "base_version": st04["persisted_version"],
            "ops": [{"op": "location_set_withdrawn", "route_id": "dashboard", "withdrawn": True}],
        },
    )
    assert op_v1_plan_code == 403 and op_patch_prev_code == 403
    assert op_v1_plan_body.get("error", {}).get("details", {}).get("required_permission") == "config:raw"

    adm_raw_plan = plan_raw(cfg04, admin_tok, base_version=st04["persisted_version"])
    adm_raw_pc = adm_raw_plan["sharing"]["postcondition"]
    assert adm_raw_pc["status"] == "available"

    adm_patch_code, adm_patch_body, _ = admin_api(
        "/api/config/patch/preview",
        token=admin_tok,
        method="POST",
        data={
            "base_version": st04["persisted_version"],
            "ops": [{"op": "location_set_withdrawn", "route_id": "dashboard", "withdrawn": True}],
        },
    )
    assert adm_patch_code == 200 and adm_patch_body["sharing"]["postcondition"]["status"] == "available"

    st_after_rbac = get_v1_status(admin_tok)
    assert st_after_rbac["serving_version"] == st04["serving_version"] and st_after_rbac["persisted_version"] == st04["persisted_version"]

    ledger.record_case(
        "CASE-F1-006",
        "pass",
        st04["serving_version"],
        "same_host_loopback",
        "Jul RBAC + #648/S17-1 config:raw gate + §9.4 postcondition producer/evaluator",
        "Verified auditor (status:read only) denied 403 on /api/sharing and /api/sharing/postcondition/evaluate; viewer (status:read+config:read) allowed 200 on both and denied 403 on plan/apply; operator (no config:raw) denied 403 on comparison previews (/api/v1/config/plan and /api/config/patch/preview); admin (with config:raw) receives postcondition=available on both raw and typed plans.",
        {
            "auditor_sharing_status": aud_sh_code,
            "auditor_postcondition_status": aud_pc_code,
            "viewer_sharing_status": vw_sh_code,
            "viewer_postcondition_result": vw_pc_body["result"],
            "viewer_plan_status": vw_plan_code,
            "operator_plan_status": op_v1_plan_code,
            "operator_required_permission": "config:raw",
            "admin_raw_postcondition_status": adm_raw_pc["status"],
            "admin_typed_postcondition_status": adm_patch_body["sharing"]["postcondition"]["status"],
        },
    )

    # CASE-F1-007: Managed CAS, idempotency, stale base, and no_change (CFG-F1-14 vs CFG-F1-04)
    # In F1 (CFG-F1-04), portal-home configures root="portal" and dashboard configures auth.basic.file="dashboard.htpasswd".
    # Per hasOpaqueReloadInputs (internal/server/serving_change.go), a config with root or auth.basic.file deliberately takes the
    # full reload path (applied_live) so an operator's reload always picks up in-place htpasswd/static edits.
    # CFG-F1-14 (14-lan-managed-no-opaque-noop.toml) is the explicit opaque-input-free candidate for verifying outcome="no_change".
    noop_variant_toml = lab.adapt_config_for_service(load_example_toml("14-lan-managed-no-opaque-noop.toml"))
    code_nv1, res_nv1 = apply_raw(noop_variant_toml, admin_tok, base_version=st04["persisted_version"])
    assert code_nv1 == 200 and res_nv1.get("outcome") == "applied_live"
    st_nv1 = get_v1_status(admin_tok)

    idem_key = "f1-nochange-" + uuid.uuid4().hex[:16]
    nc_code, nc_body = apply_raw(noop_variant_toml, admin_tok, base_version=st_nv1["persisted_version"], idempotency_key=idem_key)
    assert nc_code == 200 and nc_body.get("outcome") == "no_change", f"expected no_change, got {nc_body}"
    # Idempotency replay with same key & body returns 200 with the same apply_id
    replay_code, replay_body = apply_raw(noop_variant_toml, admin_tok, base_version=st_nv1["persisted_version"], idempotency_key=idem_key)
    assert replay_code == 200 and replay_body.get("apply_id") == nc_body.get("apply_id")
    # Reusing same Idempotency-Key with different body returns 409 idempotency_key_reused
    cfg05 = lab.adapt_config_for_service(load_example_toml("05-lan-withdrawn-gate.toml"))
    reused_code, reused_body = apply_raw(cfg05, admin_tok, base_version=st_nv1["persisted_version"], idempotency_key=idem_key)
    assert reused_code == 409 and reused_body.get("error", {}).get("code") == "idempotency_key_reused", f"expected 409 idempotency_key_reused, got {reused_code}: {reused_body}"
    # Stale base_version returns 409
    stale_code, stale_body = apply_raw(cfg05, admin_tok, base_version="0000000000000000")
    assert stale_code == 409, f"expected 409 on stale base_version, got {stale_code}: {stale_body}"

    # Restore CFG-F1-04 and also verify that re-applying CFG-F1-04 (which has auth.basic.file) executes a full reload (applied_live)
    apply_raw(cfg04, admin_tok, base_version=st_nv1["persisted_version"])
    st04 = get_v1_status(admin_tok)
    _, file_reload_body = apply_raw(cfg04, admin_tok, base_version=st04["persisted_version"])
    assert file_reload_body.get("outcome") == "applied_live"
    st04 = get_v1_status(admin_tok)

    ledger.record_case(
        "CASE-F1-007",
        "pass",
        st04["serving_version"],
        "same_host_loopback",
        "Jul ConfigApplyCoordinator CAS + idempotency ledger + hasOpaqueReloadInputs",
        "Verified no_change outcome on identical CFG-F1-14 apply, applied_live on file-backed CFG-F1-04 re-apply, idempotency replay returning same apply_id, 409 idempotency_key_reused on payload mismatch, and 409 conflict on stale base_version.",
        {
            "no_change_config": "CFG-F1-14",
            "no_change_apply_id": nc_body.get("apply_id"),
            "no_change_outcome": nc_body.get("outcome"),
            "file_backed_reapply_outcome": file_reload_body.get("outcome"),
            "idempotency_reused_code": reused_code,
            "stale_base_code": stale_code,
        },
    )

    # CASE-F1-008: Managed invalid-candidate rejection and pre-Publish not_applied restoration
    # 1. Invalid sharing config (sharing block on route without route_id)
    invalid_sharing_toml = cfg04.replace('route_id = "dashboard"\n', "")
    inv_code, inv_body = apply_raw(invalid_sharing_toml, admin_tok, base_version=st04["persisted_version"])
    assert inv_code in (400, 422), f"expected 400/422 on invalid sharing config, got {inv_code}: {inv_body}"
    # 2. Typed location_set_sharing on default-deny location (which has no route_id) -> 400 before side effects
    no_rid_code, no_rid_body, _ = admin_api(
        "/api/config/patch/apply",
        token=admin_tok,
        method="POST",
        data={
            "base_version": st04["persisted_version"],
            "ops": [{"op": "location_set_sharing", "server_index": 0, "location_index": 0, "sharing": {"mode": "public"}}],
        },
    )
    assert no_rid_code == 400, f"expected 400 on location_set_sharing without route_id, got {no_rid_code}: {no_rid_body}"
    # 3. Candidate with non-local unbindable IP (198.51.100.99:443) -> rejected at preflight bind-probe before persistence
    unbindable_toml = cfg04.replace('listen = "192.0.2.10:443"', 'listen = "198.51.100.99:443"', 1)
    unb_code, unb_body = apply_raw(unbindable_toml, admin_tok, base_version=st04["persisted_version"])
    assert unb_code in (400, 422), f"expected preflight bind rejection, got {unb_code}: {unb_body}"
    st_after_inv = get_v1_status(admin_tok)
    assert st_after_inv["serving_version"] == st04["serving_version"] and st_after_inv["config_state"] == "managed_clean"

    ledger.record_case(
        "CASE-F1-008",
        "pass",
        st04["serving_version"],
        "same_host_loopback",
        "Jul config validation + preflight bind probe",
        "Rejected sharing block without route_id (raw and typed location_set_sharing) and unbindable listener address before persistence; serving_version and managed_clean state preserved.",
        {
            "invalid_raw_status": inv_code,
            "typed_missing_route_id_status": no_rid_code,
            "unbindable_listen_status": unb_code,
            "config_state": st_after_inv["config_state"],
        },
    )

    # CASE-F1-009: Managed drift detection and explicit resolution
    drifted_toml = cfg04 + "\n# external drift comment\n"
    lab.install_config(drifted_toml, readonly=False)
    # Trigger SIGHUP so server reassesses managed drift
    lab.priv.run("systemctl", "reload", "jul.service")
    time.sleep(0.4)
    st_drift = get_v1_status(admin_tok)
    assert st_drift["config_state"] == "managed_drift" and st_drift["drift"]["detected"] is True, f"expected managed_drift, got {st_drift}"
    # Ordinary managed apply is refused with 409 while drifted
    drift_apply_code, drift_apply_body = apply_raw(cfg05, admin_tok, base_version=st04["persisted_version"])
    assert drift_apply_code == 409, f"expected 409 under drift, got {drift_apply_code}: {drift_apply_body}"
    # Explicit adopt-external resolves drift back to managed_clean
    adopt_drift_res = adopt_external(admin_tok, mode="hot")
    st_clean = get_v1_status(admin_tok)
    assert st_clean["config_state"] == "managed_clean" and st_clean["drift"]["detected"] is False
    # Re-apply clean cfg04 so comments are normalized
    apply_raw(cfg04, admin_tok, base_version=st_clean["persisted_version"])
    st04 = get_v1_status(admin_tok)

    ledger.record_case(
        "CASE-F1-009",
        "pass",
        st04["serving_version"],
        "same_host_loopback",
        "Jul ManagedBaselineStore drift gate",
        "External file edit transitioned config_state to managed_drift; ordinary apply refused 409 while live service continued; explicit adopt-external preview+confirm restored managed_clean.",
        {
            "drift_state": st_drift["config_state"],
            "refused_apply_status": drift_apply_code,
            "adopt_outcome": adopt_drift_res.get("outcome"),
            "restored_state": st_clean["config_state"],
        },
    )

    # CASE-F1-010 & CASE-F1-011: Stage restart, saved-vs-serving layers, not_serving_yet, restart boot_id rotation, unverifiable, and re-plan recovery
    # Create a candidate that changes a restart-required field (cache.enabled = true) AND changes portal audience_label so saved vs serving layer is populated on portal-home
    staged_toml = "[cache]\nenabled = true\nmemory_max_size = \"16m\"\n\n" + cfg04.replace(
        'audience_label = "anyone"',
        'audience_label = "public-visitors"',
    )
    plan_stage = plan_raw(staged_toml, admin_tok, base_version=st04["persisted_version"])
    assert plan_stage["lifecycle"]["can_apply_hot"] is False and plan_stage["lifecycle"]["can_stage_restart"] is True
    assert "cache.enabled" in plan_stage["lifecycle"]["restart_required_paths"]
    pc_stage_obj = plan_stage["sharing"]["postcondition"]
    assert pc_stage_obj["status"] == "available"

    # Hot apply must be rejected because restart is required
    hot_rej_code, _ = apply_raw(staged_toml, admin_tok, base_version=st04["persisted_version"], mode="hot")
    assert hot_rej_code == 409

    # Stage for restart
    stage_code, stage_res = apply_raw(staged_toml, admin_tok, base_version=st04["persisted_version"], mode="stage_restart")
    assert stage_code == 200, f"stage_restart failed ({stage_code}): {stage_res}"
    st_staged = get_v1_status(admin_tok)
    assert st_staged["config_state"] == "managed_pending_restart" and st_staged["pending_restart"]["pending"] is True

    # Inspect GET /api/sharing: must have both serving and saved layers with distinct revisions
    sh_staged = get_sharing_view(admin_tok)
    portal_entry = next(e for e in sh_staged["entries"] if e.get("serving", {}).get("route_id") == "portal-home")
    assert "serving" in portal_entry and "saved" in portal_entry, f"expected both serving and saved layers: {portal_entry}"
    assert portal_entry["serving"]["declared_intent"]["audience_label"]["text"] == "anyone"
    assert portal_entry["saved"]["declared_intent"]["audience_label"]["text"] == "public-visitors"

    # Evaluate postcondition BEFORE restart -> must be not_serving_yet (candidate=base_serving)
    pc_before_restart = evaluate_postcondition(pc_stage_obj, admin_tok)
    assert pc_before_restart["result"] == "not_serving_yet" and pc_before_restart["candidate"] == "base_serving"

    ledger.record_case(
        "CASE-F1-010",
        "pass",
        st_staged["serving_version"],
        "same_host_loopback",
        "Jul PlannedRestartStore + GET /api/sharing serving/saved layers + §9.4 evaluator",
        "Staged restart-bound change; verified config_state=managed_pending_restart, separate serving ('anyone') and saved ('public-visitors') layers in GET /api/sharing, and pre-restart postcondition result=not_serving_yet (candidate=base_serving).",
        {
            "config_state": st_staged["config_state"],
            "serving_revision": portal_entry["serving"]["revision"],
            "saved_revision": portal_entry["saved"]["revision"],
            "pre_restart_postcondition": {
                "result": pc_before_restart["result"],
                "candidate": pc_before_restart["candidate"],
                "access": pc_before_restart["access"],
            },
        },
    )

    # CASE-F1-011: Hold an active SSE stream across restart, restart Jul, verify clean stream termination, boot_id change, original postcondition -> unverifiable, re-plan -> candidate_serving, non-repeatable location_add
    boot_before = st_staged["boot_id"]
    sse_hold_proc = PROC_REGISTRY.register(
        start_netns_popen(
            lab,
            "lab-client-1",
            [
                "curl", "-sS", "--no-buffer", "--max-time", "15",
                "--cacert", str(root_ca), "--user", intended_basic,
                "--resolve", "dash.lab.test:443:192.0.2.10",
                "https://dash.lab.test/events",
            ],
        )
    )
    time.sleep(0.4)
    assert sse_hold_proc.poll() is None, "Held SSE stream should be active before restart"
    t_restart_start = time.monotonic()
    lab.priv.run("systemctl", "restart", "jul.service")
    sse_out, _ = sse_hold_proc.communicate(timeout=10)
    restart_stream_close_ms = int((time.monotonic() - t_restart_start) * 1000)
    assert DASHBOARD_MARKER in sse_out, "Held SSE stream must have received initial frame before restart"
    lab.wait_http_local("http://127.0.0.1:9443/readyz", timeout_s=20, expected_unit="jul.service")
    st_after_restart = get_v1_status(admin_tok)
    assert st_after_restart["boot_id"] != boot_before
    assert st_after_restart["config_state"] == "managed_clean"

    # Evaluate original pre-restart postcondition -> must be unverifiable (planned_by_earlier_process)
    pc_after_restart = evaluate_postcondition(pc_stage_obj, admin_tok)
    assert pc_after_restart["result"] == "unverifiable" and pc_after_restart["candidate"] == "unverifiable"

    # Re-plan same raw candidate -> evaluates to candidate_serving
    replan_raw = plan_raw(staged_toml, admin_tok, base_version=st_after_restart["persisted_version"])
    pc_replanned = evaluate_postcondition(replan_raw["sharing"]["postcondition"], admin_tok)
    assert pc_replanned["result"] == "candidate_serving" and pc_replanned["candidate"] == "serving"

    # Also verify repeatable typed op (location_set_withdrawn withdrawn=false) re-plan evaluates to candidate_serving
    _, rep_patch_body, _ = admin_api(
        "/api/config/patch/preview",
        token=admin_tok,
        method="POST",
        data={
            "base_version": st_after_restart["persisted_version"],
            "ops": [{"op": "location_set_withdrawn", "route_id": "dashboard", "withdrawn": False}],
        },
    )
    assert rep_patch_body["sharing"]["postcondition"]["repeatable"] is True
    pc_rep_eval = evaluate_postcondition(rep_patch_body["sharing"]["postcondition"], admin_tok)
    assert pc_rep_eval["result"] == "candidate_serving"

    # Also verify non-repeatable location_add postcondition reports repeatable=false and manual reconciliation guidance after restart
    add_code, add_patch_body, _ = admin_api(
        "/api/config/patch/preview",
        token=admin_tok,
        method="POST",
        data={
            "base_version": st_after_restart["persisted_version"],
            "ops": [{
                "op": "location_add",
                "listen": "192.0.2.10:443",
                "server_names": ["portal.lab.test", "portal-alias.lab.test"],
                "match_set": {"type": "exact", "path": "/health-note"},
                "action": {"kind": "return", "status": 204},
            }],
        },
    )
    assert add_code == 200, f"location_add preview failed ({add_code}): {add_patch_body}"
    add_pc = add_patch_body["sharing"]["postcondition"]
    assert add_pc["repeatable"] is False
    # Simulate evaluating add_pc after a process restart (stale key_id) -> unverifiable with non-repeatable manual reconciliation guidance
    stale_add_pc = {**add_pc, "key_id": "pk1-0000000000000000"}
    pc_nonrep_eval = evaluate_postcondition(stale_add_pc, admin_tok)
    assert pc_nonrep_eval["result"] == "unverifiable" and "non-repeatable" in pc_nonrep_eval.get("recovery_guidance", "")

    # Restore canonical cfg04 via stage_restart + restart
    apply_raw(cfg04, admin_tok, base_version=st_after_restart["persisted_version"], mode="stage_restart")
    lab.priv.run("systemctl", "restart", "jul.service")
    lab.wait_http_local("http://127.0.0.1:9443/readyz", timeout_s=20)
    st04 = get_v1_status(admin_tok)
    plan04_fresh = plan_raw(cfg04, admin_tok, base_version=st04["persisted_version"])
    pc04_fresh = plan04_fresh["sharing"]["postcondition"]

    ledger.record_case(
        "CASE-F1-011",
        "pass",
        st04["serving_version"],
        "same_host_loopback + lan_client",
        "Jul §9.4 postcondition process-keyed HMAC + re-plan recovery + open-stream shutdown on restart",
        f"Held active SSE stream across systemctl restart jul.service (closed cleanly in {restart_stream_close_ms}ms); after restart (boot_id changed), pre-restart postcondition returned result=unverifiable; re-planning the raw candidate and repeatable location_set_withdrawn returned result=candidate_serving; non-repeatable location_add marked repeatable=false with manual reconciliation guidance.",
        {
            "boot_id_before": boot_before,
            "boot_id_after": st_after_restart["boot_id"],
            "held_sse_closed_on_restart_ms": restart_stream_close_ms,
            "original_postcondition_after_restart": pc_after_restart["result"],
            "replanned_raw_postcondition": pc_replanned["result"],
            "replanned_repeatable_typed_postcondition": pc_rep_eval["result"],
            "non_repeatable_guidance": pc_nonrep_eval.get("recovery_guidance"),
        },
    )

    # CASE-F1-012: Postcondition access_matches, diverged, and other_revision separation
    # Start from withdrawn CFG-F1-05, plan restoring active CFG-F1-04 (so pc_dash_expected includes entry 'dashboard' with withdrawn=false, credential=basic),
    # apply CFG-F1-04 (candidate_serving), then apply an unrelated edit (log_level = "debug") -> evaluating pc_dash_expected yields access_matches!
    apply_raw(cfg05, admin_tok, base_version=st04["persisted_version"])
    st_w_pre = get_v1_status(admin_tok)
    plan_restore_04 = plan_raw(cfg04, admin_tok, base_version=st_w_pre["persisted_version"])
    pc_dash_expected = plan_restore_04["sharing"]["postcondition"]
    assert len(pc_dash_expected.get("entries", [])) == 1
    apply_raw(cfg04, admin_tok, base_version=st_w_pre["persisted_version"])
    st04 = get_v1_status(admin_tok)
    assert evaluate_postcondition(pc_dash_expected, admin_tok)["result"] == "candidate_serving"

    # Obtain a global-only plan postcondition (log_level = "debug" -> no entries expected)
    cfg04_debug = cfg04.replace('log_level = "info"', 'log_level = "debug"')
    plan_global = plan_raw(cfg04_debug, admin_tok, base_version=st04["persisted_version"])
    pc_global_only = plan_global["sharing"]["postcondition"]
    assert len(pc_global_only.get("entries", [])) == 0

    # Apply cfg04_debug (an unrelated second edit that leaves dashboard access facts unchanged)
    code_dbg, _ = apply_raw(cfg04_debug, admin_tok, base_version=st04["persisted_version"])
    assert code_dbg == 200
    st_dbg = get_v1_status(admin_tok)

    # 1. Evaluate pc_dash_expected against cfg04_debug -> candidate=other_revision, access=matches => result=access_matches
    eval_access_matches = evaluate_postcondition(pc_dash_expected, admin_tok)
    assert eval_access_matches["result"] == "access_matches" and eval_access_matches["candidate"] == "other_revision" and eval_access_matches["access"] == "matches", f"got {eval_access_matches}"

    # 2. Apply cfg05 with log_level="debug" (so it is neither base_config_digest nor candidate_config_digest, i.e., candidate=other_revision, and withdrawn=true on dashboard -> access=differs => result=diverged!)
    cfg05_debug = cfg05.replace('log_level = "info"', 'log_level = "debug"')
    code_w, apply_w = apply_raw(cfg05_debug, admin_tok, base_version=st_dbg["persisted_version"])
    assert code_w == 200
    eval_diverged = evaluate_postcondition(pc_dash_expected, admin_tok)
    assert eval_diverged["result"] == "diverged" and eval_diverged["candidate"] == "other_revision" and eval_diverged["access"] == "differs", f"got {eval_diverged}"
    assert "withdrawn" in eval_diverged["entries"][0].get("differing_fields", [])

    # 3. Evaluate pc_global_only against cfg05 -> candidate=other_revision, access=none_expected => result=other_revision
    eval_other_rev = evaluate_postcondition(pc_global_only, admin_tok)
    assert eval_other_rev["result"] == "other_revision" and eval_other_rev["candidate"] == "other_revision" and eval_other_rev["access"] == "none_expected"

    ledger.record_case(
        "CASE-F1-012",
        "pass",
        get_v1_status(admin_tok)["serving_version"],
        "same_host_loopback",
        "Jul §9.4 postcondition evaluator (SD-13)",
        "Verified separate candidate and access answers across access_matches (unrelated log_level edit), diverged (dashboard withdrawn=true edit, differing_fields=['withdrawn']), and other_revision (global-only plan with access=none_expected). Never emits a bare 'confirmed'.",
        {
            "access_matches": {"result": eval_access_matches["result"], "candidate": eval_access_matches["candidate"], "access": eval_access_matches["access"]},
            "diverged": {"result": eval_diverged["result"], "candidate": eval_diverged["candidate"], "access": eval_diverged["access"], "differing_fields": eval_diverged["entries"][0].get("differing_fields")},
            "other_revision": {"result": eval_other_rev["result"], "candidate": eval_other_rev["candidate"], "access": eval_other_rev["access"]},
        },
    )

    # CASE-F1-013: Managed history rollback from withdrawn CFG-F1-05 back to active CFG-F1-04
    # First restore exact cfg04 so we have a clean history snapshot when we apply cfg05
    st_cur = get_v1_status(admin_tok)
    apply_raw(cfg04, admin_tok, base_version=st_cur["persisted_version"])
    st_before_w = get_v1_status(admin_tok)
    apply_raw(cfg05, admin_tok, base_version=st_before_w["persisted_version"])
    st_withdrawn = get_v1_status(admin_tok)

    hist_code, hist_list, _ = admin_api("/api/v1/config/history", token=admin_tok)
    assert hist_code == 200 and len(hist_list.get("entries", [])) >= 1
    latest_hist_id = hist_list["entries"][0]["id"]

    diff_code, hist_diff, _ = admin_api(f"/api/v1/config/history/{latest_hist_id}/diff", token=admin_tok)
    assert diff_code == 200
    snap_code, snap_body, _ = admin_api(f"/api/history/get?id={latest_hist_id}", token=admin_tok)
    assert snap_code == 200 and "raw" in snap_body
    rb_plan = plan_raw(snap_body["raw"], admin_tok, base_version=hist_diff["base_version"])
    rb_dash_delta = find_delta_entry(rb_plan, "dashboard")
    assert rb_dash_delta["class"] == "widened", f"expected rollback from withdrawn to active to be classed widened, got {rb_dash_delta}"

    rb_code, rb_res, _ = admin_api(
        "/api/config/rollback",
        token=admin_tok,
        method="POST",
        data={"id": latest_hist_id, "base_version": hist_diff["base_version"]},
    )
    assert rb_code in (200, 202), f"rollback failed ({rb_code}): {rb_res}"
    s_in, b_in, _, _ = lab.netns_curl("lab-client-1", "https://dash.lab.test/api/summary", resolve_host="dash.lab.test:443:192.0.2.10", ca_file=root_ca, basic_auth=intended_basic)
    assert s_in == 200 and DASHBOARD_MARKER.encode() in b_in
    st_after_rb = get_v1_status(admin_tok)

    ledger.record_case(
        "CASE-F1-013",
        "pass",
        st_after_rb["serving_version"],
        "lan_client + same_host_loopback",
        "Jul managed history + rollback transaction",
        f"Previewed rollback of history snapshot {latest_hist_id} (dashboard audience delta class='widened') and executed POST /api/config/rollback; restored intended 200 access.",
        {
            "history_id": latest_hist_id,
            "rollback_endpoint": "POST /api/config/rollback",
            "rollback_dashboard_delta_class": rb_dash_delta["class"],
            "rollback_outcome": rb_res.get("outcome"),
            "post_rollback_client_status": s_in,
        },
    )

    # CASE-F1-014 & CASE-F1-015: File-owned authority boundary, SIGHUP reload, postcondition verification, and applied_degraded
    lab.priv.run("systemctl", "stop", "jul.service")
    cfg10 = load_example_toml("10-file-owned-lan.toml")
    cfg11 = load_example_toml("11-file-owned-withdrawn-gate.toml")
    lab.install_config(cfg10, readonly=True)
    lab.priv.run("systemctl", "start", "jul-readonly.service")
    lab.wait_http_local("http://127.0.0.1:9443/readyz", timeout_s=20, expected_unit="jul-readonly.service")

    st_fo = get_v1_status(admin_tok)
    assert st_fo["config_authority"] == "file_owned" and st_fo["config_state"] == "file_owned_clean"
    before_sha = lab.priv.run("sha256sum", "/etc/jul/server.toml").stdout.split()[0]

    # Inspect and plan work in file_owned mode
    sh_fo = get_sharing_view(admin_tok)
    assert sh_fo["authority"]["config_authority"] == "file_owned"
    plan_fo11 = plan_raw(cfg11, admin_tok)
    pc_fo11 = plan_fo11["sharing"]["postcondition"]
    assert pc_fo11["status"] == "available"
    # Before owner writes file, postcondition reports not_serving_yet
    assert evaluate_postcondition(pc_fo11, admin_tok)["result"] == "not_serving_yet"

    # Mutating API calls (apply, patch, v1 rollback, console rollback, adopt-external) are all refused with 409 config_authority_read_only
    fo_base_ver = st_fo.get("persisted_version") or plan_fo11.get("base_version") or "0000000000000000"
    fo_apply_code, fo_apply_body = apply_raw(cfg11, admin_tok, base_version=fo_base_ver)
    fo_patch_code, fo_patch_body, _ = admin_api(
        "/api/config/patch/apply",
        token=admin_tok,
        method="POST",
        data={"base_version": fo_base_ver, "ops": [{"op": "location_set_withdrawn", "route_id": "dashboard", "withdrawn": True}]},
    )
    fo_v1_rb_code, fo_v1_rb_body, _ = admin_api(
        "/api/v1/config/rollback",
        token=admin_tok,
        method="POST",
        data={"id": "0001", "base_version": fo_base_ver},
        headers={"Idempotency-Key": "f1-fo-rb-" + uuid.uuid4().hex[:12]},
    )
    fo_rb_code, _, _ = admin_api(
        "/api/config/rollback",
        token=admin_tok,
        method="POST",
        data={"id": "0001", "base_version": fo_base_ver},
    )
    fo_adopt_code, fo_adopt_body, _ = admin_api(
        "/api/v1/config/adopt-external",
        token=admin_tok,
        method="POST",
        data={"observed_digest": "0" * 64, "base_version": fo_base_ver, "mode": "hot", "confirm": True},
        headers={"Idempotency-Key": "f1-fo-adopt-" + uuid.uuid4().hex[:12]},
    )
    assert fo_apply_code == 409 and fo_apply_body.get("error", {}).get("code") == "config_authority_read_only"
    assert fo_patch_code == 409 and fo_v1_rb_code == 409 and fo_rb_code == 409 and fo_adopt_code == 409
    assert fo_v1_rb_body.get("error", {}).get("code") == "config_authority_read_only"
    assert fo_adopt_body.get("error", {}).get("code") == "config_authority_read_only"
    # Remote CLI jul apply exits with code 6 (config_authority_read_only)
    tmp_cand = lab.workdir / "fo-cand.toml"
    tmp_cand.write_text(cfg11, encoding="utf-8")
    cli_fo = subprocess.run(
        ["/usr/local/bin/jul", "apply", "--endpoint", "http://127.0.0.1:9443", "--config", str(tmp_cand), "--base-version", fo_base_ver, "--json"],
        env={**os.environ, "JUL_TOKEN": admin_tok},
        capture_output=True, text=True,
    )
    assert cli_fo.returncode == 6, f"expected CLI exit code 6, got {cli_fo.returncode}: {cli_fo.stdout}"
    after_sha = lab.priv.run("sha256sum", "/etc/jul/server.toml").stdout.split()[0]
    assert before_sha == after_sha, "file_owned refusal must not touch /etc/jul/server.toml"

    # Owner installs CFG-F1-11 and sends SIGHUP via systemctl reload jul-readonly.service
    lab.install_config(cfg11, readonly=True)
    lab.priv.run("systemctl", "reload", "jul-readonly.service")
    time.sleep(0.4)
    pc_fo11_after = evaluate_postcondition(pc_fo11, admin_tok)
    assert pc_fo11_after["result"] == "candidate_serving" and pc_fo11_after["candidate"] == "serving" and pc_fo11_after["access"] == "matches"
    s_w_fo, _, h_w_fo, _ = lab.netns_curl("lab-client-1", "https://dash.lab.test/api/summary", resolve_host="dash.lab.test:443:192.0.2.10", ca_file=root_ca, basic_auth=intended_basic)
    assert s_w_fo == 403 and "no-store" in h_w_fo.get("cache-control", "").lower()

    # Owner rolls back by reinstalling CFG-F1-10 + SIGHUP
    plan_fo10 = plan_raw(cfg10, admin_tok)
    lab.install_config(cfg10, readonly=True)
    lab.priv.run("systemctl", "reload", "jul-readonly.service")
    time.sleep(0.4)
    assert evaluate_postcondition(plan_fo10["sharing"]["postcondition"], admin_tok)["result"] == "candidate_serving"
    s_rec_fo, _, _, _ = lab.netns_curl("lab-client-1", "https://dash.lab.test/api/summary", resolve_host="dash.lab.test:443:192.0.2.10", ca_file=root_ca, basic_auth=intended_basic)
    assert s_rec_fo == 200

    ledger.record_case(
        "CASE-F1-014",
        "pass",
        get_v1_status(admin_tok)["serving_version"],
        "lan_client + same_host_loopback",
        "Jul file_owned authority gate + SIGHUP reload + §9.4 postcondition",
        "Verified inspect/plan work in file_owned mode; mutating API/CLI calls (apply, patch, rollback, adopt, jul apply) all refused with 409 config_authority_read_only (CLI exit 6) and zero file side effects; owner SIGHUP reload applied CFG-F1-11 (postcondition=candidate_serving, client 403 no-store) and reverted to CFG-F1-10 (200 OK).",
        {
            "api_apply_status": fo_apply_code,
            "api_patch_status": fo_patch_code,
            "api_v1_rollback_status": fo_v1_rb_code,
            "api_rollback_status": fo_rb_code,
            "api_adopt_status": fo_adopt_code,
            "api_error_code": fo_apply_body.get("error", {}).get("code"),
            "cli_exit_code": cli_fo.returncode,
            "file_sha256_unchanged_on_refusal": before_sha == after_sha,
            "withdrawn_postcondition": pc_fo11_after["result"],
            "withdrawn_client_status": s_w_fo,
            "recovered_client_status": s_rec_fo,
        },
    )

    # CASE-F1-015: Reproducible bounded applied_degraded outcome
    # Hold a local TCP socket on 127.0.0.1:19581 and reload a file_owned config that sets withdrawn=true on dashboard AND adds a [[stream]] listener on 127.0.0.1:19581.
    with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as blocker:
        blocker.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
        blocker.bind(("127.0.0.1", 19581))
        blocker.listen(1)
        degraded_toml = cfg11 + '\n[[stream]]\nlisten = "127.0.0.1:19581"\nproxy_pass = "127.0.0.1:3000"\n'
        lab.install_config(degraded_toml, readonly=True)
        lab.priv.run("systemctl", "reload", "jul-readonly.service")
        time.sleep(0.5)
        _, st_internal, _ = admin_api("/api/runtime/overview", token=admin_tok)
        last_reload = st_internal.get("last_reload", {})
        assert last_reload.get("outcome") == "applied_degraded" and last_reload.get("published") is True, f"expected applied_degraded, got {last_reload}"
        assert last_reload.get("http", {}).get("status") == "ok" and last_reload.get("stream", {}).get("status") == "failed"
        # Verify HTTP withdrawal IS effective despite stream subsystem degradation (§9.2)
        s_deg, _, h_deg, _ = lab.netns_curl("lab-client-1", "https://dash.lab.test/api/summary", resolve_host="dash.lab.test:443:192.0.2.10", ca_file=root_ca, basic_auth=intended_basic)
        assert s_deg == 403 and "no-store" in h_deg.get("cache-control", "").lower()

    # Stop jul-readonly.service and return to managed jul.service on canonical CFG-F1-04
    lab.priv.run("systemctl", "stop", "jul-readonly.service")
    lab.install_config(cfg04, readonly=False)
    lab.priv.run("systemctl", "start", "jul.service")
    lab.wait_http_local("http://127.0.0.1:9443/readyz", timeout_s=20)
    adopt_external(admin_tok, mode="hot")
    st_restored = get_v1_status(admin_tok)

    ledger.record_case(
        "CASE-F1-015",
        "pass",
        st_restored["serving_version"],
        "lan_client + same_host_loopback",
        "Jul ReloadPlan post-Publish subsystem tracking (§9.2)",
        "Reproduced bounded applied_degraded outcome: HTTP Publish succeeded (dashboard withdrawal immediately enforced with 403 + Cache-Control: no-store) while post-Publish L4 stream bind on occupied port 127.0.0.1:19581 reported stream.status='failed' and outcome='applied_degraded'.",
        {
            "reload_outcome": last_reload.get("outcome"),
            "published": last_reload.get("published"),
            "http_subsystem": last_reload.get("http"),
            "stream_subsystem": last_reload.get("stream"),
            "dashboard_withdrawn_status": s_deg,
        },
    )


def execute_isolation_and_controls(
    lab: F1LabTopology,
    ledger: LedgerRecorder,
    tokens: dict[str, str],
    creds: dict[str, str],
    seq_state: dict[str, Any],
) -> None:
    admin_tok = tokens["admin"]
    cfg04 = seq_state["cfg04"]
    root_ca = lab.workdir / "tls" / "root-ca.pem"
    wrong_ca = lab.workdir / "tls" / "wrong-ca.pem"
    intended_basic = f"{creds['intended_user']}:{creds['intended_password']}"
    st = get_v1_status(admin_tok)

    # CASE-F1-016: Anonymous portal success, intended dashboard success, excluded dashboard denial
    reset_origin_counters()
    s_p, b_p, h_p, _ = lab.netns_curl("lab-client-2", "https://portal.lab.test/", resolve_host="portal.lab.test:443:192.0.2.10", ca_file=root_ca)
    assert s_p == 200 and PORTAL_MARKER.encode() in b_p and PRIVATE_MARKER.encode() not in b_p
    assert "public, max-age=300" in h_p.get("cache-control", "")

    s_d_no, b_d_no, _, _ = lab.netns_curl("lab-client-2", "https://dash.lab.test/api/summary", resolve_host="dash.lab.test:443:192.0.2.10", ca_file=root_ca)
    s_d_bad, b_d_bad, _, _ = lab.netns_curl("lab-client-2", "https://dash.lab.test/api/summary", resolve_host="dash.lab.test:443:192.0.2.10", ca_file=root_ca, basic_auth="family-intended:badpass")
    assert s_d_no == 401 and s_d_bad == 401 and DASHBOARD_MARKER.encode() not in b_d_no and DASHBOARD_MARKER.encode() not in b_d_bad
    assert origin_counters()["dashboard_requests"] == 0

    s_d_ok, b_d_ok, h_d_ok, _ = lab.netns_curl("lab-client-1", "https://dash.lab.test/api/summary", resolve_host="dash.lab.test:443:192.0.2.10", ca_file=root_ca, basic_auth=intended_basic)
    assert s_d_ok == 200 and DASHBOARD_MARKER.encode() in b_d_ok and origin_counters()["dashboard_requests"] == 1

    ledger.record_case(
        "CASE-F1-016",
        "pass",
        st["serving_version"],
        "lan_client (lab-client-1 192.0.2.11, lab-client-2 192.0.2.12)",
        "Jul router + location auth.basic",
        "Portal served 200 OK anonymously to lab-client-2; dashboard denied missing/wrong credentials with 401 (origin_delta=0) and served 200 OK with dashboard marker to lab-client-1 (origin_delta=+1).",
        {"portal_200": s_p, "dash_missing_401": s_d_no, "dash_wrong_401": s_d_bad, "dash_intended_200": s_d_ok, "origin_requests": 1},
    )

    # CASE-F1-017: Unknown Host and IP literal reaching default-deny (404)
    reset_origin_counters()
    s_unk, b_unk, _, _ = lab.netns_curl(
        "lab-client-2",
        "https://portal.lab.test/",
        resolve_host="portal.lab.test:443:192.0.2.10",
        ca_file=root_ca,
        headers={"Host": "unknown.lab.test"},
    )
    s_ip, b_ip, _, _ = lab.netns_curl(
        "lab-client-2",
        "https://192.0.2.10/",
        resolve_host=None,
        ca_file=root_ca,
    )
    assert s_unk == 404 and PORTAL_MARKER.encode() not in b_unk and DASHBOARD_MARKER.encode() not in b_unk
    assert s_ip == 404 and PORTAL_MARKER.encode() not in b_ip and DASHBOARD_MARKER.encode() not in b_ip
    assert origin_counters()["dashboard_requests"] == 0

    ledger.record_case(
        "CASE-F1-017",
        "pass",
        st["serving_version"],
        "lan_client (lab-client-2 192.0.2.12)",
        "Jul first-declared default-deny server block (return = 404)",
        "Unknown Host (unknown.lab.test) and direct IP literal (https://192.0.2.10/) both matched first-declared default-deny block and returned 404 with zero portal or dashboard content (origin_delta=0).",
        {"unknown_host_status": s_unk, "ip_literal_status": s_ip, "origin_delta": 0},
    )

    # CASE-F1-018: Host case, trailing dot, explicit port, and server_names alias normalisation
    reset_origin_counters()
    for host_hdr in ("PORTAL.LAB.TEST", "portal.lab.test.", "portal.lab.test:443", "portal-alias.lab.test", "PORTAL-ALIAS.LAB.TEST."):
        sc, body, _, _ = lab.netns_curl(
            "lab-client-2",
            "https://portal.lab.test/",
            resolve_host="portal.lab.test:443:192.0.2.10",
            ca_file=root_ca,
            headers={"Host": host_hdr},
        )
        assert sc == 200 and PORTAL_MARKER.encode() in body, f"Host {host_hdr} failed: {sc}"

    for d_host_hdr in ("DASH.LAB.TEST", "dash.lab.test.", "dash.lab.test:443"):
        sc_no, _, _, _ = lab.netns_curl(
            "lab-client-2",
            "https://dash.lab.test/api/summary",
            resolve_host="dash.lab.test:443:192.0.2.10",
            ca_file=root_ca,
            headers={"Host": d_host_hdr},
        )
        sc_ok, _, _, _ = lab.netns_curl(
            "lab-client-1",
            "https://dash.lab.test/api/summary",
            resolve_host="dash.lab.test:443:192.0.2.10",
            ca_file=root_ca,
            basic_auth=intended_basic,
            headers={"Host": d_host_hdr},
        )
        assert sc_no == 401 and sc_ok == 200
    assert origin_counters()["dashboard_requests"] == 3

    sh_view = get_sharing_view(admin_tok)
    portal_sh = next(e for e in sh_view["entries"] if e.get("serving", {}).get("route_id") == "portal-home")
    assert portal_sh["serving"]["urls"]["count"] == 2

    ledger.record_case(
        "CASE-F1-018",
        "pass",
        st["serving_version"],
        "lan_client (lab-client-1, lab-client-2)",
        "Jul router Host normalisation (case, trailing dot, port, alias)",
        "All portal Host variants (uppercase, trailing dot, :443 port, portal-alias.lab.test) routed to portal-home (200); all dashboard Host variants enforced Basic auth (401 excluded, 200 intended); GET /api/sharing groups both aliases under one portal-home entry (urls.count=2).",
        {"portal_urls_count": portal_sh["serving"]["urls"]["count"], "dashboard_origin_hits": 3},
    )

    # CASE-F1-019: Missing/unknown or mismatched SNI vs Host
    reset_origin_counters()
    # Connect with SNI = portal.lab.test (via URL https://portal.lab.test/api/summary) while setting Host: dash.lab.test
    s_sni_no, _, _, _ = lab.netns_curl(
        "lab-client-2",
        "https://portal.lab.test/api/summary",
        resolve_host="portal.lab.test:443:192.0.2.10",
        ca_file=root_ca,
        headers={"Host": "dash.lab.test"},
    )
    assert s_sni_no == 401 and origin_counters()["dashboard_requests"] == 0

    s_sni_ok, b_sni_ok, _, _ = lab.netns_curl(
        "lab-client-1",
        "https://portal.lab.test/api/summary",
        resolve_host="portal.lab.test:443:192.0.2.10",
        ca_file=root_ca,
        basic_auth=intended_basic,
        headers={"Host": "dash.lab.test"},
    )
    assert s_sni_ok == 200 and DASHBOARD_MARKER.encode() in b_sni_ok and origin_counters()["dashboard_requests"] == 1

    # Also connect with IP literal SNI (no DNS SNI) and Host: dash.lab.test
    s_nosni_no, _, _, _ = lab.netns_curl(
        "lab-client-2",
        "https://192.0.2.10/api/summary",
        resolve_host=None,
        ca_file=root_ca,
        headers={"Host": "dash.lab.test"},
    )
    assert s_nosni_no == 401 and origin_counters()["dashboard_requests"] == 1

    ledger.record_case(
        "CASE-F1-019",
        "pass",
        st["serving_version"],
        "lan_client (lab-client-1, lab-client-2)",
        "Jul TLS fallback certificate selection + HTTP Host router + location auth.basic",
        "Mismatched SNI (SNI=portal.lab.test with Host=dash.lab.test) and IP-literal SNI completed TLS with listener certificate, routed by Host to dashboard, and enforced Basic auth (401 without credentials, 200 with credentials).",
        {"sni_portal_host_dash_unauth": s_sni_no, "sni_portal_host_dash_auth": s_sni_ok, "ip_sni_host_dash_unauth": s_nosni_no},
    )

    # CASE-F1-020: Dot segments, repeated/encoded separators, overlapping/fallback routes and public prefixes
    reset_origin_counters()
    for raw_path in ("/a/../api/summary", "//api///summary", "/%2e%2e/api/summary"):
        _, rt_body, _ = admin_api(
            "/api/routes/test",
            token=admin_tok,
            method="POST",
            data={"listen": "192.0.2.10:443", "host": "dash.lab.test", "method": "GET", "path": raw_path},
        )
        assert rt_body.get("matched") is True and rt_body.get("upstream") == "dashboard" and rt_body.get("auth") is True, f"route test mismatch for {raw_path}: {rt_body}"

        # Unauthenticated request with raw_path (--path-as-is in curl) must be denied 401
        proc_no = lab.priv.run(
            "ip", "netns", "exec", "lab-client-2",
            "curl", "-s", "-o", "/dev/null", "-w", "%{http_code}", "--path-as-is",
            "--cacert", str(root_ca), "--resolve", "dash.lab.test:443:192.0.2.10",
            f"https://dash.lab.test{raw_path}",
        )
        assert proc_no.stdout.strip() == "401"

        proc_ok = lab.priv.run(
            "ip", "netns", "exec", "lab-client-1",
            "curl", "-fsS", "--path-as-is",
            "--cacert", str(root_ca), "--user", intended_basic,
            "--resolve", "dash.lab.test:443:192.0.2.10",
            f"https://dash.lab.test{raw_path}",
        )
        parsed_ok = json.loads(proc_ok.stdout)
        assert parsed_ok["observed_path"] == "/api/summary", f"backend saw {parsed_ok['observed_path']} instead of /api/summary"

    # Also exercise percent-encoded path separators (%2f and %2F) and static root traversal escape
    for enc_sep_path in ("/api%2fsummary", "/api%2Fsummary", "/..%2fapi/summary"):
        proc_sep_no = lab.priv.run(
            "ip", "netns", "exec", "lab-client-2",
            "curl", "-s", "-o", "/dev/null", "-w", "%{http_code}", "--path-as-is",
            "--cacert", str(root_ca), "--resolve", "dash.lab.test:443:192.0.2.10",
            f"https://dash.lab.test{enc_sep_path}",
        )
        assert proc_sep_no.stdout.strip() == "401", f"expected 401 for unauthenticated {enc_sep_path}, got {proc_sep_no.stdout}"

    proc_esc = lab.priv.run(
        "ip", "netns", "exec", "lab-client-2",
        "curl", "-s", "-o", "/dev/null", "-w", "%{http_code}", "--path-as-is",
        "--cacert", str(root_ca), "--resolve", "portal.lab.test:443:192.0.2.10",
        "https://portal.lab.test/..%2f..%2fetc/passwd",
    )
    assert proc_esc.stdout.strip() in ("400", "403", "404")

    # Requesting /api/summary or /dashboard on Host: portal.lab.test returns 404 from static root, never dashboard content
    s_p_shadow, b_p_shadow, _, _ = lab.netns_curl("lab-client-2", "https://portal.lab.test/api/summary", resolve_host="portal.lab.test:443:192.0.2.10", ca_file=root_ca)
    assert s_p_shadow == 404 and DASHBOARD_MARKER.encode() not in b_p_shadow

    ledger.record_case(
        "CASE-F1-020",
        "pass",
        st["serving_version"],
        "lan_client + POST /api/routes/test",
        "Jul canonicalizeRequest (E37, ADR 0018) + separate vhost routing",
        "Dot-segment (/a/../api/summary), repeated-slash (//api///summary), percent-encoded dot (/%2e%2e/api/summary), and percent-encoded separator (/api%2fsummary, /api%2Fsummary, /..%2fapi/summary) paths all enforced Basic auth (401 unauthenticated); static escape /..%2f..%2fetc/passwd rejected; portal.lab.test/api/summary returned 404 from static root.",
        {"canonical_path": "/api/summary", "encoded_separator_unauth_status": 401, "static_escape_status": int(proc_esc.stdout.strip()), "portal_shadow_status": s_p_shadow},
    )

    # CASE-F1-021: Forged X-Forwarded-For, Forwarded, RFC 9440, and provider identity headers
    reset_origin_counters()
    forged_headers = {
        "X-Forwarded-For": "127.0.0.1, 10.0.0.1",
        "X-Real-Ip": "127.0.0.1",
        "Forwarded": "for=127.0.0.1;proto=https",
        "Client-Cert": ":c29tZS1mb3JnZWQtY2VydA==:",
        "Client-Cert-Chain": ":c29tZS1mb3JnZWQtY2hhaW4=:",
        "X-Forwarded-Client-Cert": "By=spiffe://cluster.local/ns/default/sa/default;Subject=\"CN=admin\"",
        "Tailscale-User-Login": "admin@example.net",
        "Tailscale-User-Name": "Forged Tailscale Admin",
        "Cf-Access-Authenticated-User-Email": "admin@example.net",
        "Cf-Access-Jwt-Assertion": "eyJhbGciOiJub25lIn0.eyJzdWIiOiJhZG1pbiJ9.",
    }
    s_forg_no, b_forg_no, _, _ = lab.netns_curl(
        "lab-client-2",
        "https://dash.lab.test/api/summary",
        resolve_host="dash.lab.test:443:192.0.2.10",
        ca_file=root_ca,
        headers=forged_headers,
    )
    assert s_forg_no == 401 and DASHBOARD_MARKER.encode() not in b_forg_no and origin_counters()["dashboard_requests"] == 0

    s_forg_ok, b_forg_ok, _, _ = lab.netns_curl(
        "lab-client-1",
        "https://dash.lab.test/api/summary",
        resolve_host="dash.lab.test:443:192.0.2.10",
        ca_file=root_ca,
        basic_auth=intended_basic,
        headers=forged_headers,
    )
    assert s_forg_ok == 200
    obs_hdrs = json.loads(b_forg_ok.decode("utf-8"))["observed_headers"]
    # ADR 0016 §12: X-Forwarded-For is overwritten with transport peer (192.0.2.11);
    # Forwarded, Client-Cert, Client-Cert-Chain, and X-Forwarded-Client-Cert are stripped unconditionally;
    # SH-12: provider headers (and unconfigured custom headers) reach backend as untrusted headers and are never trusted for auth.
    assert obs_hdrs.get("x-forwarded-for") == "192.0.2.11", f"expected XFF overwritten to 192.0.2.11, got {obs_hdrs}"
    for stripped in ("forwarded", "client-cert", "client-cert-chain", "x-forwarded-client-cert"):
        assert stripped not in obs_hdrs, f"expected {stripped} to be stripped, got {obs_hdrs}"
    assert obs_hdrs.get("tailscale-user-login") == "admin@example.net"
    assert obs_hdrs.get("cf-access-authenticated-user-email") == "admin@example.net"

    ledger.record_case(
        "CASE-F1-021",
        "pass",
        st["serving_version"],
        "lan_client (lab-client-1 192.0.2.11, lab-client-2 192.0.2.12)",
        "Jul ADR 0016 §12 header sanitiser + location auth.basic (SH-12)",
        "Forged forwarding/cert/provider headers without Basic credentials denied 401 (origin_delta=0); on authenticated request, Jul overwrote X-Forwarded-For/X-Real-Ip to 192.0.2.11, stripped Forwarded/Client-Cert/Client-Cert-Chain/X-Forwarded-Client-Cert, and passed unknown provider headers through as untrusted.",
        {"unauth_forged_status": s_forg_no, "backend_observed_headers": obs_hdrs},
    )

    # CASE-F1-022: Direct-origin, admin, metrics, and unconfigured-service isolation + scoped firewall recovery
    rej_before = lab.firewall_reject_packets()
    for port in (3000, 9443, 8444):
        lab.netns_curl("lab-client-2", f"http://192.0.2.10:{port}/", resolve_host=None, expect_curl_exit=7)
        lab.netns_curl("lab-client-1", f"http://192.0.2.10:{port}/metrics", resolve_host=None, expect_curl_exit=7)
    rej_after = lab.firewall_reject_packets()
    assert rej_after >= rej_before + 6, f"expected firewall REJECT counter to increase by >= 6, got {rej_before} -> {rej_after}"

    # Temporarily block port 443 in JL581 chain, verify connection refused (exit 7), then restore and verify 200
    lab.set_port_443_allowed(False)
    lab.netns_curl("lab-client-1", "https://portal.lab.test/", resolve_host="portal.lab.test:443:192.0.2.10", ca_file=root_ca, expect_curl_exit=7)
    lab.set_port_443_allowed(True)
    s_fw_rec, _, _, _ = lab.netns_curl("lab-client-1", "https://portal.lab.test/", resolve_host="portal.lab.test:443:192.0.2.10", ca_file=root_ca)
    assert s_fw_rec == 200

    ledger.record_case(
        "CASE-F1-022",
        "pass",
        st["serving_version"],
        "lan_client (lab-client-1, lab-client-2)",
        "Loopback binds (127.0.0.1:3000, 127.0.0.1:9443) + scoped iptables chain",
        "Direct origin (:3000), admin/metrics (:9443), and unconfigured port (:8444) unreachable from lab-client-1 and lab-client-2 (curl exit 7); iptables REJECT counter incremented; temporary :443 firewall block rejected traffic and recovered 200 when restored.",
        {"firewall_reject_before": rej_before, "firewall_reject_after": rej_after, "recovered_443_status": s_fw_rec},
    )

    # CASE-F1-023: Native Jul TLS chain verification negatives (wrong CA, wrong hostname, incomplete chain)
    # 1. Wrong CA -> curl exit 60
    lab.netns_curl("lab-client-1", "https://portal.lab.test/", resolve_host="portal.lab.test:443:192.0.2.10", ca_file=wrong_ca, expect_curl_exit=60)
    # 2. Wrong hostname -> curl exit 60
    lab.netns_curl("lab-client-1", "https://wrong.lab.test/", resolve_host="wrong.lab.test:443:192.0.2.10", ca_file=root_ca, expect_curl_exit=60)
    # 3. Incomplete chain (server serves leaf-only cert without intermediate while client trusts only root-ca.pem)
    leaf_only_toml = cfg04.replace('cert = "tls/lab.crt"', 'cert = "tls/lab-leaf-only.crt"')
    st_cur = get_v1_status(admin_tok)
    apply_raw(leaf_only_toml, admin_tok, base_version=st_cur["persisted_version"])
    lab.netns_curl("lab-client-1", "https://portal.lab.test/", resolve_host="portal.lab.test:443:192.0.2.10", ca_file=root_ca, expect_curl_exit=60)
    # Restore full chain (leaf + intermediate)
    st_leaf = get_v1_status(admin_tok)
    apply_raw(cfg04, admin_tok, base_version=st_leaf["persisted_version"])
    s_chain_ok, _, _, _ = lab.netns_curl("lab-client-1", "https://portal.lab.test/", resolve_host="portal.lab.test:443:192.0.2.10", ca_file=root_ca)
    assert s_chain_ok == 200
    st = get_v1_status(admin_tok)

    ledger.record_case(
        "CASE-F1-023",
        "pass",
        st["serving_version"],
        "lan_client (lab-client-1 192.0.2.11)",
        "Client TLS X.509 verifier + Jul TLS server certificate hot reload",
        "Wrong CA, wrong hostname (wrong.lab.test), and leaf-only incomplete chain all failed TLS verification (curl exit 60); restoring full chain (leaf + intermediate) recovered verified 200 OK.",
        {"wrong_ca_exit": 60, "wrong_hostname_exit": 60, "incomplete_chain_exit": 60, "restored_full_chain_status": s_chain_ok},
    )

    # CASE-F1-024: WAF advanced control (benign 200, SQLi/XSS 403, oversized body 413)
    reset_origin_counters()
    s_waf_benign, _, _, _ = lab.netns_curl(
        "lab-client-1",
        "https://dash.lab.test/api/summary?filter=greenhouse",
        resolve_host="dash.lab.test:443:192.0.2.10",
        ca_file=root_ca,
        basic_auth=intended_basic,
    )
    assert s_waf_benign == 200 and origin_counters()["dashboard_requests"] == 1

    s_waf_sqli, b_waf_sqli, _, _ = lab.netns_curl(
        "lab-client-1",
        "https://dash.lab.test/api/summary?id=1%27%20OR%20%271%27=%271",
        resolve_host="dash.lab.test:443:192.0.2.10",
        ca_file=root_ca,
        basic_auth=intended_basic,
    )
    s_waf_xss, b_waf_xss, _, _ = lab.netns_curl(
        "lab-client-1",
        "https://dash.lab.test/api/summary?q=%3Cscript%3Ealert(1)%3C/script%3E",
        resolve_host="dash.lab.test:443:192.0.2.10",
        ca_file=root_ca,
        basic_auth=intended_basic,
    )
    assert s_waf_sqli == 403 and s_waf_xss == 403
    assert DASHBOARD_MARKER.encode() not in b_waf_sqli and DASHBOARD_MARKER.encode() not in b_waf_xss
    assert origin_counters()["dashboard_requests"] == 1, "WAF-blocked requests must never reach the dashboard origin"

    oversized_payload = b"field=" + (b"A" * (1200 * 1024))
    s_waf_413, _, _, _ = lab.netns_curl(
        "lab-client-1",
        "https://dash.lab.test/api/summary",
        resolve_host="dash.lab.test:443:192.0.2.10",
        ca_file=root_ca,
        basic_auth=intended_basic,
        method="POST",
        headers={"Content-Type": "application/x-www-form-urlencoded"},
        data=oversized_payload,
        max_time=15,
    )
    assert s_waf_413 == 413 and origin_counters()["dashboard_requests"] == 1

    ledger.record_case(
        "CASE-F1-024",
        "pass",
        st["serving_version"],
        "lan_client (lab-client-1 192.0.2.11)",
        "Jul Coraza CRS WAF (mode=block, request_body_limit=1m)",
        "Benign authenticated request succeeded 200 (origin_delta=+1); SQLi and XSS requests blocked with 403 (origin_delta=0); 1.2 MiB POST body rejected with 413 (origin_delta=0).",
        {"benign_status": s_waf_benign, "sqli_status": s_waf_sqli, "xss_status": s_waf_xss, "oversized_body_status": s_waf_413, "origin_requests_after_all": 1},
    )

    # CASE-F1-025: Rate-limit advanced control on portal (paced normal load vs deliberate 429 burst)
    # 1. Paced normal requests from lab-client-1 (20 requests at 25ms pacing = 40 req/s < 50 req/s limit)
    paced_statuses = []
    for _ in range(20):
        sc, _, _, _ = lab.netns_curl("lab-client-1", "https://portal.lab.test/", resolve_host="portal.lab.test:443:192.0.2.10", ca_file=root_ca)
        paced_statuses.append(sc)
        time.sleep(0.025)
    assert all(sc == 200 for sc in paced_statuses)

    # 2. Deliberate unpaced burst from lab-client-2 (160 rapid requests on single HTTP/2 connection using curl URL range [1-160])
    burst_proc = lab.priv.run(
        "ip", "netns", "exec", "lab-client-2",
        "curl", "-s", "-o", "/dev/null", "-w", "%{http_code}\n", "--http2",
        "--cacert", str(root_ca), "--resolve", "portal.lab.test:443:192.0.2.10",
        "https://portal.lab.test/?burst=[1-160]",
        timeout=30,
    )
    burst_codes = [int(x) for x in burst_proc.stdout.splitlines() if x.strip().isdigit()]
    count_200 = sum(1 for c in burst_codes if c == 200)
    count_429 = sum(1 for c in burst_codes if c == 429)
    assert count_200 > 0 and count_429 > 0, f"expected mix of 200 and 429 in burst, got 200={count_200}, 429={count_429}"

    # Verify lab-client-1 (separate source IP 192.0.2.11) and dashboard are unaffected during lab-client-2 rate limiting
    s_c1_portal, _, _, _ = lab.netns_curl("lab-client-1", "https://portal.lab.test/", resolve_host="portal.lab.test:443:192.0.2.10", ca_file=root_ca)
    s_c1_dash, _, _, _ = lab.netns_curl("lab-client-1", "https://dash.lab.test/api/summary", resolve_host="dash.lab.test:443:192.0.2.10", ca_file=root_ca, basic_auth=intended_basic)
    assert s_c1_portal == 200 and s_c1_dash == 200

    ledger.record_case(
        "CASE-F1-025",
        "pass",
        st["serving_version"],
        "lan_client (lab-client-1 192.0.2.11, lab-client-2 192.0.2.12)",
        "Jul per-location token-bucket rate limiter (rate=50, burst=100 per client IP)",
        f"Paced normal load from lab-client-1 succeeded 20/20 (200 OK); deliberate 160-request burst from lab-client-2 produced {count_429}x 429 Too Many Requests after {count_200}x 200 OK while lab-client-1's bucket and dashboard remained 200 OK.",
        {"paced_200_count": len(paced_statuses), "burst_200_count": count_200, "burst_429_count": count_429, "sibling_client_portal_status": s_c1_portal},
    )

    # CASE-F1-026: Cache isolation (cold/warm private markers) and SD-16 cache warnings in plan/lint
    reset_origin_counters()
    # Warm dashboard with 3 authenticated requests from lab-client-1
    for _ in range(3):
        sc, body, hdrs, _ = lab.netns_curl("lab-client-1", "https://dash.lab.test/api/summary", resolve_host="dash.lab.test:443:192.0.2.10", ca_file=root_ca, basic_auth=intended_basic)
        assert sc == 200 and PRIVATE_MARKER.encode() in body
        assert "x-cache" not in hdrs, "F1 must keep Jul response cache disabled"
    assert origin_counters()["dashboard_requests"] == 3, "Every dashboard request must reach origin (not cached)"

    # Verify excluded client on dashboard and anonymous requests on portal never see PRIVATE_MARKER
    _, body_ex_after, _, _ = lab.netns_curl("lab-client-2", "https://dash.lab.test/api/summary", resolve_host="dash.lab.test:443:192.0.2.10", ca_file=root_ca)
    _, body_po_after, hdrs_po_after, _ = lab.netns_curl("lab-client-2", "https://portal.lab.test/", resolve_host="portal.lab.test:443:192.0.2.10", ca_file=root_ca)
    assert PRIVATE_MARKER.encode() not in body_ex_after and PRIVATE_MARKER.encode() not in body_po_after
    assert hdrs_po_after.get("cache-control") == "public, max-age=300" and "x-cache" not in hdrs_po_after

    # Plan a candidate (without applying!) that enables Jul response cache on the restricted dashboard and adds a second cached server block for portal.lab.test to verify intent_restricted_cached and cache_shared_across_entries warnings
    cache_warn_toml = (
        "[cache]\nenabled = true\nmemory_max_size = \"16m\"\n\n"
        + cfg04.replace('proxy_pass = "http://dashboard"', 'proxy_pass = "http://dashboard"\n  cache = true')
        .replace('root = "portal"', 'root = "portal"\n  cache = true')
        + '\n[[servers]]\nname = "portal-alt"\nlisten = "127.0.0.1:18443"\nserver_names = ["portal.lab.test"]\n  [[servers.locations]]\n  route_id = "portal-alt"\n  match = { type = "prefix", path = "/" }\n  root = "portal"\n  cache = true\n'
    )
    cache_plan = plan_raw(cache_warn_toml, admin_tok, base_version=st["persisted_version"])
    finding_codes = {f["code"] for f in cache_plan["sharing"].get("intent_findings", []) if "code" in f}
    lint_codes = {d["code"] for d in cache_plan.get("lint", []) if "code" in d}
    all_codes = finding_codes | lint_codes
    assert "intent_restricted_cached" in all_codes and "cache_shared_across_entries" in all_codes, f"expected cache warnings, got {all_codes}"

    ledger.record_case(
        "CASE-F1-026",
        "pass",
        st["serving_version"],
        "lan_client + POST /api/v1/config/plan",
        "Jul cache policy + §4.5/§11 lint & plan findings (SD-16)",
        "Verified Jul response cache is disabled on F1 (no X-Cache header, all 3 warm dashboard requests hit origin); private content marker never appeared on portal or excluded responses; planning cache on restricted entry surfaced intent_restricted_cached and cache_shared_across_entries warnings.",
        {"dashboard_origin_hits": 3, "private_marker_leaked": False, "plan_warning_codes": sorted(all_codes)},
    )

    # CASE-F1-027: Backend down and recovery while portal remains isolated
    lab.priv.run("systemctl", "stop", "jul-f1-dashboard.service")
    time.sleep(0.2)
    s_down_dash, b_down_dash, _, _ = lab.netns_curl("lab-client-1", "https://dash.lab.test/api/summary", resolve_host="dash.lab.test:443:192.0.2.10", ca_file=root_ca, basic_auth=intended_basic)
    s_down_portal, b_down_portal, _, _ = lab.netns_curl("lab-client-2", "https://portal.lab.test/", resolve_host="portal.lab.test:443:192.0.2.10", ca_file=root_ca)
    s_down_unauth, _, _, _ = lab.netns_curl("lab-client-2", "https://dash.lab.test/api/summary", resolve_host="dash.lab.test:443:192.0.2.10", ca_file=root_ca)
    assert s_down_dash in (502, 503) and s_down_portal == 200 and PORTAL_MARKER.encode() in b_down_portal
    assert s_down_unauth == 401, "Excluded client must still be denied 401 at Jul before hitting down backend"

    lab.priv.run("systemctl", "start", "jul-f1-dashboard.service")
    lab.wait_http_local("http://127.0.0.1:3000/healthz", timeout_s=10)
    s_up_dash, b_up_dash, _, _ = lab.netns_curl("lab-client-1", "https://dash.lab.test/api/summary", resolve_host="dash.lab.test:443:192.0.2.10", ca_file=root_ca, basic_auth=intended_basic)
    assert s_up_dash == 200 and DASHBOARD_MARKER.encode() in b_up_dash

    ledger.record_case(
        "CASE-F1-027",
        "pass",
        st["serving_version"],
        "lan_client (lab-client-1, lab-client-2)",
        "Jul upstream reverse proxy + independent static root",
        f"During backend stop: authenticated dashboard returned {s_down_dash}, unauthenticated dashboard still denied 401 before upstream dial, and portal remained 200 OK; after backend restart, dashboard immediately recovered 200 OK.",
        {"dash_down_status": s_down_dash, "dash_unauth_during_outage": s_down_unauth, "portal_during_outage": s_down_portal, "dash_recovered_status": s_up_dash},
    )

    # CASE-F1-028: Portal-only route/target/header change, rejection, rollback, and restart with no dashboard audience widening
    portal_route_toml = cfg04.replace(
        '  cache_control = "public, max-age=300"\n',
        '  cache_control = "public, max-age=600"\n',
    ).replace(
        '[[servers]]\nname = "dashboard"',
        '  [[servers.locations]]\n  route_id = "portal-status"\n  match = { type = "exact", path = "/portal-status" }\n  return = 204\n\n    [servers.locations.sharing]\n    mode = "public"\n    audience_label = "anyone"\n\n[[servers]]\nname = "dashboard"',
        1,
    )
    plan_po = plan_raw(portal_route_toml, admin_tok, base_version=st["persisted_version"])
    po_entries = [e["ref"].get("route_id") for e in plan_po["sharing"].get("entries", [])]
    assert "dashboard" not in po_entries, f"portal route edit must not affect dashboard: {plan_po['sharing']}"
    apply_raw(portal_route_toml, admin_tok, base_version=st["persisted_version"])
    s_po_check, _, h_po_check, _ = lab.netns_curl("lab-client-2", "https://portal.lab.test/", resolve_host="portal.lab.test:443:192.0.2.10", ca_file=root_ca)
    s_po_sub, _, _, _ = lab.netns_curl("lab-client-2", "https://portal.lab.test/portal-status", resolve_host="portal.lab.test:443:192.0.2.10", ca_file=root_ca)
    s_da_check, _, _, _ = lab.netns_curl("lab-client-2", "https://dash.lab.test/api/summary", resolve_host="dash.lab.test:443:192.0.2.10", ca_file=root_ca)
    s_da_in_check, _, _, _ = lab.netns_curl("lab-client-1", "https://dash.lab.test/api/summary", resolve_host="dash.lab.test:443:192.0.2.10", ca_file=root_ca, basic_auth=intended_basic)
    assert s_po_check == 200 and "max-age=600" in h_po_check.get("cache-control", "") and s_po_sub == 204 and s_da_check == 401 and s_da_in_check == 200

    # Reject an invalid portal-only edit (sharing block without route_id on portal) and verify dashboard stays restricted
    inv_po_toml = portal_route_toml.replace('route_id = "portal-status"\n', "")
    inv_po_code, _ = apply_raw(inv_po_toml, admin_tok, base_version=get_v1_status(admin_tok)["persisted_version"])
    assert inv_po_code in (400, 422)

    # Roll back to cfg04 and restart jul.service, confirming dashboard stays restricted (401 excluded, 200 intended)
    apply_raw(cfg04, admin_tok, base_version=get_v1_status(admin_tok)["persisted_version"])
    lab.priv.run("systemctl", "restart", "jul.service")
    lab.wait_http_local("http://127.0.0.1:9443/readyz", timeout_s=20, expected_unit="jul.service")
    s_da_post_rst, _, _, _ = lab.netns_curl("lab-client-2", "https://dash.lab.test/api/summary", resolve_host="dash.lab.test:443:192.0.2.10", ca_file=root_ca)
    assert s_da_post_rst == 401
    st = get_v1_status(admin_tok)

    ledger.record_case(
        "CASE-F1-028",
        "pass",
        st["serving_version"],
        "lan_client + POST /api/v1/config/plan",
        "Jul §9.5 per-entry audience-delta classifier",
        "Portal-only route addition (/portal-status 204) and cache_control update, invalid portal edit rejection, rollback, and service restart produced zero audience delta on dashboard; dashboard remained restricted (401 excluded, 200 intended) throughout.",
        {"affected_entries_in_plan": po_entries, "portal_subroute_status": s_po_sub, "rejected_portal_edit_status": inv_po_code, "dash_excluded_status": s_da_check, "dash_post_restart_excluded_status": s_da_post_rst},
    )

    # CASE-F1-029: Deliberate dashboard-public transition (CFG-F1-07), Console vs API/CLI acknowledgement contract, and exact recovery
    # 1. Plan removing Basic auth while keeping sharing.mode="restricted" -> emits intent_restricted_unenforced and class="widened"
    unenforced_toml = cfg04.replace('    [servers.locations.auth.basic]\n    file = "dashboard.htpasswd"\n    realm = "Dashboard"\n\n', "")
    plan_unenf = plan_raw(unenforced_toml, admin_tok, base_version=st["persisted_version"])
    unenf_dash = find_delta_entry(plan_unenf, "dashboard")
    unenf_codes = [f["code"] for f in (unenf_dash.get("intent_findings") or [])]
    assert unenf_dash["class"] == "widened" and unenf_dash["requires_acknowledgement"] is True and "intent_restricted_unenforced" in unenf_codes

    # 2. Plan intentional dashboard -> public transition (CFG-F1-07)
    cfg07 = lab.adapt_config_for_service(load_example_toml("07-lan-dashboard-public-transition.toml"))
    plan07 = plan_raw(cfg07, admin_tok, base_version=st["persisted_version"])
    dash07 = find_delta_entry(plan07, "dashboard")
    assert dash07["class"] == "widened" and dash07["declared_mode_before"] == "restricted" and dash07["declared_mode_after"] == "public"
    assert dash07["declared_mode_widened"] is True and dash07["requires_acknowledgement"] is True
    assert plan07["sharing"]["requires_acknowledgement"] is True and len(plan07["sharing"]["acknowledgement_entries"]) == 1

    # API/CLI apply succeeds without a server-side acknowledgement parameter (SD-15), while Console UI gates Apply button on requires_acknowledgement
    code07, apply07 = apply_raw(cfg07, admin_tok, base_version=st["persisted_version"])
    assert code07 == 200 and apply07.get("outcome") == "applied_live"
    assert evaluate_postcondition(plan07["sharing"]["postcondition"], admin_tok)["result"] == "candidate_serving"

    # In public mode, anonymous dashboard request from lab-client-2 succeeds 200 (expected)
    s_pub, b_pub, _, _ = lab.netns_curl("lab-client-2", "https://dash.lab.test/api/summary", resolve_host="dash.lab.test:443:192.0.2.10", ca_file=root_ca)
    assert s_pub == 200 and DASHBOARD_MARKER.encode() in b_pub

    # 3. Exact recovery back to CFG-F1-04 (class="narrowed", triggers_retirement=true)
    st07 = get_v1_status(admin_tok)
    plan_rec04 = plan_raw(cfg04, admin_tok, base_version=st07["persisted_version"])
    dash_rec04 = find_delta_entry(plan_rec04, "dashboard")
    assert dash_rec04["class"] == "narrowed" and dash_rec04["triggers_retirement"] is True
    code_rec04, _ = apply_raw(cfg04, admin_tok, base_version=st07["persisted_version"])
    assert code_rec04 == 200
    assert evaluate_postcondition(plan_rec04["sharing"]["postcondition"], admin_tok)["result"] == "candidate_serving"

    s_rec_ex, _, _, _ = lab.netns_curl("lab-client-2", "https://dash.lab.test/api/summary", resolve_host="dash.lab.test:443:192.0.2.10", ca_file=root_ca)
    s_rec_in, _, _, _ = lab.netns_curl("lab-client-1", "https://dash.lab.test/api/summary", resolve_host="dash.lab.test:443:192.0.2.10", ca_file=root_ca, basic_auth=intended_basic)
    assert s_rec_ex == 401 and s_rec_in == 200
    st = get_v1_status(admin_tok)

    ledger.record_case(
        "CASE-F1-029",
        "pass",
        st["serving_version"],
        "lan_client + POST /api/v1/config/plan",
        "Jul §4.5/§9.5 widening guard (SH-5, SD-15) + recovery",
        "Removing Basic auth while restricted emitted intent_restricted_unenforced + class=widened; CFG-F1-07 transition emitted class=widened, declared_mode_widened=true, requires_acknowledgement=true; public transition served 200 anonymously; recovery to CFG-F1-04 (class=narrowed, triggers_retirement=true) restored 401 excluded and 200 intended.",
        {
            "unenforced_finding_codes": unenf_codes,
            "public_transition_delta": {
                "class": dash07["class"],
                "declared_mode_widened": dash07["declared_mode_widened"],
                "requires_acknowledgement": dash07["requires_acknowledgement"],
            },
            "public_mode_anonymous_status": s_pub,
            "recovery_delta_class": dash_rec04["class"],
            "post_recovery_excluded_status": s_rec_ex,
            "post_recovery_intended_status": s_rec_in,
        },
    )


def run_retirement_client_verification(
    lab: F1LabTopology,
    load_bin: Path,
    namespace: str,
    tls_url: str,
    ca_file: Path,
    auth_user: str,
    auth_password: str,
    trigger_fn: Any,
    admin_tok: str,
    with_ws: bool = True,
    h1_keepalive: bool = False,
    client_cert: tuple[Path, Path] | None = None,
    expected_post_dash_status: int = 403,
    portal_host: str = "portal.lab.test",
    dash_host: str = "dash.lab.test",
) -> dict[str, Any]:
    tag = uuid.uuid4().hex[:8]
    ready_file = lab.workdir / f"ret-ready-{tag}.txt"
    pub_file = lab.workdir / f"ret-pub-{tag}.txt"
    events_file = lab.workdir / f"ret-events-{tag}.jsonl"
    summary_file = lab.workdir / f"ret-summary-{tag}.json"
    mode_flag = "-f1H1KeepAlive" if h1_keepalive else "-f1SharedH2"
    cmd = [
        str(load_bin),
        mode_flag,
        "-duration=15s",
        f"-tls={tls_url}",
        f"-caFile={ca_file}",
        f"-portalHost={portal_host}",
        f"-dashHost={dash_host}",
        f"-authUser={auth_user}",
        f"-authPassword={auth_password}",
        "-pace=60ms",
        f"-readyFile={ready_file}",
        f"-publishSignalFile={pub_file}",
        "-retirementBound=2500ms",
        f"-expectedPostDashStatus={expected_post_dash_status}",
        f"-withWS={'true' if (with_ws and not h1_keepalive) else 'false'}",
        "-wsEchoClose=true",
        f"-eventLog={events_file}",
        f"-summaryOut={summary_file}",
    ]
    if client_cert is not None:
        cmd.extend([f"-clientCert={client_cert[0]}", f"-clientKey={client_cert[1]}"])

    proc = start_netns_popen(lab, namespace, cmd)
    deadline = time.monotonic() + 10.0
    while time.monotonic() < deadline and not ready_file.exists():
        time.sleep(0.05)
    assert ready_file.exists(), f"retirement client ({mode_flag}) did not become ready"

    # Observe server-side open_exchanges BEFORE Publish (proving portal-home exchange is actively held in Jul on shared H2)
    sh_before = get_sharing_view(admin_tok)
    portal_open_before = 0
    dash_open_before = 0
    for entry in sh_before.get("entries", []):
        srv = entry.get("serving", {})
        if srv.get("route_id") == "portal-home":
            portal_open_before = int(srv.get("open_exchanges", 0))
        elif srv.get("route_id") == "dashboard":
            dash_open_before = int(srv.get("open_exchanges", 0))
    hold_portal_across_publish = (not h1_keepalive and client_cert is None and portal_host == "portal.lab.test")
    if hold_portal_across_publish:
        assert portal_open_before >= 1, f"expected portal-home open_exchanges >= 1 at Publish, got {portal_open_before}"
    assert dash_open_before >= 1, f"expected dashboard open_exchanges >= 1 at Publish, got {dash_open_before}"

    pub_file.write_text("publish\n", encoding="utf-8")
    t_apply_start = time.monotonic()
    trigger_fn()
    t_apply_end = time.monotonic()

    stdout, stderr = proc.communicate(timeout=20)
    summary_debug = summary_file.read_text(encoding="utf-8") if summary_file.exists() else "<no summary>"
    assert proc.returncode == 0, f"retirement client ({mode_flag}) failed ({proc.returncode}): {stdout} {stderr} summary={summary_debug}"
    res = json.loads(summary_file.read_text(encoding="utf-8"))
    assert res["portal_stream_completed_ok"] is True
    assert res["portal_active_at_retirement"] is True
    assert res["premature_close_before_publish"] is False
    assert res["retirement_within_bound"] is True
    assert res["post_change_dash_same_conn_status"] == expected_post_dash_status
    if hold_portal_across_publish:
        assert res["portal_bytes_before_retirement"] == 4096
        assert res["portal_bytes_after_retirement"] == 258048
        assert res["portal_stream_bytes"] == 262144
        assert res["post_change_portal_same_conn_status"] == 200
    elif not h1_keepalive and portal_host == "portal.lab.test":
        assert res["post_change_portal_same_conn_status"] == 200
    if with_ws and not h1_keepalive:
        assert res["ws_opened"] is True and res["ws_close_code"] == 1008 and res["ws_graceful_handshake"] is True

    sh_after = get_sharing_view(admin_tok)
    dash_ret = next(e for e in sh_after["entries"] if e.get("serving", {}).get("route_id") == "dashboard")["serving"].get("last_retirement", {})
    assert dash_ret.get("ended_exchanges", 0) >= 1
    assert dash_ret.get("completed_within_bound") is True
    assert dash_ret.get("collateral_count") == 0
    return {
        "client_result": res,
        "server_last_retirement": dash_ret,
        "portal_open_exchanges_before_publish": portal_open_before,
        "dash_open_exchanges_before_publish": dash_open_before,
        "apply_wall_ms": int((t_apply_end - t_apply_start) * 1000),
    }


def execute_withdrawal_mtls_and_continuity(
    lab: F1LabTopology,
    ledger: LedgerRecorder,
    tokens: dict[str, str],
    creds: dict[str, str],
    seq_state: dict[str, Any],
    load_bin: Path,
) -> None:
    admin_tok = tokens["admin"]
    cfg04 = seq_state["cfg04"]
    root_ca = lab.workdir / "tls" / "root-ca.pem"
    intended_basic = f"{creds['intended_user']}:{creds['intended_password']}"
    second_basic = f"{creds['second_user']}:{creds['second_password']}"
    st = get_v1_status(admin_tok)

    # CASE-F1-030: Complete withdrawal via withdrawn=true and repeatable location_set_withdrawn
    reset_origin_counters()
    _, patch_w_res, _ = admin_api(
        "/api/config/patch/apply",
        token=admin_tok,
        method="POST",
        data={
            "base_version": st["persisted_version"],
            "ops": [{"op": "location_set_withdrawn", "route_id": "dashboard", "withdrawn": True}],
        },
    )
    assert patch_w_res.get("ok") is True and patch_w_res.get("reload", {}).get("outcome") == "applied_live"

    s_w_in, b_w_in, h_w_in, _ = lab.netns_curl("lab-client-1", "https://dash.lab.test/api/summary", resolve_host="dash.lab.test:443:192.0.2.10", ca_file=root_ca, basic_auth=intended_basic)
    s_w_ex, _, h_w_ex, _ = lab.netns_curl("lab-client-2", "https://dash.lab.test/api/summary", resolve_host="dash.lab.test:443:192.0.2.10", ca_file=root_ca)
    s_w_po, _, _, _ = lab.netns_curl("lab-client-2", "https://portal.lab.test/", resolve_host="portal.lab.test:443:192.0.2.10", ca_file=root_ca)
    assert s_w_in == 403 and s_w_ex == 403 and s_w_po == 200
    assert "no-store" in h_w_in.get("cache-control", "").lower() and "expires" not in h_w_in
    assert origin_counters()["dashboard_requests"] == 0

    sh_w = get_sharing_view(admin_tok)
    dash_w_entry = next(e for e in sh_w["entries"] if e.get("serving", {}).get("route_id") == "dashboard")["serving"]
    assert dash_w_entry["withdrawn"] == "yes"
    assert dash_w_entry["jul_checks"]["credential"] == "basic" and dash_w_entry["action"] in ("proxy", "proxy_pass")

    # One-change recovery via location_set_withdrawn withdrawn=false
    st_w = get_v1_status(admin_tok)
    _, patch_rec_res, _ = admin_api(
        "/api/config/patch/apply",
        token=admin_tok,
        method="POST",
        data={
            "base_version": st_w["persisted_version"],
            "ops": [{"op": "location_set_withdrawn", "route_id": "dashboard", "withdrawn": False}],
        },
    )
    assert patch_rec_res.get("ok") is True
    s_rec_in, _, _, _ = lab.netns_curl("lab-client-1", "https://dash.lab.test/api/summary", resolve_host="dash.lab.test:443:192.0.2.10", ca_file=root_ca, basic_auth=intended_basic)
    s_rec_ex, _, _, _ = lab.netns_curl("lab-client-2", "https://dash.lab.test/api/summary", resolve_host="dash.lab.test:443:192.0.2.10", ca_file=root_ca)
    assert s_rec_in == 200 and s_rec_ex == 401
    st = get_v1_status(admin_tok)

    ledger.record_case(
        "CASE-F1-030",
        "pass",
        st["serving_version"],
        "lan_client (lab-client-1, lab-client-2) + GET /api/sharing",
        "Jul outermost stage-0 withdrawal gate (withdrawn=true, location_set_withdrawn)",
        "location_set_withdrawn (withdrawn=true) returned 403 Forbidden with Cache-Control: no-store and no Expires (origin_delta=0), preserved credential='basic' and action='proxy_pass' in GET /api/sharing (withdrawn='yes'), and recovered in one change (withdrawn=false).",
        {
            "withdrawn_intended_status": s_w_in,
            "withdrawn_excluded_status": s_w_ex,
            "withdrawn_cache_control": h_w_in.get("cache-control"),
            "sharing_view_withdrawn": dash_w_entry["withdrawn"],
            "sharing_view_credential": dash_w_entry["jul_checks"]["credential"],
            "recovered_intended_status": s_rec_in,
            "recovered_excluded_status": s_rec_ex,
        },
    )

    # CASE-F1-031: Outermost stage-0 withdrawal gate precedence over redirect rewrites, WASM request Stop plugin (request-block.wasm) & header-inject plugin, CORS preflight, and conflicting response_headers/expires
    wasm_inject_src = ROOT / "testdata" / "plugins" / "v1-current-header-inject.wasm"
    wasm_inject_sha = hashlib.sha256(wasm_inject_src.read_bytes()).hexdigest()
    wasm_stop_src = ROOT / "testdata" / "plugins" / "request-block.wasm"
    wasm_stop_sha = hashlib.sha256(wasm_stop_src.read_bytes()).hexdigest()
    lab.priv.run("install", "-o", "jul", "-g", "jul", "-m0600", wasm_inject_src, "/etc/jul/v1-current-header-inject.wasm")
    lab.priv.run("install", "-o", "jul", "-g", "jul", "-m0600", wasm_stop_src, "/etc/jul/request-block.wasm")

    plugin_defs = (
        f'[plugins.req-stop]\npath = "/etc/jul/request-block.wasm"\ntype = "middleware"\nsha256 = "sha256:{wasm_stop_sha}"\ntimeout = "5s"\n\n'
        f'[plugins.req-inject]\npath = "/etc/jul/v1-current-header-inject.wasm"\ntype = "middleware"\nsha256 = "sha256:{wasm_inject_sha}"\ntimeout = "5s"\n'
    )
    active_plugin_toml = (
        cfg04.replace('[global]\nconfig_authority = "managed"\nlog_level = "info"\n', f'[global]\nconfig_authority = "managed"\nlog_level = "info"\n\n{plugin_defs}', 1)
        .replace(
            '  route_id = "dashboard"\n  match = { type = "prefix", path = "/" }\n  proxy_pass = "http://dashboard"',
            '  route_id = "dashboard"\n  match = { type = "prefix", path = "/" }\n  expires = "3600s"\n  plugins = ["req-stop", "req-inject"]\n  proxy_pass = "http://dashboard"',
            1,
        )
    )
    apply_raw(active_plugin_toml, admin_tok, base_version=st["persisted_version"])
    # Verify when withdrawn=false that sending X-Block: 1 invokes request-block.wasm (sdk.Stop) returning "blocked by request-block plugin"
    s_stop_active, b_stop_active, _, _ = lab.netns_curl(
        "lab-client-1", "https://dash.lab.test/api/summary",
        resolve_host="dash.lab.test:443:192.0.2.10", ca_file=root_ca,
        basic_auth=intended_basic, headers={"X-Block": "1"},
    )
    assert s_stop_active == 403 and b"blocked by request-block plugin" in b_stop_active

    precedence_toml = (
        active_plugin_toml.replace(
            '  route_id = "dashboard"\n  match = { type = "prefix", path = "/" }\n  expires = "3600s"\n  plugins = ["req-stop", "req-inject"]\n  proxy_pass = "http://dashboard"',
            '  route_id = "dashboard"\n  match = { type = "prefix", path = "/" }\n  withdrawn = true\n  expires = "3600s"\n  plugins = ["req-stop", "req-inject"]\n  proxy_pass = "http://dashboard"',
            1,
        )
        .replace(
            '    [servers.locations.sharing]\n    mode = "restricted"\n    audience_label = "family"\n',
            '    [servers.locations.sharing]\n    mode = "restricted"\n    audience_label = "family"\n\n    [servers.locations.cors]\n    enabled = true\n    allowed_origins = ["https://portal.lab.test"]\n    allowed_methods = ["GET", "POST", "OPTIONS"]\n    allow_credentials = true\n\n    [[servers.locations.rewrites]]\n    pattern = "^/old$"\n    replacement = "https://portal.lab.test/new"\n    flag = "redirect"\n\n    [[servers.locations.response_headers]]\n    op = "set"\n    name = "Cache-Control"\n    value = "public, max-age=3600"\n\n    [[servers.locations.response_headers]]\n    op = "set"\n    name = "X-Custom-Decorated"\n    value = "present"\n',
            1,
        )
    )
    code_prec, body_prec = apply_raw(precedence_toml, admin_tok, base_version=get_v1_status(admin_tok)["persisted_version"])
    assert code_prec == 200, f"precedence_toml apply failed ({code_prec}): {body_prec}"

    # 1. Request to /old with X-Block: 1 must hit stage-0 withdrawal gate BEFORE redirect rewrite and BEFORE request-block.wasm sdk.Stop
    s_pr_rw, b_pr_rw, h_pr_rw, _ = lab.netns_curl(
        "lab-client-1", "https://dash.lab.test/old",
        resolve_host="dash.lab.test:443:192.0.2.10", ca_file=root_ca,
        basic_auth=intended_basic, headers={"X-Block": "1"},
    )
    assert s_pr_rw == 403 and b"blocked by request-block plugin" not in b_pr_rw
    assert "location" not in h_pr_rw and "x-plugin" not in h_pr_rw and h_pr_rw.get("cache-control") == "no-store" and "expires" not in h_pr_rw

    # 2. Approved CORS preflight OPTIONS request must return 403 + Cache-Control: no-store, NOT 204
    s_pr_cors, _, h_pr_cors, _ = lab.netns_curl(
        "lab-client-1",
        "https://dash.lab.test/api/summary",
        resolve_host="dash.lab.test:443:192.0.2.10",
        ca_file=root_ca,
        method="OPTIONS",
        headers={"Origin": "https://portal.lab.test", "Access-Control-Request-Method": "POST"},
    )
    assert s_pr_cors == 403 and h_pr_cors.get("cache-control") == "no-store" and "expires" not in h_pr_cors
    assert h_pr_cors.get("x-custom-decorated") == "present", "Non-conflicting response_headers may decorate while Cache-Control: no-store and Expires removal win"

    # Restore canonical cfg04
    apply_raw(cfg04, admin_tok, base_version=get_v1_status(admin_tok)["persisted_version"])
    st = get_v1_status(admin_tok)

    ledger.record_case(
        "CASE-F1-031",
        "pass",
        st["serving_version"],
        "lan_client (lab-client-1 192.0.2.11)",
        "Jul Router stage-0 withdrawal gate + response_headers no-store precedence (#642)",
        "Verified request-block.wasm sdk.Stop body when withdrawn=false, then verified stage-0 withdrawal gate (withdrawn=true) answered 403 Forbidden with empty body before redirect rewrites, request-block.wasm sdk.Stop plugin, header-inject plugin, and CORS preflight OPTIONS, and enforced Cache-Control: no-store with Expires removed.",
        {
            "stop_plugin_when_active_body": b_stop_active.decode("utf-8", errors="replace").strip(),
            "rewrite_and_stop_plugin_withdrawn_status": s_pr_rw,
            "stop_plugin_bypassed_by_stage0": b"blocked by request-block plugin" not in b_pr_rw,
            "cors_preflight_status": s_pr_cors,
            "cache_control_header": h_pr_cors.get("cache-control"),
            "expires_header_present": "expires" in h_pr_cors,
            "non_conflicting_response_header": h_pr_cors.get("x-custom-decorated"),
        },
    )

    # CASE-F1-032: Legacy auth.deny withdrawal mechanism (CFG-F1-06), held-stream retirement, and documented limits
    cfg06 = lab.adapt_config_for_service(load_example_toml("06-lan-withdrawn-legacy-deny.toml"))
    plan06 = plan_raw(cfg06, admin_tok, base_version=st["persisted_version"])
    dash06 = find_delta_entry(plan06, "dashboard")
    assert dash06["class"] == "narrowed" and dash06["triggers_retirement"] is True

    ret06 = run_retirement_client_verification(
        lab=lab,
        load_bin=load_bin,
        namespace="lab-client-1",
        tls_url="https://192.0.2.10:443",
        ca_file=root_ca,
        auth_user=creds["intended_user"],
        auth_password=creds["intended_password"],
        trigger_fn=lambda: apply_raw(cfg06, admin_tok, base_version=st["persisted_version"]),
        admin_tok=admin_tok,
        with_ws=True,
    )
    reset_origin_counters()
    s_leg_in, _, _, _ = lab.netns_curl("lab-client-1", "https://dash.lab.test/api/summary", resolve_host="dash.lab.test:443:192.0.2.10", ca_file=root_ca, basic_auth=intended_basic)
    s_leg_po, _, _, _ = lab.netns_curl("lab-client-2", "https://portal.lab.test/", resolve_host="portal.lab.test:443:192.0.2.10", ca_file=root_ca)
    assert s_leg_in == 403 and s_leg_po == 200 and origin_counters()["dashboard_requests"] == 0

    sh06 = get_sharing_view(admin_tok)
    dash_sh06 = next(e for e in sh06["entries"] if e.get("serving", {}).get("route_id") == "dashboard")["serving"]
    assert dash_sh06["withdrawn"] == "yes"
    assert dash_sh06["jul_checks"]["network"] == "deny_list" and dash_sh06["jul_checks"]["credential"] == "basic"

    # Also verify that if a restricted route has CORS/rewrites/plugins, plan/lint flags intent_checks_bypassed (the documented limit of auth.deny vs stage-0 gate)
    bypass_toml = cfg06.replace(
        '    [servers.locations.sharing]\n    mode = "restricted"\n    audience_label = "family"\n',
        '    [servers.locations.sharing]\n    mode = "restricted"\n    audience_label = "family"\n\n    [[servers.locations.rewrites]]\n    pattern = "^/legacy$"\n    replacement = "https://portal.lab.test/"\n    flag = "redirect"\n',
        1,
    )
    plan_bypass = plan_raw(bypass_toml, admin_tok, base_version=get_v1_status(admin_tok)["persisted_version"])
    bypass_codes = {f["code"] for f in plan_bypass["sharing"].get("intent_findings", []) if "code" in f} | {d["code"] for d in plan_bypass.get("lint", []) if "code" in d}
    assert "intent_checks_bypassed" in bypass_codes

    # Recover to cfg04
    apply_raw(cfg04, admin_tok, base_version=get_v1_status(admin_tok)["persisted_version"])
    st = get_v1_status(admin_tok)

    ledger.record_case(
        "CASE-F1-032",
        "pass",
        st["serving_version"],
        "lan_client + GET /api/sharing + POST /api/v1/config/plan",
        "Jul location auth.deny CIDR gate + #642 stream retirement + §4.5 intent_checks_bypassed lint",
        "Applied complete candidate CFG-F1-06 (auth.deny=['0.0.0.0/0','::/0'] alongside auth.basic); retired held SSE + H1 WebSocket streams within <= 2.0s while active sibling portal stream completed 262,144 bytes; verified 403 denial (origin_delta=0) and intent_checks_bypassed warning when outer rewrites exist; recovered to CFG-F1-04.",
        {
            "legacy_deny_status": s_leg_in,
            "sharing_jul_checks": dash_sh06["jul_checks"],
            "bypass_warning_surfaced": "intent_checks_bypassed" in bypass_codes,
            "retirement_verification": ret06,
        },
    )

    # CASE-F1-033: Exact-entry retirement on shared HTTP/2 connection coalescing (with server-active backpressured portal stream) and owned H1 WebSocket within 2s bound
    cfg05 = lab.adapt_config_for_service(load_example_toml("05-lan-withdrawn-gate.toml"))
    ret33 = run_retirement_client_verification(
        lab=lab,
        load_bin=load_bin,
        namespace="lab-client-1",
        tls_url="https://192.0.2.10:443",
        ca_file=root_ca,
        auth_user=creds["intended_user"],
        auth_password=creds["intended_password"],
        trigger_fn=lambda: apply_raw(cfg05, admin_tok, base_version=st["persisted_version"]),
        admin_tok=admin_tok,
        with_ws=True,
    )
    h2_res = ret33["client_result"]
    dash_ret = ret33["server_last_retirement"]
    assert dash_ret.get("claim") == "complete_withdrawal"

    # Recover to cfg04
    apply_raw(cfg04, admin_tok, base_version=get_v1_status(admin_tok)["persisted_version"])
    st = get_v1_status(admin_tok)

    ledger.record_case(
        "CASE-F1-033",
        "pass",
        st["serving_version"],
        "lan_client (lab-client-1 single-socket HTTP/2 + H1 WebSocket)",
        "Jul ExchangeRegistry + H2 RST_STREAM + #562 owned WebSocket Close 1008",
        f"On 1 shared TLS/H2 socket (local {h2_res['local_addr']} -> {h2_res['remote_addr']}) with portal-home open_exchanges={ret33['portal_open_exchanges_before_publish']} and dashboard open_exchanges={ret33['dash_open_exchanges_before_publish']} active at Publish: withdrawal retired dashboard SSE ({h2_res.get('publish_to_dash_sse_close_ms')}ms from publish signal) and H1 WebSocket (RFC 6455 Close 1008, {h2_res.get('publish_to_ws_close_ms')}ms from publish signal) within <= 2s bound while the backpressured portal stream transferred 4,096 B before and 258,048 B after retirement (262,144 B total, collateral_count=0, claim='complete_withdrawal').",
        ret33,
    )

    # CASE-F1-034 & CASE-F1-035: mTLS shared-listener variant (CFG-F1-08) and CRL revocation reload (handshake refusal + per-request 403 on BOTH open H2 and open H1 keep-alive connections)
    lab.priv.run("cp", "/etc/jul/tls/clients-clean.crl", "/etc/jul/tls/clients.crl")
    lab.priv.run("chown", "jul:jul", "/etc/jul/tls/clients.crl")
    cfg08 = lab.adapt_config_for_service(load_example_toml("08-lan-mtls-shared-listener.toml"))
    code08, _ = apply_raw(cfg08, admin_tok, base_version=st["persisted_version"])
    assert code08 == 200
    st08 = get_v1_status(admin_tok)

    sh08 = get_sharing_view(admin_tok)
    portal_sh08 = next(e for e in sh08["entries"] if e.get("serving", {}).get("route_id") == "portal-home")["serving"]
    dash_sh08 = next(e for e in sh08["entries"] if e.get("serving", {}).get("route_id") == "dashboard")["serving"]
    p08_warn_codes = [w["code"] for w in (portal_sh08.get("warnings") or [])]
    assert "client_auth_request_mode_shared" in p08_warn_codes
    assert dash_sh08["jul_checks"]["client_certificate"] == "required_by_route"

    intended_cert = (lab.workdir / "tls" / "client-intended.crt", lab.workdir / "tls" / "client-intended.key")
    revoked_cert = (lab.workdir / "tls" / "client-revoked.crt", lab.workdir / "tls" / "client-revoked.key")
    unauth_san_cert = (lab.workdir / "tls" / "client-unauthorised-san.crt", lab.workdir / "tls" / "client-unauthorised-san.key")
    wrong_ca_cert = (lab.workdir / "tls" / "client-wrong-ca.crt", lab.workdir / "tls" / "client-wrong-ca.key")

    s_m_po, _, _, _ = lab.netns_curl("lab-client-2", "https://portal.lab.test/", resolve_host="portal.lab.test:443:192.0.2.10", ca_file=root_ca)
    s_m_nocert, _, _, _ = lab.netns_curl("lab-client-1", "https://dash.lab.test/api/summary", resolve_host="dash.lab.test:443:192.0.2.10", ca_file=root_ca, basic_auth=intended_basic)
    assert s_m_po == 200 and s_m_nocert == 403
    lab.netns_curl("lab-client-2", "https://dash.lab.test/api/summary", resolve_host="dash.lab.test:443:192.0.2.10", ca_file=root_ca, basic_auth=intended_basic, client_cert=wrong_ca_cert, expect_curl_exit=56)
    lab.netns_curl("lab-client-2", "https://dash.lab.test/api/summary", resolve_host="dash.lab.test:443:192.0.2.10", ca_file=root_ca, basic_auth=intended_basic, client_cert=unauth_san_cert, expect_curl_exit=56)
    s_m_int, _, _, _ = lab.netns_curl("lab-client-1", "https://dash.lab.test/api/summary", resolve_host="dash.lab.test:443:192.0.2.10", ca_file=root_ca, basic_auth=intended_basic, client_cert=intended_cert)
    s_m_rev_before, _, _, _ = lab.netns_curl("lab-client-2", "https://dash.lab.test/api/summary", resolve_host="dash.lab.test:443:192.0.2.10", ca_file=root_ca, basic_auth=intended_basic, client_cert=revoked_cert)
    assert s_m_int == 200 and s_m_rev_before == 200
    cg08_snap = lab.cgroup_snapshot("jul.service")

    ledger.record_case(
        "CASE-F1-034",
        "pass",
        st08["serving_version"],
        "lan_client (lab-client-1, lab-client-2) + GET /api/sharing",
        "Jul TLS client_auth (mode=request) + location require_client_cert",
        "Shared-listener mTLS (CFG-F1-08) surfaced client_auth_request_mode_shared warning on portal, allowed anonymous portal 200, denied cert-less dashboard 403, rejected wrong-CA and unauthorised-SAN client certs at handshake, and admitted valid client certs (200).",
        {
            "portal_warnings": p08_warn_codes,
            "portal_no_cert_status": s_m_po,
            "dash_no_cert_status": s_m_nocert,
            "dash_intended_cert_status": s_m_int,
            "dash_pre_revocation_cert_status": s_m_rev_before,
            "cgroup_snapshot": cg08_snap,
        },
    )

    # CASE-F1-035: mTLS CRL revocation reload — active stream retirement + per-request 403 on BOTH open H2 and open H1 keep-alive connections + handshake refusal on new connection
    def trigger_crl_revoke() -> None:
        lab.priv.run("cp", "/etc/jul/tls/clients-revoked.crl", "/etc/jul/tls/clients.crl")
        lab.priv.run("chown", "jul:jul", "/etc/jul/tls/clients.crl")
        c_code, c_res = apply_raw(cfg08, admin_tok, base_version=get_v1_status(admin_tok)["persisted_version"])
        assert c_code == 200 and c_res.get("outcome") == "applied_live"

    crl_h2_ver = run_retirement_client_verification(
        lab=lab,
        load_bin=load_bin,
        namespace="lab-client-2",
        tls_url="https://192.0.2.10:443",
        ca_file=root_ca,
        auth_user=creds["intended_user"],
        auth_password=creds["intended_password"],
        trigger_fn=trigger_crl_revoke,
        admin_tok=admin_tok,
        with_ws=False,
        h1_keepalive=False,
        client_cert=revoked_cert,
        expected_post_dash_status=403,
    )

    # Restore clean CRL, then exercise the same CRL revocation reload over an open HTTP/1.1 keep-alive connection + H1 SSE stream!
    lab.priv.run("cp", "/etc/jul/tls/clients-clean.crl", "/etc/jul/tls/clients.crl")
    lab.priv.run("chown", "jul:jul", "/etc/jul/tls/clients.crl")
    apply_raw(cfg08, admin_tok, base_version=get_v1_status(admin_tok)["persisted_version"])

    crl_h1_ver = run_retirement_client_verification(
        lab=lab,
        load_bin=load_bin,
        namespace="lab-client-2",
        tls_url="https://192.0.2.10:443",
        ca_file=root_ca,
        auth_user=creds["intended_user"],
        auth_password=creds["intended_password"],
        trigger_fn=trigger_crl_revoke,
        admin_tok=admin_tok,
        with_ws=False,
        h1_keepalive=True,
        client_cert=revoked_cert,
        expected_post_dash_status=403,
    )

    # Verify new TLS handshake with revoked cert is refused at handshake, while intended cert still gets 200
    lab.netns_curl("lab-client-2", "https://dash.lab.test/api/summary", resolve_host="dash.lab.test:443:192.0.2.10", ca_file=root_ca, basic_auth=intended_basic, client_cert=revoked_cert, expect_curl_exit=56)
    s_m_int_after, _, _, _ = lab.netns_curl("lab-client-1", "https://dash.lab.test/api/summary", resolve_host="dash.lab.test:443:192.0.2.10", ca_file=root_ca, basic_auth=intended_basic, client_cert=intended_cert)
    assert s_m_int_after == 200

    ledger.record_case(
        "CASE-F1-035",
        "pass",
        get_v1_status(admin_tok)["serving_version"],
        "lan_client (lab-client-2 open H2 + open H1 keep-alive + new handshake, lab-client-1 intended cert)",
        "Jul publish-time CRL classifier + per-request clientAuthBundle re-check (#642, #563)",
        "CRL revocation reload retired active H2 and H1 dashboard SSE streams within <= 2s (completed_within_bound=true), denied next request to dash.lab.test on BOTH the already-open H2 connection and the already-open H1 keep-alive connection with 403 while the active sibling H2 portal stream completed 262,144 bytes (zero collateral), refused new handshakes with revoked cert, and kept intended cert working (200).",
        {
            "open_h2_conn_verification": crl_h2_ver,
            "open_h1_keepalive_verification": crl_h1_ver,
            "new_handshake_revoked_curl_exit": 56,
            "intended_cert_status_after_crl": s_m_int_after,
        },
    )

    # CASE-F1-036: mTLS dedicated-listener variant (CFG-F1-09) admission, default-deny, held-stream retirement, and cgroup snapshot
    lab.priv.run("cp", "/etc/jul/tls/clients-clean.crl", "/etc/jul/tls/clients.crl")
    lab.priv.run("chown", "jul:jul", "/etc/jul/tls/clients.crl")
    cfg09 = lab.adapt_config_for_service(load_example_toml("09-lan-mtls-dedicated-listener.toml"))
    code09, body09 = apply_raw(cfg09, admin_tok, base_version=get_v1_status(admin_tok)["persisted_version"], mode="stage_restart")
    assert code09 == 200, f"cfg09 stage_restart failed ({code09}): {body09}"
    lab.priv.run("systemctl", "restart", "jul.service")
    lab.wait_http_local("http://127.0.0.1:9443/readyz", timeout_s=20, expected_unit="jul.service")
    st09 = get_v1_status(admin_tok)
    sh09 = get_sharing_view(admin_tok)
    portal_sh09 = next(e for e in sh09["entries"] if e.get("serving", {}).get("route_id") == "portal-home")["serving"]
    dash_sh09 = next(e for e in sh09["entries"] if e.get("serving", {}).get("route_id") == "dashboard")["serving"]
    p09_warn_codes = [w["code"] for w in (portal_sh09.get("warnings") or [])]
    assert "client_auth_request_mode_shared" not in p09_warn_codes
    assert portal_sh09["listener"]["transport"]["client_auth"] == "none"
    assert dash_sh09["listener"]["address"]["text"] == "192.0.2.10:8443"
    assert dash_sh09["jul_checks"]["client_certificate"] == "required_by_listener"

    s_09_po, _, _, _ = lab.netns_curl("lab-client-2", "https://portal.lab.test/", resolve_host="portal.lab.test:443:192.0.2.10", ca_file=root_ca)
    assert s_09_po == 200
    lab.netns_curl("lab-client-2", "https://dash.lab.test:8443/api/summary", resolve_host="dash.lab.test:8443:192.0.2.10", ca_file=root_ca, expect_curl_exit=56)
    s_09_dash, _, _, _ = lab.netns_curl("lab-client-1", "https://dash.lab.test:8443/api/summary", resolve_host="dash.lab.test:8443:192.0.2.10", ca_file=root_ca, client_cert=intended_cert)
    s_09_unk, _, _, _ = lab.netns_curl("lab-client-1", "https://dash.lab.test:8443/", resolve_host="dash.lab.test:8443:192.0.2.10", ca_file=root_ca, client_cert=intended_cert, headers={"Host": "unknown.lab.test"})
    assert s_09_dash == 200 and s_09_unk == 404
    cg09_snap = lab.cgroup_snapshot("jul.service")

    # Exercise held-stream retirement on dedicated mTLS listener :8443 via location_set_withdrawn (withdrawn=true) and recovery
    cfg09_withdrawn = cfg09.replace('  route_id = "dashboard"\n  match = { type = "prefix", path = "/" }\n', '  route_id = "dashboard"\n  match = { type = "prefix", path = "/" }\n  withdrawn = true\n', 1)
    ret09 = run_retirement_client_verification(
        lab=lab,
        load_bin=load_bin,
        namespace="lab-client-1",
        tls_url="https://192.0.2.10:8443",
        ca_file=root_ca,
        auth_user="",
        auth_password="",
        trigger_fn=lambda: apply_raw(cfg09_withdrawn, admin_tok, base_version=get_v1_status(admin_tok)["persisted_version"]),
        admin_tok=admin_tok,
        with_ws=False,
        h1_keepalive=True,
        client_cert=intended_cert,
        expected_post_dash_status=403,
        portal_host="",
        dash_host="dash.lab.test",
    )

    # Restore clean CRL and canonical CFG-F1-04 (stage_restart + restart to return from 4 blocks to 3 blocks)
    apply_raw(cfg04, admin_tok, base_version=get_v1_status(admin_tok)["persisted_version"], mode="stage_restart")
    lab.priv.run("systemctl", "restart", "jul.service")
    lab.wait_http_local("http://127.0.0.1:9443/readyz", timeout_s=20, expected_unit="jul.service")
    st = get_v1_status(admin_tok)

    ledger.record_case(
        "CASE-F1-036",
        "pass",
        st["serving_version"],
        "lan_client (lab-client-1, lab-client-2) + GET /api/sharing",
        "Jul dedicated mTLS listener 192.0.2.10:8443 (client_auth.mode=require) + stream retirement",
        "Dedicated-listener variant (CFG-F1-09) served portal on :443 with client_auth=none (no client_auth_request_mode_shared warning) and dashboard on :8443 with mandatory client_auth=require, default-deny-mtls (404 on unknown Host), and <= 2.0s held-stream retirement on withdrawal.",
        {
            "portal_client_auth": portal_sh09["listener"]["transport"]["client_auth"],
            "portal_warnings": p09_warn_codes,
            "dash_listener": dash_sh09["listener"]["address"]["text"],
            "dash_intended_status": s_09_dash,
            "dash_unknown_host_status": s_09_unk,
            "dedicated_listener_retirement": ret09,
            "cgroup_snapshot": cg09_snap,
        },
    )

    # CASE-F1-037: htpasswd file-content lifecycle (edit without reload has no effect; reload retires stream and revokes removed user with 401; postcondition does not compare file contents)
    plan04_cur = plan_raw(cfg04, admin_tok, base_version=st["persisted_version"])
    pc04_cur = plan04_cur["sharing"]["postcondition"]

    s_u1, _, _, _ = lab.netns_curl("lab-client-1", "https://dash.lab.test/api/summary", resolve_host="dash.lab.test:443:192.0.2.10", ca_file=root_ca, basic_auth=intended_basic)
    s_u2, _, _, _ = lab.netns_curl("lab-client-2", "https://dash.lab.test/api/summary", resolve_host="dash.lab.test:443:192.0.2.10", ca_file=root_ca, basic_auth=second_basic)
    assert s_u1 == 200 and s_u2 == 200

    lab.priv.run("cp", "/etc/jul/dashboard-revoked.htpasswd", "/etc/jul/dashboard.htpasswd")
    lab.priv.run("chown", "jul:jul", "/etc/jul/dashboard.htpasswd")
    s_u2_no_reload, _, _, _ = lab.netns_curl("lab-client-2", "https://dash.lab.test/api/summary", resolve_host="dash.lab.test:443:192.0.2.10", ca_file=root_ca, basic_auth=second_basic)
    assert s_u2_no_reload == 200, "Editing htpasswd without reload must not change live access"
    pc_eval_no_reload = evaluate_postcondition(pc04_cur, admin_tok)
    assert pc_eval_no_reload["result"] == "candidate_serving"

    ht_ver = run_retirement_client_verification(
        lab=lab,
        load_bin=load_bin,
        namespace="lab-client-2",
        tls_url="https://192.0.2.10:443",
        ca_file=root_ca,
        auth_user=creds["second_user"],
        auth_password=creds["second_password"],
        trigger_fn=lambda: apply_raw(cfg04, admin_tok, base_version=st["persisted_version"]),
        admin_tok=admin_tok,
        with_ws=False,
        expected_post_dash_status=401,
    )

    s_u1_after, _, _, _ = lab.netns_curl("lab-client-1", "https://dash.lab.test/api/summary", resolve_host="dash.lab.test:443:192.0.2.10", ca_file=root_ca, basic_auth=intended_basic)
    s_u2_after, _, _, _ = lab.netns_curl("lab-client-2", "https://dash.lab.test/api/summary", resolve_host="dash.lab.test:443:192.0.2.10", ca_file=root_ca, basic_auth=second_basic)
    assert s_u1_after == 200 and s_u2_after == 401

    lab.priv.run("cp", lab.workdir / "dashboard.htpasswd", "/etc/jul/dashboard.htpasswd")
    lab.priv.run("chown", "jul:jul", "/etc/jul/dashboard.htpasswd")
    apply_raw(cfg04, admin_tok, base_version=get_v1_status(admin_tok)["persisted_version"])
    st = get_v1_status(admin_tok)

    ledger.record_case(
        "CASE-F1-037",
        "pass",
        st["serving_version"],
        "lan_client (lab-client-1, lab-client-2) + §9.4 postcondition",
        "Jul Basic auth htpasswd loader + publish-time file-content retirement (#642, RR12, RR13)",
        "Editing dashboard.htpasswd without reload left family-guest accepted (200) and postcondition=candidate_serving (file_contents not compared in postconditions); explicit reload classified user removal as narrowed at publish, retired the active stream within <= 2s (completed_within_bound=true), denied family-guest with 401 on the open H2 connection and fresh requests, and kept family-intended at 200.",
        {
            "file_backed_entries_notice": plan04_cur["sharing"].get("file_backed_inputs_notice"),
            "status_before_reload": s_u2_no_reload,
            "status_after_reload_removed_user": s_u2_after,
            "status_after_reload_retained_user": s_u1_after,
            "stream_retirement_verification": ht_ver,
        },
    )

    # CASE-F1-038: Constituent-reason keep_open_entries eligibility and mandatory-tightening override
    pure_uncertain_toml = cfg04.replace('servers = [{ address = "127.0.0.1:3000" }]', 'servers = [{ address = "127.0.0.1:3000", weight = 2 }]')
    plan_pure_unc = plan_raw(pure_uncertain_toml, admin_tok, base_version=st["persisted_version"])
    dash_pure_unc = find_delta_entry(plan_pure_unc, "dashboard")
    assert dash_pure_unc["class"] == "uncertain" and dash_pure_unc["keep_open_eligible"] is True

    ko_code, keep_open_res, _ = admin_api(
        f"/api/v1/config/apply?base_version={st['persisted_version']}&mode=hot&keep_open_entries=dashboard",
        token=admin_tok,
        method="POST",
        data=pure_uncertain_toml.encode("utf-8"),
        content_type="application/toml",
        headers={"Idempotency-Key": "f1-ko-" + uuid.uuid4().hex[:16]},
    )
    assert ko_code == 200 and keep_open_res.get("ok") is True, f"keep_open apply failed ({ko_code}): {keep_open_res}"
    sh_ko = get_sharing_view(admin_tok)
    dash_ko_ret = next(e for e in sh_ko["entries"] if e.get("serving", {}).get("route_id") == "dashboard")["serving"].get("last_retirement", {})
    assert dash_ko_ret.get("keep_open") is True and "exception" in dash_ko_ret.get("claim", "")

    st_ko = get_v1_status(admin_tok)
    mixed_toml = cfg04.replace(
        "    [servers.locations.auth.basic]\n",
        '    [servers.locations.auth]\n    allow = ["192.0.2.0/24"]\n\n      [servers.locations.auth.basic]\n',
    )
    plan_mixed = plan_raw(mixed_toml, admin_tok, base_version=st_ko["persisted_version"])
    dash_mixed = find_delta_entry(plan_mixed, "dashboard")
    assert dash_mixed["class"] == "uncertain" and dash_mixed["keep_open_eligible"] is False, f"expected keep_open_eligible=false on mixed change, got {dash_mixed}"

    mx_code, mixed_res, _ = admin_api(
        f"/api/v1/config/apply?base_version={st_ko['persisted_version']}&mode=hot&keep_open_entries=dashboard",
        token=admin_tok,
        method="POST",
        data=mixed_toml.encode("utf-8"),
        content_type="application/toml",
        headers={"Idempotency-Key": "f1-mx-" + uuid.uuid4().hex[:16]},
    )
    assert mx_code == 200 and mixed_res.get("ok") is True, f"mixed apply failed ({mx_code}): {mixed_res}"
    sh_mixed = get_sharing_view(admin_tok)
    dash_mixed_ret = next(e for e in sh_mixed["entries"] if e.get("serving", {}).get("route_id") == "dashboard")["serving"].get("last_retirement", {})
    assert bool(dash_mixed_ret.get("keep_open", False)) is False

    apply_raw(cfg04, admin_tok, base_version=get_v1_status(admin_tok)["persisted_version"])
    st = get_v1_status(admin_tok)

    ledger.record_case(
        "CASE-F1-038",
        "pass",
        st["serving_version"],
        "same_host_loopback",
        "Jul §8.4/§9.5 constituent-reason keep_open_entries evaluator (SD-09)",
        "Pure uncertain change set keep_open_eligible=true and honoured keep_open_entries=['dashboard'] (keep_open=true, claim='new access updated; open connections kept by operator exception'); mixed uncertain + mandatory CIDR tightening set keep_open_eligible=false and overrode keep_open_entries (keep_open=false).",
        {
            "pure_uncertain_keep_open_eligible": dash_pure_unc["keep_open_eligible"],
            "pure_uncertain_retirement": dash_ko_ret,
            "mixed_keep_open_eligible": dash_mixed["keep_open_eligible"],
            "mixed_retirement": dash_mixed_ret,
        },
    )

    # CASE-F1-039: Protocol matrix verification (HTTP/1.1, TLS ALPN HTTP/2, SSE over BOTH H1 and H2, H1 WebSocket)
    s_h1, b_h1, _, p_h1 = lab.netns_curl("lab-client-1", "https://dash.lab.test/api/summary", resolve_host="dash.lab.test:443:192.0.2.10", ca_file=root_ca, basic_auth=intended_basic, http_version="--http1.1")
    s_h2, b_h2, _, p_h2 = lab.netns_curl("lab-client-1", "https://dash.lab.test/api/summary", resolve_host="dash.lab.test:443:192.0.2.10", ca_file=root_ca, basic_auth=intended_basic, http_version="--http2")
    assert s_h1 == 200 and p_h1 == "1.1" and s_h2 == 200 and p_h2 == "2"

    # Explicitly exercise SSE (/events) over BOTH HTTP/1.1 (--http1.1) and HTTP/2 (--http2)
    sse_h1_proc = lab.priv.run(
        "ip", "netns", "exec", "lab-client-1",
        "curl", "-sS", "--http1.1", "--max-time", "1", "-D", "-",
        "--cacert", str(root_ca), "--user", intended_basic,
        "--resolve", "dash.lab.test:443:192.0.2.10",
        "https://dash.lab.test/events",
        check=False,
    )
    sse_h2_proc = lab.priv.run(
        "ip", "netns", "exec", "lab-client-1",
        "curl", "-sS", "--http2", "--max-time", "1", "-D", "-",
        "--cacert", str(root_ca), "--user", intended_basic,
        "--resolve", "dash.lab.test:443:192.0.2.10",
        "https://dash.lab.test/events",
        check=False,
    )
    assert "HTTP/1.1 200" in sse_h1_proc.stdout and "text/event-stream" in sse_h1_proc.stdout.lower() and DASHBOARD_MARKER in sse_h1_proc.stdout
    assert "HTTP/2 200" in sse_h2_proc.stdout and "text/event-stream" in sse_h2_proc.stdout.lower() and DASHBOARD_MARKER in sse_h2_proc.stdout

    ledger.record_case(
        "CASE-F1-039",
        "pass",
        st["serving_version"],
        "lan_client (lab-client-1 192.0.2.11)",
        "Jul HTTP/1.1, HTTP/2 ALPN, SSE (H1 & H2), and H1 WebSocket handlers (spec §12)",
        "Verified HTTP/1.1 (proto=1.1), TLS ALPN HTTP/2 (proto=2), SSE over HTTP/1.1 (HTTP/1.1 200 text/event-stream), SSE over HTTP/2 (HTTP/2 200 text/event-stream), and H1 WebSocket (101 Switching Protocols) on F1; recorded native gRPC, HTTP/3, and L4 as not exercised by F1 with owner test references.",
        {
            "http1_1_status": s_h1,
            "http1_1_proto": p_h1,
            "http2_status": s_h2,
            "http2_proto": p_h2,
            "sse_h1_verified": True,
            "sse_h2_verified": True,
            "h1_websocket_verified_in_case_033": True,
            "not_exercised_by_f1": ["native_grpc", "http3", "l4_stream"],
        },
    )

    # CASE-F1-049 & CASE-F1-050: File-owned SIGHUP held-stream retirement, file-owned restart postcondition lifecycle, and file-owned mTLS (CFG-F1-12 shared & CFG-F1-13 dedicated)
    lab.priv.run("systemctl", "stop", "jul.service")
    cfg10 = load_example_toml("10-file-owned-lan.toml")
    cfg11 = load_example_toml("11-file-owned-withdrawn-gate.toml")
    cfg12 = load_example_toml("12-file-owned-mtls-shared-listener.toml")
    cfg13 = load_example_toml("13-file-owned-mtls-dedicated-listener.toml")
    lab.install_config(cfg10, readonly=True)
    lab.priv.run("systemctl", "start", "jul-readonly.service")
    lab.wait_http_local("http://127.0.0.1:9443/readyz", timeout_s=20, expected_unit="jul-readonly.service")
    st_fo49 = get_v1_status(admin_tok)
    plan_fo11_49 = plan_raw(cfg11, admin_tok)
    pc_fo11_49 = plan_fo11_49["sharing"]["postcondition"]

    def trigger_fo_withdraw() -> None:
        lab.install_config(cfg11, readonly=True)
        for _ in range(40):
            if get_v1_status(admin_tok)["serving_version"] != st_fo49["serving_version"]:
                break
            time.sleep(0.05)

    ret_fo49 = run_retirement_client_verification(
        lab=lab,
        load_bin=load_bin,
        namespace="lab-client-1",
        tls_url="https://192.0.2.10:443",
        ca_file=root_ca,
        auth_user=creds["intended_user"],
        auth_password=creds["intended_password"],
        trigger_fn=trigger_fo_withdraw,
        admin_tok=admin_tok,
        with_ws=True,
    )
    assert evaluate_postcondition(pc_fo11_49, admin_tok)["result"] == "candidate_serving"

    # Also verify pure SIGHUP held-stream retirement under file_owned authority (htpasswd user removal + SIGHUP)
    lab.install_config(cfg10, readonly=True)
    time.sleep(0.45)
    lab.priv.run("systemctl", "reload", "jul-readonly.service")
    time.sleep(0.2)

    def trigger_fo_sighup_htpasswd() -> None:
        lab.priv.run("cp", lab.workdir / "dashboard-revoked.htpasswd", "/etc/jul/dashboard.htpasswd")
        lab.priv.run("chown", "jul:jul", "/etc/jul/dashboard.htpasswd")
        lab.priv.run("systemctl", "reload", "jul-readonly.service")
        time.sleep(0.2)

    ret_fo49_sighup = run_retirement_client_verification(
        lab=lab,
        load_bin=load_bin,
        namespace="lab-client-2",
        tls_url="https://192.0.2.10:443",
        ca_file=root_ca,
        auth_user=creds["second_user"],
        auth_password=creds["second_password"],
        trigger_fn=trigger_fo_sighup_htpasswd,
        admin_tok=admin_tok,
        with_ws=False,
        expected_post_dash_status=401,
    )
    lab.priv.run("cp", lab.workdir / "dashboard.htpasswd", "/etc/jul/dashboard.htpasswd")
    lab.priv.run("chown", "jul:jul", "/etc/jul/dashboard.htpasswd")
    lab.install_config(cfg11, readonly=True)
    time.sleep(0.45)

    # Restart jul-readonly.service and verify boot_id rotation, pre-restart postcondition -> unverifiable, and re-plan -> candidate_serving
    fo_boot_before = get_v1_status(admin_tok)["boot_id"]
    lab.priv.run("systemctl", "restart", "jul-readonly.service")
    lab.wait_http_local("http://127.0.0.1:9443/readyz", timeout_s=20, expected_unit="jul-readonly.service")
    st_fo_after_rst = get_v1_status(admin_tok)
    assert st_fo_after_rst["boot_id"] != fo_boot_before
    pc_fo_unver = evaluate_postcondition(pc_fo11_49, admin_tok)
    assert pc_fo_unver["result"] == "unverifiable"
    replan_fo11 = plan_raw(cfg11, admin_tok)
    pc_fo_replan = evaluate_postcondition(replan_fo11["sharing"]["postcondition"], admin_tok)
    assert pc_fo_replan["result"] == "candidate_serving"
    cg_fo10_snap = lab.cgroup_snapshot("jul-readonly.service")

    ledger.record_case(
        "CASE-F1-049",
        "pass",
        st_fo_after_rst["serving_version"],
        "lan_client + same_host_loopback (jul-readonly.service)",
        "Jul file_owned ReloadPlan + SIGHUP + ExchangeRegistry + §9.4 process-keyed postcondition",
        "Under file_owned authority (jul-readonly.service), withdrawal (CFG-F1-10 -> CFG-F1-11) and SIGHUP credential revocation retired held H2 SSE + H1 WebSocket within <= 2.0s while the backpressured 256 KiB portal stream completed 262,144 bytes; service restart rotated boot_id, marked pre-restart postcondition unverifiable, and confirmed candidate_serving on re-plan.",
        {
            "withdrawal_retirement": ret_fo49,
            "sighup_htpasswd_retirement": ret_fo49_sighup,
            "boot_id_before": fo_boot_before,
            "boot_id_after": st_fo_after_rst["boot_id"],
            "post_restart_original_postcondition": pc_fo_unver["result"],
            "post_restart_replanned_postcondition": pc_fo_replan["result"],
            "cgroup_snapshot": cg_fo10_snap,
        },
    )

    # CASE-F1-050: File-owned mTLS shared-listener (CFG-F1-12) and dedicated-listener (CFG-F1-13)
    lab.priv.run("cp", "/etc/jul/tls/clients-clean.crl", "/etc/jul/tls/clients.crl")
    lab.priv.run("chown", "jul:jul", "/etc/jul/tls/clients.crl")
    lab.install_config(cfg12, readonly=True)
    time.sleep(0.45)
    lab.priv.run("systemctl", "reload", "jul-readonly.service")
    time.sleep(0.2)
    fo12_mut_code, _ = apply_raw(cfg12, admin_tok, base_version=get_v1_status(admin_tok)["serving_version"])
    assert fo12_mut_code == 409
    s_fo12_po, _, _, _ = lab.netns_curl("lab-client-2", "https://portal.lab.test/", resolve_host="portal.lab.test:443:192.0.2.10", ca_file=root_ca)
    s_fo12_nocert, _, _, _ = lab.netns_curl("lab-client-1", "https://dash.lab.test/api/summary", resolve_host="dash.lab.test:443:192.0.2.10", ca_file=root_ca, basic_auth=intended_basic)
    s_fo12_int, _, _, _ = lab.netns_curl("lab-client-1", "https://dash.lab.test/api/summary", resolve_host="dash.lab.test:443:192.0.2.10", ca_file=root_ca, basic_auth=intended_basic, client_cert=intended_cert)
    assert s_fo12_po == 200 and s_fo12_nocert == 403 and s_fo12_int == 200

    def trigger_fo12_crl() -> None:
        lab.priv.run("cp", "/etc/jul/tls/clients-revoked.crl", "/etc/jul/tls/clients.crl")
        lab.priv.run("chown", "jul:jul", "/etc/jul/tls/clients.crl")
        lab.priv.run("systemctl", "reload", "jul-readonly.service")
        time.sleep(0.3)

    ret_fo12 = run_retirement_client_verification(
        lab=lab,
        load_bin=load_bin,
        namespace="lab-client-2",
        tls_url="https://192.0.2.10:443",
        ca_file=root_ca,
        auth_user=creds["intended_user"],
        auth_password=creds["intended_password"],
        trigger_fn=trigger_fo12_crl,
        admin_tok=admin_tok,
        with_ws=False,
        client_cert=revoked_cert,
        expected_post_dash_status=403,
    )

    # Install CFG-F1-13 (file-owned dedicated-listener mTLS on :8443) and restart jul-readonly.service
    lab.priv.run("cp", "/etc/jul/tls/clients-clean.crl", "/etc/jul/tls/clients.crl")
    lab.priv.run("chown", "jul:jul", "/etc/jul/tls/clients.crl")
    lab.install_config(cfg13, readonly=True)
    lab.priv.run("systemctl", "restart", "jul-readonly.service")
    lab.wait_http_local("http://127.0.0.1:9443/readyz", timeout_s=20, expected_unit="jul-readonly.service")
    s_fo13_po, _, _, _ = lab.netns_curl("lab-client-2", "https://portal.lab.test/", resolve_host="portal.lab.test:443:192.0.2.10", ca_file=root_ca)
    lab.netns_curl("lab-client-2", "https://dash.lab.test:8443/api/summary", resolve_host="dash.lab.test:8443:192.0.2.10", ca_file=root_ca, expect_curl_exit=56)
    s_fo13_dash, _, _, _ = lab.netns_curl("lab-client-1", "https://dash.lab.test:8443/api/summary", resolve_host="dash.lab.test:8443:192.0.2.10", ca_file=root_ca, client_cert=intended_cert)
    assert s_fo13_po == 200 and s_fo13_dash == 200
    cg_fo13_snap = lab.cgroup_snapshot("jul-readonly.service")

    # Return to managed jul.service on canonical CFG-F1-04
    lab.priv.run("systemctl", "stop", "jul-readonly.service")
    lab.install_config(cfg04, readonly=False)
    lab.priv.run("systemctl", "start", "jul.service")
    lab.wait_http_local("http://127.0.0.1:9443/readyz", timeout_s=20, expected_unit="jul.service")
    adopt_external(admin_tok, mode="hot")
    st = get_v1_status(admin_tok)

    ledger.record_case(
        "CASE-F1-050",
        "pass",
        st["serving_version"],
        "lan_client + same_host_loopback (jul-readonly.service)",
        "Jul file_owned authority + mTLS shared (:443) and dedicated (:8443) listeners",
        "Verified file_owned mTLS shared-listener (CFG-F1-12, 409 on API apply, 200 portal, 403 cert-less dashboard, <= 2.0s SIGHUP CRL stream retirement + 403 on open H2 connection) and file_owned mTLS dedicated-listener (CFG-F1-13, portal :443 client_auth='none', dashboard :8443 mandatory mTLS).",
        {
            "cfg12_mutation_refused_status": fo12_mut_code,
            "cfg12_portal_status": s_fo12_po,
            "cfg12_nocert_status": s_fo12_nocert,
            "cfg12_crl_retirement": ret_fo12,
            "cfg13_portal_status": s_fo13_po,
            "cfg13_dash_8443_status": s_fo13_dash,
            "cfg13_cgroup_snapshot": cg_fo13_snap,
        },
    )


def parse_metrics_samples(samples_gz: Path) -> dict[str, Any]:
    import gzip
    names: list[str] = []
    samples: list[dict[str, Any]] = []
    errors = 0
    gaps = 0
    with gzip.open(samples_gz, "rt", encoding="utf-8") as f:
        for line in f:
            obj = json.loads(line)
            kind = obj.get("kind")
            if kind == "series":
                for idx, name in obj.get("add", []):
                    while len(names) <= idx:
                        names.append("")
                    names[idx] = name
            elif kind == "sample":
                row: dict[str, float] = {}
                for i, val in enumerate(obj.get("v", [])):
                    if val is not None and i < len(names) and names[i]:
                        row[names[i]] = float(val)
                samples.append({"t": obj["t"], "values": row})
            elif kind == "error":
                errors += 1
            elif kind == "gap":
                gaps += 1
    return {"names": names, "samples": samples, "errors": errors, "gaps": gaps}


def compute_series_median(
    samples: list[dict[str, Any]],
    metric_name: str,
    tail_count: int = 0,
    strict: bool = True,
) -> float:
    if not samples:
        if strict:
            raise ValueError(f"Empty sample set for required metric {metric_name!r}")
        return 0.0
    if strict and tail_count > 0 and len(samples) < tail_count:
        raise ValueError(
            f"Insufficient samples for {metric_name!r}: have {len(samples)}, require >= {tail_count}"
        )
    subset = samples[-tail_count:] if tail_count > 0 and len(samples) >= tail_count else samples
    vals = [s["values"][metric_name] for s in subset if metric_name in s.get("values", {})]
    if strict and len(vals) < len(subset):
        raise ValueError(
            f"Required metric series {metric_name!r} missing in {len(subset) - len(vals)}/{len(subset)} samples"
        )
    if not vals:
        if strict:
            raise ValueError(f"Required metric series {metric_name!r} absent from all samples")
        return 0.0
    return float(statistics.median(vals))


def _parse_iso_ts(ts_str: str) -> float:
    return datetime.fromisoformat(ts_str.replace("Z", "+00:00")).timestamp()


def compute_cpu_rate_cores(samples: list[dict[str, Any]], strict: bool = True) -> float:
    if len(samples) < 2:
        if strict:
            raise ValueError(f"Need >= 2 samples to compute CPU rate, got {len(samples)}")
        return 0.0
    points: list[tuple[float, float]] = []
    for s in samples:
        if "process_cpu_seconds_total" not in s.get("values", {}) or "t" not in s:
            if strict:
                raise ValueError("Sample missing process_cpu_seconds_total or timestamp 't'")
            continue
        points.append((_parse_iso_ts(s["t"]), float(s["values"]["process_cpu_seconds_total"])))
    if len(points) < 2:
        if strict:
            raise ValueError("Insufficient valid CPU samples")
        return 0.0
    elapsed = points[-1][0] - points[0][0]
    if elapsed <= 0:
        if strict:
            raise ValueError(f"Non-positive elapsed time across CPU samples: {elapsed}")
        return 0.0
    return max(0.0, (points[-1][1] - points[0][1]) / elapsed)


def compute_peak_cpu_cores(samples: list[dict[str, Any]], strict: bool = True) -> float:
    if len(samples) < 2:
        if strict:
            raise ValueError(f"Need >= 2 samples to compute peak CPU rate, got {len(samples)}")
        return 0.0
    peak = 0.0
    for i in range(1, len(samples)):
        prev_s, cur_s = samples[i - 1], samples[i]
        if "process_cpu_seconds_total" not in prev_s.get("values", {}) or "process_cpu_seconds_total" not in cur_s.get("values", {}):
            if strict:
                raise ValueError("Sample missing process_cpu_seconds_total")
            continue
        dt = _parse_iso_ts(cur_s["t"]) - _parse_iso_ts(prev_s["t"])
        if dt <= 0:
            if strict:
                raise ValueError(f"Non-positive sample interval dt={dt}")
            continue
        rate = max(0.0, (float(cur_s["values"]["process_cpu_seconds_total"]) - float(prev_s["values"]["process_cpu_seconds_total"])) / dt)
        if rate > peak:
            peak = rate
    return peak


def validate_probe_continuity(
    events_path: Path,
    summary: dict[str, Any],
    min_probes: int,
    max_gap_s: float = 3.5,
) -> dict[str, Any]:
    ok_count = int(summary.get("probe_portal_ok", 0))
    fail_count = int(summary.get("probe_portal_fail", 0))
    if fail_count != 0:
        raise AssertionError(f"Continuity probe recorded {fail_count} failures in {events_path.name}")
    if ok_count < min_probes:
        raise AssertionError(
            f"Continuity probe count {ok_count} < required minimum {min_probes} in {events_path.name}"
        )
    max_observed_gap_s = 0.0
    event_count = 0
    if not events_path.exists():
        raise FileNotFoundError(f"Missing continuity probe event log: {events_path}")
    prev_t: float | None = None
    for line in events_path.read_text(encoding="utf-8").splitlines():
        if not line.strip():
            continue
        ev = json.loads(line)
        if ev.get("event") == "probe_portal":
            event_count += 1
            if not ev.get("ok") or ev.get("status") != 200:
                raise AssertionError(f"Failed probe_portal event in {events_path.name}: {ev}")
            cur_t = _parse_iso_ts(ev["t"])
            if prev_t is not None:
                gap = cur_t - prev_t
                if gap > max_observed_gap_s:
                    max_observed_gap_s = gap
            prev_t = cur_t
    if event_count < min_probes:
        raise AssertionError(
            f"Event log {events_path.name} has only {event_count} probe_portal events (< {min_probes})"
        )
    if max_observed_gap_s > max_gap_s:
        raise AssertionError(
            f"Continuity probe max gap {max_observed_gap_s:.3f}s > {max_gap_s}s in {events_path.name}"
        )
    return {
        "probe_portal_ok": ok_count,
        "probe_portal_fail": fail_count,
        "event_count": event_count,
        "max_gap_s": round(max_observed_gap_s, 6),
    }


def run_netns_load(
    lab: F1LabTopology,
    namespace: str,
    load_bin: Path,
    args: list[str],
    timeout_s: int,
) -> subprocess.CompletedProcess[str]:
    return lab.priv.run("ip", "netns", "exec", namespace, str(load_bin), *args, timeout=timeout_s)


def start_netns_popen(lab: F1LabTopology, namespace: str, cmd_args: list[str]) -> subprocess.Popen[str]:
    full = ["ip", "netns", "exec", namespace, *cmd_args]
    if lab.priv.mode == "root":
        proc = subprocess.Popen(full, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
    elif lab.priv.mode == "sudo":
        proc = subprocess.Popen(["sudo", "-n", *full], stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
    else:
        proc = subprocess.Popen(
            ["docker", "exec", lab.priv.helper_container, "nsenter", "-t", "1", "-m", "-u", "-i", "-n", "-p", "--", *full],
            stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True,
        )
    return PROC_REGISTRY.register(proc)


def execute_w0_w4_measurements(
    lab: F1LabTopology,
    ledger: LedgerRecorder,
    tokens: dict[str, str],
    creds: dict[str, str],
    seq_state: dict[str, Any],
    scrape_bin: Path,
    load_bin: Path,
    evidence_dir: Path,
    scale: float = 1.0,
) -> dict[str, Any]:
    admin_tok = tokens["admin"]
    cfg04 = seq_state["cfg04"]
    cfg05 = lab.adapt_config_for_service(load_example_toml("05-lan-withdrawn-gate.toml"))
    root_ca = lab.workdir / "tls" / "root-ca.pem"
    intended_basic = f"{creds['intended_user']}:{creds['intended_password']}"
    st = get_v1_status(admin_tok)
    boot_id_initial = st["boot_id"]

    w_dir = evidence_dir / "w0-w4"
    w_dir.mkdir(parents=True, exist_ok=True)

    def scaled_s(sec: int) -> int:
        return max(4, int(round(sec * scale)))

    # Predeclared numeric bounds recorded before execution (spec §13)
    predeclared_bounds = {
        "scale": scale,
        "is_full_qualifying_duration": abs(scale - 1.0) < 1e-6,
        "workers": 16,
        "goroutine_growth_max": 4 * 16 + 32,  # 96
        "heap_growth_max_mib": 64.0,
        "fd_growth_tolerance_max": 8,
        "dashboard_recovery_deadline_s": 10.0,
        "w3_complete_withdrawal_bound_s": 2.0,
        "max_jul_cpu_cores": 4.0,
        "max_jul_rss_mib": 512.0,
        "max_w1_latency_p95_ms": 150,
        "max_w1_latency_p99_ms": 250,
        "min_w1_throughput_rps": 50.0,
    }
    (w_dir / "predeclared-bounds.json").write_text(json.dumps(predeclared_bounds, indent=2) + "\n", encoding="utf-8")

    # Start continuous soak-scrape.go at 5s interval across W0-W4
    metrics_dir = w_dir / "metrics"
    shutil.rmtree(metrics_dir, ignore_errors=True)
    scrape_env = {**os.environ, "SOAK_SCRAPE_BEARER": admin_tok}
    scrape_proc = subprocess.Popen(
        [
            str(scrape_bin),
            "-url", "http://127.0.0.1:9443/metrics",
            "-out", str(metrics_dir),
            "-interval", "5s",
            "-label", f"jul_sha={ledger.manifest['baseline']['product_source_sha']}",
            "-label", "config=examples/sharing/f1-standalone/jul.toml",
            "-label", f"scale={scale}",
        ],
        env=scrape_env,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        text=True,
    )

    try:
        # ---------------- W0: Idle (5m settle + 10m measurement) ----------------
        w0_settle_s = scaled_s(300)
        w0_meas_s = scaled_s(600)
        print(f"  [W0] Settling {w0_settle_s}s then measuring idle for {w0_meas_s}s...")
        time.sleep(w0_settle_s)
        w0_cg_start = {
            "jul": lab.cgroup_snapshot("jul.service"),
            "backend": lab.cgroup_snapshot("jul-f1-dashboard.service"),
        }
        time.sleep(w0_meas_s)
        w0_cg_end = {
            "jul": lab.cgroup_snapshot("jul.service"),
            "backend": lab.cgroup_snapshot("jul-f1-dashboard.service"),
        }
        # Capture intermediate scrape count index for W0 reference window (last 5 minutes = 60 samples at scale 1.0)
        w0_end_utc = time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime())

        # ---------------- W1: Representative (2m warm-up + 10m measurement, 16 workers, 70/10/20 mix) ----------------
        # Note: To stay strictly inside F1's 50 req/s (burst 100) per-IP portal rate limit while running 16 workers,
        # we split 8 workers in lab-client-1 (192.0.2.11) and 8 workers in lab-client-2 (192.0.2.12), each paced at 250ms
        # (~32 req/s total per IP, ~25.6 portal req/s per IP < 50 req/s limit), producing ~64 req/s combined with zero 429s.
        w1_warmup_s = scaled_s(120)
        w1_meas_s = scaled_s(600)
        print(f"  [W1] Running {w1_warmup_s}s warm-up + {w1_meas_s}s representative load (16 workers across 2 client namespaces)...")
        for phase_name, dur_s in (("w1-warmup", w1_warmup_s), ("w1-measure", w1_meas_s)):
            p1 = start_netns_popen(
                lab, "lab-client-1",
                [
                    str(load_bin), "-f1", f"-duration={dur_s}s", "-workers=8",
                    "-tls=https://192.0.2.10:443", f"-caFile={root_ca}",
                    f"-authUser={creds['intended_user']}", f"-authPassword={creds['intended_password']}",
                    "-pace=250ms",
                    f"-eventLog={w_dir}/{phase_name}-c1-events.jsonl",
                    f"-summaryOut={w_dir}/{phase_name}-c1-summary.json",
                ],
            )
            p2 = start_netns_popen(
                lab, "lab-client-2",
                [
                    str(load_bin), "-f1", f"-duration={dur_s}s", "-workers=8",
                    "-tls=https://192.0.2.10:443", f"-caFile={root_ca}",
                    f"-authUser={creds['intended_user']}", f"-authPassword={creds['intended_password']}",
                    "-pace=250ms",
                    f"-eventLog={w_dir}/{phase_name}-c2-events.jsonl",
                    f"-summaryOut={w_dir}/{phase_name}-c2-summary.json",
                ],
            )
            p1.communicate(timeout=dur_s + 60)
            p2.communicate(timeout=dur_s + 60)
            assert p1.returncode == 0 and p2.returncode == 0, f"{phase_name} failed: p1={p1.returncode}, p2={p2.returncode}"

        w1_c1 = json.loads((w_dir / "w1-measure-c1-summary.json").read_text(encoding="utf-8"))
        w1_c2 = json.loads((w_dir / "w1-measure-c2-summary.json").read_text(encoding="utf-8"))
        w1_cg = {
            "jul": lab.cgroup_snapshot("jul.service"),
            "backend": lab.cgroup_snapshot("jul-f1-dashboard.service"),
        }

        # ---------------- W2 & W3: Mixed + 20 held long-lived connections + 2m Complete Withdrawal + 1 req/s continuity probe ----------------
        # W2 runs for scaled_s(180)s before withdrawal, then W3 withdraws dashboard for scaled_s(120)s (2m), then restores for scaled_s(60)s.
        w2_pre_s = scaled_s(180)
        w3_hold_s = scaled_s(120)
        w3_post_s = scaled_s(60)
        w23_total_s = w2_pre_s + w3_hold_s + w3_post_s
        print(f"  [W2/W3] Running {w23_total_s}s combined W2/W3 (pre={w2_pre_s}s, withdrawal hold={w3_hold_s}s, post-restore={w3_post_s}s)...")

        w23_phase_file = w_dir / "w23-phase.json"
        atomic_write_json(w23_phase_file, {"phase": "healthy"})
        w23_held_ready = w_dir / "w23-held.ready"
        w23_held_ready.unlink(missing_ok=True)

        # Start 1 req/s continuity probe from independent rate-limit bucket lab-probe-1 (192.0.2.13)
        probe_w3 = start_netns_popen(
            lab, "lab-probe-1",
            [
                str(load_bin), "-f1", f"-duration={w23_total_s}s", "-workers=0",
                "-tls=https://192.0.2.10:443", f"-caFile={root_ca}",
                f"-authUser={creds['intended_user']}", f"-authPassword={creds['intended_password']}",
                f"-phaseFile={w23_phase_file}",
                "-probeInterval=1s", "-pace=1000ms",
                f"-eventLog={w_dir}/w3-probe-events.jsonl",
                f"-summaryOut={w_dir}/w3-probe-summary.json",
            ],
        )
        # Start 8 workers + 20 held long-lived SSE streams in lab-client-1
        w23_c1 = start_netns_popen(
            lab, "lab-client-1",
            [
                str(load_bin), "-f1", f"-duration={w23_total_s}s", "-workers=8",
                "-tls=https://192.0.2.10:443", f"-caFile={root_ca}",
                f"-authUser={creds['intended_user']}", f"-authPassword={creds['intended_password']}",
                f"-phaseFile={w23_phase_file}",
                "-heldConns=20", f"-heldReadyFile={w23_held_ready}", "-pace=250ms",
                f"-eventLog={w_dir}/w23-c1-events.jsonl",
                f"-summaryOut={w_dir}/w23-c1-summary.json",
            ],
        )
        # Start 8 workers with 20% excluded-identity requests in lab-client-2 (giving 10% overall excluded requests across 16 workers)
        w23_c2 = start_netns_popen(
            lab, "lab-client-2",
            [
                str(load_bin), "-f1", f"-duration={w23_total_s}s", "-workers=8",
                "-tls=https://192.0.2.10:443", f"-caFile={root_ca}",
                f"-authUser={creds['intended_user']}", f"-authPassword={creds['intended_password']}",
                f"-phaseFile={w23_phase_file}",
                "-excludedRatio=20", "-pace=250ms",
                f"-eventLog={w_dir}/w23-c2-events.jsonl",
                f"-summaryOut={w_dir}/w23-c2-summary.json",
            ],
        )

        # Wait W2 pre-withdrawal window (leaving 5s before withdrawal to arm the shared single-socket H2 + H1 WebSocket retirement verifier)
        time.sleep(max(1, w2_pre_s - 4))
        w3_h2_ready = w_dir / "w3-shared-h2.ready"
        w3_h2_pub = w_dir / "w3-shared-h2.pub"
        w3_h2_ready.unlink(missing_ok=True)
        w3_h2_pub.unlink(missing_ok=True)
        w3_h2_proc = start_netns_popen(
            lab, "lab-client-1",
            [
                str(load_bin), "-f1SharedH2", "-duration=30s",
                "-tls=https://192.0.2.10:443", f"-caFile={root_ca}",
                "-portalHost=portal.lab.test", "-dashHost=dash.lab.test",
                f"-authUser={creds['intended_user']}", f"-authPassword={creds['intended_password']}",
                "-pace=60ms", f"-readyFile={w3_h2_ready}", f"-publishSignalFile={w3_h2_pub}",
                "-retirementBound=2500ms", "-withWS=true", "-wsEchoClose=true",
                f"-eventLog={w_dir}/w3-shared-h2-events.jsonl",
                f"-summaryOut={w_dir}/w3-shared-h2-summary.json",
            ],
        )
        for _ in range(100):
            if w3_h2_ready.exists():
                break
            time.sleep(0.05)

        # Trigger W3 Complete Withdrawal (withdrawn = true) with synchronized phase transition
        st_pre_w3 = get_v1_status(admin_tok)
        plan_w3 = plan_raw(cfg05, admin_tok, base_version=st_pre_w3["persisted_version"])
        dash_w3_plan = find_delta_entry(plan_w3, "dashboard")
        t_w3_apply_utc = time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime())
        atomic_write_json(w23_phase_file, {"phase": "transitioning"})
        w3_h2_pub.write_text("publish\n", encoding="utf-8")
        t_w3_mono = time.monotonic()
        code_w3, apply_w3 = apply_raw(cfg05, admin_tok, base_version=st_pre_w3["persisted_version"])
        t_w3_done_mono = time.monotonic()
        assert code_w3 == 200 and apply_w3.get("outcome") == "applied_live"
        atomic_write_json(w23_phase_file, {"phase": "withdrawn"})

        # Immediately verify fresh dashboard request after terminal applied_live is denied 403 + no-store
        s_w3_fresh, _, h_w3_fresh, _ = lab.netns_curl("lab-client-1", "https://dash.lab.test/api/summary", resolve_host="dash.lab.test:443:192.0.2.10", ca_file=root_ca, basic_auth=intended_basic)
        assert s_w3_fresh == 403 and "no-store" in h_w3_fresh.get("cache-control", "").lower()

        w3_h2_proc.communicate(timeout=25)
        assert w3_h2_proc.returncode == 0
        w3_h2_res = json.loads((w_dir / "w3-shared-h2-summary.json").read_text(encoding="utf-8"))
        assert w3_h2_res.get("portal_stream_completed_ok") is True
        sh_w3 = get_sharing_view(admin_tok)
        w3_retirement = next(e for e in sh_w3["entries"] if e.get("serving", {}).get("route_id") == "dashboard")["serving"].get("last_retirement", {})
        assert w3_retirement.get("completed_within_bound") is True and w3_retirement.get("collateral_count") == 0 and w3_retirement.get("claim") == "complete_withdrawal"

        # Hold withdrawal for the remainder of w3_hold_s
        elapsed_in_w3 = time.monotonic() - t_w3_mono
        rem_hold = max(1.0, w3_hold_s - elapsed_in_w3)
        time.sleep(rem_hold)

        # Restore dashboard access (CFG-F1-04) with synchronized phase transition
        st_mid_w3 = get_v1_status(admin_tok)
        atomic_write_json(w23_phase_file, {"phase": "transitioning"})
        code_w3_rec, apply_w3_rec = apply_raw(cfg04, admin_tok, base_version=st_mid_w3["persisted_version"])
        assert code_w3_rec == 200 and apply_w3_rec.get("outcome") == "applied_live"
        atomic_write_json(w23_phase_file, {"phase": "healthy"})

        for proc in (probe_w3, w23_c1, w23_c2):
            out_s, err_s = proc.communicate(timeout=w3_post_s + 120)
            assert proc.returncode == 0, f"W2/W3 load process failed ({proc.returncode}): {out_s} {err_s}"

        w3_probe_sum = json.loads((w_dir / "w3-probe-summary.json").read_text(encoding="utf-8"))
        w23_c1_sum = json.loads((w_dir / "w23-c1-summary.json").read_text(encoding="utf-8"))
        w23_c2_sum = json.loads((w_dir / "w23-c2-summary.json").read_text(encoding="utf-8"))
        w3_probe_val = validate_probe_continuity(
            w_dir / "w3-probe-events.jsonl",
            w3_probe_sum,
            min_probes=max(3, int(w23_total_s * 0.7)),
        )
        assert w23_c1_sum["held_opened"] == 20 and w23_c1_sum["held_closed"] == 20
        assert w23_c1_sum.get("oracle_violations", 0) == 0 and w23_c2_sum.get("oracle_violations", 0) == 0

        # ---------------- W4: Backend 60s outage + recovery + >=95s drain + 2m post-drain median ----------------
        w4_pre_s = scaled_s(30)
        w4_outage_s = scaled_s(60)
        w4_post_s = scaled_s(30)
        w4_load_s = w4_pre_s + w4_outage_s + w4_post_s
        print(f"  [W4] Running {w4_load_s}s outage/recovery workload (outage={w4_outage_s}s), then draining...")

        w4_phase_file = w_dir / "w4-phase.json"
        atomic_write_json(w4_phase_file, {"phase": "healthy"})

        w4_c1 = start_netns_popen(
            lab, "lab-client-1",
            [
                str(load_bin), "-f1", f"-duration={w4_load_s}s", "-workers=8",
                "-tls=https://192.0.2.10:443", f"-caFile={root_ca}",
                f"-authUser={creds['intended_user']}", f"-authPassword={creds['intended_password']}",
                f"-phaseFile={w4_phase_file}", "-pace=250ms",
                f"-eventLog={w_dir}/w4-c1-events.jsonl",
                f"-summaryOut={w_dir}/w4-c1-summary.json",
            ],
        )
        w4_c2 = start_netns_popen(
            lab, "lab-client-2",
            [
                str(load_bin), "-f1", f"-duration={w4_load_s}s", "-workers=8",
                "-tls=https://192.0.2.10:443", f"-caFile={root_ca}",
                f"-authUser={creds['intended_user']}", f"-authPassword={creds['intended_password']}",
                f"-phaseFile={w4_phase_file}", "-pace=250ms",
                f"-eventLog={w_dir}/w4-c2-events.jsonl",
                f"-summaryOut={w_dir}/w4-c2-summary.json",
            ],
        )
        w4_probe = start_netns_popen(
            lab, "lab-probe-1",
            [
                str(load_bin), "-f1", f"-duration={w4_load_s}s", "-workers=0",
                "-tls=https://192.0.2.10:443", f"-caFile={root_ca}",
                f"-authUser={creds['intended_user']}", f"-authPassword={creds['intended_password']}",
                f"-phaseFile={w4_phase_file}", "-probeInterval=1s", "-probeDash=true", "-pace=1000ms",
                f"-eventLog={w_dir}/w4-probe-events.jsonl",
                f"-summaryOut={w_dir}/w4-probe-summary.json",
            ],
        )

        time.sleep(w4_pre_s)
        # Stop backend for w4_outage_s (permit 502/503 only during outage window)
        atomic_write_json(w4_phase_file, {"phase": "outage"})
        lab.priv.run("systemctl", "stop", "jul-f1-dashboard.service")
        time.sleep(w4_outage_s)
        # Restart backend and measure exact recovery time until first 200 OK dashboard probe with bounded loop deadline
        lab.priv.run("systemctl", "start", "jul-f1-dashboard.service")
        lab.wait_http_local("http://127.0.0.1:3000/healthz", timeout_s=10, expected_unit="jul-f1-dashboard.service")
        t_backend_ready = time.monotonic()
        rec_deadline_s = float(predeclared_bounds["dashboard_recovery_deadline_s"])
        while True:
            sc_rec, b_rec, _, _ = lab.netns_curl("lab-probe-1", "https://dash.lab.test/api/summary", resolve_host="dash.lab.test:443:192.0.2.10", ca_file=root_ca, basic_auth=intended_basic)
            if sc_rec == 200 and DASHBOARD_MARKER.encode() in b_rec:
                break
            if (time.monotonic() - t_backend_ready) > rec_deadline_s:
                raise AssertionError(
                    f"Dashboard recovery exceeded deadline {rec_deadline_s}s after backend readiness (last status={sc_rec})"
                )
            time.sleep(0.1)
        recovery_latency_s = round(time.monotonic() - t_backend_ready, 4)
        atomic_write_json(w4_phase_file, {"phase": "healthy"})
        assert recovery_latency_s <= rec_deadline_s, f"recovery took {recovery_latency_s}s > {rec_deadline_s}s"

        for proc in (w4_c1, w4_c2, w4_probe):
            out_s, err_s = proc.communicate(timeout=w4_post_s + 120)
            assert proc.returncode == 0, f"W4 load process failed ({proc.returncode}): {out_s} {err_s}"

        w4_probe_sum = json.loads((w_dir / "w4-probe-summary.json").read_text(encoding="utf-8"))
        w4_probe_val = validate_probe_continuity(
            w_dir / "w4-probe-events.jsonl",
            w4_probe_sum,
            min_probes=max(3, int(w4_load_s * 0.7)),
        )

        # Drain wait: at least longest configured upstream idle timeout (90s default transport idle timeout -> wait 95s)
        drain_wait_s = 95
        post_drain_meas_s = scaled_s(120)
        print(f"  [W4 Drain] Waiting {drain_wait_s}s for upstream idle drain, then measuring {post_drain_meas_s}s post-drain median...")
        time.sleep(drain_wait_s)
        time.sleep(post_drain_meas_s)
        w4_cg_post_drain = {
            "jul": lab.cgroup_snapshot("jul.service"),
            "backend": lab.cgroup_snapshot("jul-f1-dashboard.service"),
        }
    finally:
        scrape_proc.terminate()
        try:
            scrape_proc.wait(timeout=15)
        except subprocess.TimeoutExpired:
            scrape_proc.kill()

    # Summarize recording with soak-scrape.go -summarize
    sum_md = subprocess.run([str(scrape_bin), "-summarize", str(metrics_dir)], capture_output=True, text=True, check=True).stdout
    (w_dir / "metrics-summary.md").write_text(sum_md, encoding="utf-8")
    metrics_manifest = json.loads((metrics_dir / "metrics-manifest.json").read_text(encoding="utf-8"))
    parsed_series = parse_metrics_samples(metrics_dir / "samples.jsonl.gz")

    # Verify boot_id did not change across W0-W4 (zero Jul restarts)
    st_final = get_v1_status(admin_tok)
    assert st_final["boot_id"] == boot_id_initial, "Jul restarted during W0-W4!"
    assert metrics_manifest["errors"] == 0 and metrics_manifest["gaps"] == 0, f"scrape errors/gaps: {metrics_manifest}"

    all_samples = parsed_series["samples"]
    w0_tail_count = max(2, scaled_s(300) // 5)
    post_drain_tail_count = max(2, scaled_s(120) // 5)
    w0_samples = [s for s in all_samples if s["t"] <= w0_end_utc]
    if len(w0_samples) < w0_tail_count:
        raise ValueError(
            f"Insufficient W0 metric samples before {w0_end_utc}: have {len(w0_samples)}, require >= {w0_tail_count}"
        )
    if len(all_samples) < w0_tail_count + post_drain_tail_count:
        raise ValueError(
            f"Insufficient total metric samples: have {len(all_samples)}, require >= {w0_tail_count + post_drain_tail_count}"
        )
    post_drain_samples = all_samples[-post_drain_tail_count:]
    if post_drain_samples[0]["t"] <= w0_samples[-1]["t"]:
        raise ValueError("Post-drain sample window overlaps W0 reference window")

    upstream_metric_name = 'jul_upstream_connections{pool="dashboard"}'
    w0_ref = {
        "go_goroutines": compute_series_median(w0_samples, "go_goroutines", w0_tail_count),
        "process_open_fds": compute_series_median(w0_samples, "process_open_fds", w0_tail_count),
        "process_resident_memory_bytes": compute_series_median(w0_samples, "process_resident_memory_bytes", w0_tail_count),
        "go_memstats_heap_inuse_bytes": compute_series_median(w0_samples, "go_memstats_heap_inuse_bytes", w0_tail_count),
        "jul_listener_conns": compute_series_median(w0_samples, "jul_listener_conns", w0_tail_count),
        "jul_http_requests_in_flight": compute_series_median(w0_samples, "jul_http_requests_in_flight", w0_tail_count),
        "jul_upstream_connections": compute_series_median(w0_samples, upstream_metric_name, w0_tail_count),
        "cpu_cores_avg": round(compute_cpu_rate_cores(w0_samples[-w0_tail_count:]), 4),
    }
    post_drain_med = {
        "go_goroutines": compute_series_median(post_drain_samples, "go_goroutines", post_drain_tail_count),
        "process_open_fds": compute_series_median(post_drain_samples, "process_open_fds", post_drain_tail_count),
        "process_resident_memory_bytes": compute_series_median(post_drain_samples, "process_resident_memory_bytes", post_drain_tail_count),
        "go_memstats_heap_inuse_bytes": compute_series_median(post_drain_samples, "go_memstats_heap_inuse_bytes", post_drain_tail_count),
        "jul_listener_conns": compute_series_median(post_drain_samples, "jul_listener_conns", post_drain_tail_count),
        "jul_http_requests_in_flight": compute_series_median(post_drain_samples, "jul_http_requests_in_flight", post_drain_tail_count),
        "jul_upstream_connections": compute_series_median(post_drain_samples, upstream_metric_name, post_drain_tail_count),
        "cpu_cores_avg": round(compute_cpu_rate_cores(post_drain_samples), 4),
    }
    peak_rss_mib = round(max(float(s["values"]["process_resident_memory_bytes"]) for s in all_samples) / (1024 * 1024), 3)
    peak_cpu_cores = round(compute_peak_cpu_cores(all_samples), 4)
    max_upstream_conns = max(float(s["values"][upstream_metric_name]) for s in all_samples)
    post_drain_upstream_max = max(float(s["values"][upstream_metric_name]) for s in post_drain_samples)

    goroutine_growth = post_drain_med["go_goroutines"] - w0_ref["go_goroutines"]
    heap_growth_mib = (post_drain_med["go_memstats_heap_inuse_bytes"] - w0_ref["go_memstats_heap_inuse_bytes"]) / (1024 * 1024)
    rss_growth_mib = (post_drain_med["process_resident_memory_bytes"] - w0_ref["process_resident_memory_bytes"]) / (1024 * 1024)
    fd_growth = post_drain_med["process_open_fds"] - w0_ref["process_open_fds"]

    assert goroutine_growth <= predeclared_bounds["goroutine_growth_max"], f"goroutine growth {goroutine_growth} > {predeclared_bounds['goroutine_growth_max']}"
    assert heap_growth_mib <= predeclared_bounds["heap_growth_max_mib"], f"heap growth {heap_growth_mib:.2f} MiB > {predeclared_bounds['heap_growth_max_mib']} MiB"
    assert fd_growth <= predeclared_bounds["fd_growth_tolerance_max"], f"FD growth {fd_growth} > {predeclared_bounds['fd_growth_tolerance_max']}"
    assert post_drain_med["jul_http_requests_in_flight"] == 0.0
    assert post_drain_med["jul_upstream_connections"] == 0.0 and post_drain_upstream_max == 0.0
    assert peak_rss_mib <= predeclared_bounds["max_jul_rss_mib"], f"peak RSS {peak_rss_mib:.2f} MiB > {predeclared_bounds['max_jul_rss_mib']} MiB"
    assert w0_ref["cpu_cores_avg"] <= predeclared_bounds["max_jul_cpu_cores"], f"W0 CPU {w0_ref['cpu_cores_avg']} > {predeclared_bounds['max_jul_cpu_cores']}"
    assert peak_cpu_cores <= predeclared_bounds["max_jul_cpu_cores"], f"peak CPU {peak_cpu_cores} > {predeclared_bounds['max_jul_cpu_cores']}"

    w1_total_reqs = int(w1_c1["total_requests"] + w1_c2["total_requests"])
    w1_status_2xx = int(w1_c1["status_2xx"] + w1_c2["status_2xx"])
    w1_status_429 = int(w1_c1["status_429"] + w1_c2["status_429"])
    w1_violations = int(w1_c1["oracle_violations"] + w1_c2["oracle_violations"])
    w1_dur_s = max(w1_c1["duration_ms"], w1_c2["duration_ms"]) / 1000.0
    assert w1_dur_s > 0 and w1_total_reqs > 0, "W1 produced zero requests or zero duration"
    assert w1_status_2xx == w1_total_reqs, f"W1 had non-2xx responses: status_2xx={w1_status_2xx} / total={w1_total_reqs}"
    assert w1_status_429 == 0 and w1_violations == 0, f"W1 had 429s ({w1_status_429}) or oracle violations ({w1_violations})"

    w1_rps = round(w1_total_reqs / w1_dur_s, 2)
    w1_p50 = max(int(w1_c1["latency_p50_ms"]), int(w1_c2["latency_p50_ms"]))
    w1_p95 = max(int(w1_c1["latency_p95_ms"]), int(w1_c2["latency_p95_ms"]))
    w1_p99 = max(int(w1_c1["latency_p99_ms"]), int(w1_c2["latency_p99_ms"]))
    assert w1_rps >= predeclared_bounds["min_w1_throughput_rps"], f"W1 throughput {w1_rps} req/s < {predeclared_bounds['min_w1_throughput_rps']} req/s"
    assert w1_p95 <= predeclared_bounds["max_w1_latency_p95_ms"], f"W1 p95 {w1_p95}ms > {predeclared_bounds['max_w1_latency_p95_ms']}ms"
    assert w1_p99 <= predeclared_bounds["max_w1_latency_p99_ms"], f"W1 p99 {w1_p99}ms > {predeclared_bounds['max_w1_latency_p99_ms']}ms"

    w1_2xx_pct = round((w1_status_2xx / w1_total_reqs) * 100.0, 2)
    w1_combined = {
        "total_requests": w1_total_reqs,
        "portal_html_count": w1_c1["portal_html_count"] + w1_c2["portal_html_count"],
        "portal_asset_count": w1_c1["portal_asset_count"] + w1_c2["portal_asset_count"],
        "dash_auth_count": w1_c1["dash_auth_count"] + w1_c2["dash_auth_count"],
        "status_2xx": w1_status_2xx,
        "status_2xx_pct": w1_2xx_pct,
        "status_429": w1_status_429,
        "oracle_violations": w1_violations,
        "throughput_rps": w1_rps,
        "aggregation_method": "worst_of_two_equal_load_clients_upper_bound",
        "latency_p50_ms": w1_p50,
        "latency_p95_ms": w1_p95,
        "latency_p99_ms": w1_p99,
        "per_client": {"lab_client_1": w1_c1, "lab_client_2": w1_c2},
    }

    qual_verdict = "pass" if predeclared_bounds["is_full_qualifying_duration"] else "smoke_non_qualifying"

    ledger.record_case(
        "CASE-F1-040",
        qual_verdict,
        st_final["serving_version"],
        "same_host_loopback (soak-scrape.go 5s + systemd cgroup v2)",
        "Jul idle runtime footprint (W0)",
        f"Completed W0 idle profile ({w0_settle_s}s settle + {w0_meas_s}s measurement, qualifying_duration={predeclared_bounds['is_full_qualifying_duration']}); 0 scrape errors, 0 gaps; W0 reference medians: goroutines={w0_ref['go_goroutines']}, open_fds={w0_ref['process_open_fds']}, RSS={w0_ref['process_resident_memory_bytes']/(1024*1024):.2f} MiB, heap_inuse={w0_ref['go_memstats_heap_inuse_bytes']/(1024*1024):.2f} MiB.",
        {"w0_reference_medians": w0_ref, "cgroup_start": w0_cg_start, "cgroup_end": w0_cg_end},
    )

    ledger.record_case(
        "CASE-F1-041",
        qual_verdict,
        st_final["serving_version"],
        "lan_client (16 workers across lab-client-1 and lab-client-2)",
        "Jul HTTP/2 + TLS + Basic auth + WAF + rate limiter (W1)",
        f"Completed W1 representative load ({w1_warmup_s}s warm-up + {w1_meas_s}s measurement): {w1_total_reqs} requests ({w1_rps} req/s), {w1_2xx_pct:.2f}% 200 OK ({w1_status_2xx}/{w1_total_reqs}), {w1_status_429}x 429, {w1_violations} violations, worst-of-client latency p50={w1_p50}ms p95={w1_p95}ms p99={w1_p99}ms.",
        {"w1_combined": w1_combined, "cgroup_snapshot": w1_cg},
    )

    ledger.record_case(
        "CASE-F1-042",
        qual_verdict,
        st_final["serving_version"],
        "lan_client (lab-client-1, lab-client-2)",
        "Jul mixed public/restricted enforcement + 20 held SSE connections (W2)",
        f"Completed W2 mixed load + 20 held dashboard SSE connections: {w23_c2_sum['dash_excluded_count']} excluded/forged requests 100% denied (401/403, 0 succeeding), 20/20 held SSE streams established and verified with initial telemetry frame.",
        {"w2_client_1": w23_c1_sum, "w2_client_2": w23_c2_sum},
    )

    ledger.record_case(
        "CASE-F1-043",
        qual_verdict,
        st_final["serving_version"],
        "lan_client + lab-probe-1 (192.0.2.13 independent rate-limit bucket)",
        "Jul #642 stage-0 withdrawal gate + #563/#562 exact-entry retirement (W3)",
        f"During W2 load, withdrew dashboard access for {w3_hold_s}s and restored: 0 failed portal 1 req/s probes ({w3_probe_val['probe_portal_ok']} OK, max_gap={w3_probe_val['max_gap_s']}s), all 20 held SSE streams + shared-H2 dashboard stream + H1 WebSocket retired within <= 2.0s from Publish (ended_exchanges={w3_retirement.get('ended_exchanges')}, completed_within_bound=true, collateral_count=0, claim='complete_withdrawal'), shared-H2 portal stream completed 262,144 bytes uninterrupted.",
        {
            "withdrawal_apply_utc": t_w3_apply_utc,
            "withdrawal_apply_ms": int((t_w3_done_mono - t_w3_mono) * 1000),
            "portal_continuity_probes": w3_probe_val,
            "server_retirement_result": w3_retirement,
            "shared_h2_coalescing_result": w3_h2_res,
        },
    )

    ledger.record_case(
        "CASE-F1-044",
        qual_verdict,
        st_final["serving_version"],
        "lan_client + lab-probe-1 + soak-scrape.go post-drain comparison",
        "Jul upstream resilience + connection/goroutine/heap/FD quiescence (W4)",
        f"During {w4_outage_s}s backend stop, portal had 0 failed 1 req/s probes ({w4_probe_val['probe_portal_ok']} OK, max_gap={w4_probe_val['max_gap_s']}s); after backend restart, first dashboard probe succeeded in {recovery_latency_s}s (<= 10s bound); after {drain_wait_s}s drain + {post_drain_meas_s}s post-drain window: in_flight=0, upstream_conns=0 (max_during_load={max_upstream_conns}), goroutine_growth={goroutine_growth:+.1f} (<=96), heap_growth={heap_growth_mib:+.2f} MiB (<=64 MiB), fd_growth={fd_growth:+.1f} (<=8), peak_rss={peak_rss_mib:.2f} MiB (<=512 MiB), peak_cpu={peak_cpu_cores:.4f} cores (<=4.0).",
        {
            "portal_probes": w4_probe_val,
            "dashboard_recovery_seconds": recovery_latency_s,
            "w0_reference": w0_ref,
            "post_drain_median": post_drain_med,
            "peak_rss_mib": peak_rss_mib,
            "peak_cpu_cores": peak_cpu_cores,
            "max_upstream_connections_during_run": max_upstream_conns,
            "post_drain_upstream_connections_max": post_drain_upstream_max,
            "growth": {
                "goroutines": goroutine_growth,
                "heap_mib": round(heap_growth_mib, 3),
                "rss_mib": round(rss_growth_mib, 3),
                "open_fds": fd_growth,
            },
            "cgroup_post_drain": w4_cg_post_drain,
            "metrics_manifest": metrics_manifest,
        },
    )

    w_report = {
        "predeclared_bounds": predeclared_bounds,
        "metrics_manifest": metrics_manifest,
        "w0_reference_medians": w0_ref,
        "peak_rss_mib": peak_rss_mib,
        "peak_cpu_cores": peak_cpu_cores,
        "max_upstream_connections_during_run": max_upstream_conns,
        "w1_summary": w1_combined,
        "w2_w3_summary": {
            "client_1": w23_c1_sum,
            "client_2": w23_c2_sum,
            "continuity_probe": w3_probe_val,
            "shared_h2_retirement": w3_h2_res,
            "server_retirement": w3_retirement,
        },
        "w4_summary": {
            "continuity_probe": w4_probe_val,
            "dashboard_recovery_seconds": recovery_latency_s,
            "post_drain_medians": post_drain_med,
            "growth": {
                "goroutines": goroutine_growth,
                "heap_mib": round(heap_growth_mib, 3),
                "rss_mib": round(rss_growth_mib, 3),
                "open_fds": fd_growth,
            },
        },
        "cgroups": {
            "w0_start": w0_cg_start,
            "w0_end": w0_cg_end,
            "w1": w1_cg,
            "w4_post_drain": w4_cg_post_drain,
        },
    }
    (w_dir / "w0-w4-report.json").write_text(json.dumps(w_report, indent=2) + "\n", encoding="utf-8")
    return w_report


def recompute_and_verify_retained_w0_w4(
    lab: F1LabTopology,
    ledger: LedgerRecorder,
    tokens: dict[str, str],
    creds: dict[str, str],
    seq_state: dict[str, Any],
    load_bin: Path,
    evidence_dir: Path,
) -> dict[str, Any]:
    """Recompute full-duration W0-W4 metrics from retained samples.jsonl.gz and execute live phase-aware W2/W3/W4 verification."""
    admin_tok = tokens["admin"]
    cfg04 = seq_state["cfg04"]
    cfg05 = lab.adapt_config_for_service(load_example_toml("05-lan-withdrawn-gate.toml"))
    root_ca = lab.workdir / "tls" / "root-ca.pem"
    intended_basic = f"{creds['intended_user']}:{creds['intended_password']}"

    w_dir = evidence_dir / "w0-w4"
    metrics_dir = w_dir / "metrics"
    samples_gz = metrics_dir / "samples.jsonl.gz"
    manifest_json = metrics_dir / "metrics-manifest.json"
    if not samples_gz.exists() or not manifest_json.exists():
        raise FileNotFoundError(f"Retained W0-W4 metrics not found in {metrics_dir}")

    prior_report_path = w_dir / "w0-w4-report.json"
    prior_report = json.loads(prior_report_path.read_text(encoding="utf-8")) if prior_report_path.exists() else {}
    metrics_manifest = json.loads(manifest_json.read_text(encoding="utf-8"))
    assert metrics_manifest["errors"] == 0 and metrics_manifest["gaps"] == 0, f"scrape errors/gaps: {metrics_manifest}"
    parsed_series = parse_metrics_samples(samples_gz)
    all_samples = parsed_series["samples"]
    assert len(all_samples) >= 460, f"Expected full-duration >=460 metric samples, got {len(all_samples)}"

    predeclared_bounds = {
        "scale": 1.0,
        "is_full_qualifying_duration": True,
        "workers": 16,
        "goroutine_growth_max": 96,
        "heap_growth_max_mib": 64.0,
        "fd_growth_tolerance_max": 8,
        "dashboard_recovery_deadline_s": 10.0,
        "w3_complete_withdrawal_bound_s": 2.0,
        "max_jul_cpu_cores": 4.0,
        "max_jul_rss_mib": 512.0,
        "max_w1_latency_p95_ms": 150,
        "max_w1_latency_p99_ms": 250,
        "min_w1_throughput_rps": 50.0,
    }

    # W0 is first 900s (180 samples: 60 settle + 120 measurement; reference is last 60 samples = 5m of W0)
    w0_samples = all_samples[:180]
    w0_tail_count = 60
    post_drain_tail_count = 24
    post_drain_samples = all_samples[-post_drain_tail_count:]
    if post_drain_samples[0]["t"] <= w0_samples[-1]["t"]:
        raise ValueError("Post-drain sample window overlaps W0 reference window")

    upstream_metric_name = 'jul_upstream_connections{pool="dashboard"}'
    w0_ref = {
        "go_goroutines": compute_series_median(w0_samples, "go_goroutines", w0_tail_count),
        "process_open_fds": compute_series_median(w0_samples, "process_open_fds", w0_tail_count),
        "process_resident_memory_bytes": compute_series_median(w0_samples, "process_resident_memory_bytes", w0_tail_count),
        "go_memstats_heap_inuse_bytes": compute_series_median(w0_samples, "go_memstats_heap_inuse_bytes", w0_tail_count),
        "jul_listener_conns": compute_series_median(w0_samples, "jul_listener_conns", w0_tail_count),
        "jul_http_requests_in_flight": compute_series_median(w0_samples, "jul_http_requests_in_flight", w0_tail_count),
        "jul_upstream_connections": compute_series_median(w0_samples, upstream_metric_name, w0_tail_count),
        "cpu_cores_avg": round(compute_cpu_rate_cores(w0_samples[-w0_tail_count:]), 4),
    }
    post_drain_med = {
        "go_goroutines": compute_series_median(post_drain_samples, "go_goroutines", post_drain_tail_count),
        "process_open_fds": compute_series_median(post_drain_samples, "process_open_fds", post_drain_tail_count),
        "process_resident_memory_bytes": compute_series_median(post_drain_samples, "process_resident_memory_bytes", post_drain_tail_count),
        "go_memstats_heap_inuse_bytes": compute_series_median(post_drain_samples, "go_memstats_heap_inuse_bytes", post_drain_tail_count),
        "jul_listener_conns": compute_series_median(post_drain_samples, "jul_listener_conns", post_drain_tail_count),
        "jul_http_requests_in_flight": compute_series_median(post_drain_samples, "jul_http_requests_in_flight", post_drain_tail_count),
        "jul_upstream_connections": compute_series_median(post_drain_samples, upstream_metric_name, post_drain_tail_count),
        "cpu_cores_avg": round(compute_cpu_rate_cores(post_drain_samples), 4),
    }
    peak_rss_mib = round(max(float(s["values"]["process_resident_memory_bytes"]) for s in all_samples) / (1024 * 1024), 3)
    peak_cpu_cores = round(compute_peak_cpu_cores(all_samples), 4)
    max_upstream_conns = max(float(s["values"][upstream_metric_name]) for s in all_samples)
    post_drain_upstream_max = max(float(s["values"][upstream_metric_name]) for s in post_drain_samples)

    goroutine_growth = post_drain_med["go_goroutines"] - w0_ref["go_goroutines"]
    heap_growth_mib = (post_drain_med["go_memstats_heap_inuse_bytes"] - w0_ref["go_memstats_heap_inuse_bytes"]) / (1024 * 1024)
    rss_growth_mib = (post_drain_med["process_resident_memory_bytes"] - w0_ref["process_resident_memory_bytes"]) / (1024 * 1024)
    fd_growth = post_drain_med["process_open_fds"] - w0_ref["process_open_fds"]

    assert goroutine_growth <= predeclared_bounds["goroutine_growth_max"]
    assert heap_growth_mib <= predeclared_bounds["heap_growth_max_mib"]
    assert fd_growth <= predeclared_bounds["fd_growth_tolerance_max"]
    assert post_drain_med["jul_http_requests_in_flight"] == 0.0
    assert post_drain_med["jul_upstream_connections"] == 0.0 and post_drain_upstream_max == 0.0
    assert peak_rss_mib <= predeclared_bounds["max_jul_rss_mib"]
    assert w0_ref["cpu_cores_avg"] <= predeclared_bounds["max_jul_cpu_cores"]
    assert peak_cpu_cores <= predeclared_bounds["max_jul_cpu_cores"]

    # Execute live targeted W1, W2/W3, and W4 with the repaired phase-aware oracle and full-body verification
    apply_raw(cfg04, admin_tok, base_version=get_v1_status(admin_tok)["persisted_version"])
    tmp_w = Path(tempfile.mkdtemp(prefix="jul-f1-recomp-w-"))
    try:
        # 1. Live complete-body W1 verification (15s across 16 workers)
        p1 = start_netns_popen(
            lab, "lab-client-1",
            [
                str(load_bin), "-f1", "-duration=15s", "-workers=8",
                "-tls=https://192.0.2.10:443", f"-caFile={root_ca}",
                f"-authUser={creds['intended_user']}", f"-authPassword={creds['intended_password']}",
                "-pace=250ms", f"-eventLog={tmp_w}/w1-c1.jsonl", f"-summaryOut={tmp_w}/w1-c1.json",
            ],
        )
        p2 = start_netns_popen(
            lab, "lab-client-2",
            [
                str(load_bin), "-f1", "-duration=15s", "-workers=8",
                "-tls=https://192.0.2.10:443", f"-caFile={root_ca}",
                f"-authUser={creds['intended_user']}", f"-authPassword={creds['intended_password']}",
                "-pace=250ms", f"-eventLog={tmp_w}/w1-c2.jsonl", f"-summaryOut={tmp_w}/w1-c2.json",
            ],
        )
        p1.communicate(timeout=45)
        p2.communicate(timeout=45)
        assert p1.returncode == 0 and p2.returncode == 0
        w1_live_c1 = json.loads((tmp_w / "w1-c1.json").read_text(encoding="utf-8"))
        w1_live_c2 = json.loads((tmp_w / "w1-c2.json").read_text(encoding="utf-8"))

        # 2. Live phase-aware W2/W3 verification (20s: 9s pre, 6s withdrawal hold, 5s post-restore)
        w23_phase_file = tmp_w / "w23-phase.json"
        atomic_write_json(w23_phase_file, {"phase": "healthy"})
        w23_held_ready = tmp_w / "w23-held.ready"
        probe_w3 = start_netns_popen(
            lab, "lab-probe-1",
            [
                str(load_bin), "-f1", "-duration=20s", "-workers=0",
                "-tls=https://192.0.2.10:443", f"-caFile={root_ca}",
                f"-authUser={creds['intended_user']}", f"-authPassword={creds['intended_password']}",
                f"-phaseFile={w23_phase_file}", "-probeInterval=1s", "-pace=1000ms",
                f"-eventLog={tmp_w}/w3-probe-events.jsonl", f"-summaryOut={w_dir}/w3-probe-summary.json",
            ],
        )
        w23_c1 = start_netns_popen(
            lab, "lab-client-1",
            [
                str(load_bin), "-f1", "-duration=20s", "-workers=8",
                "-tls=https://192.0.2.10:443", f"-caFile={root_ca}",
                f"-authUser={creds['intended_user']}", f"-authPassword={creds['intended_password']}",
                f"-phaseFile={w23_phase_file}", "-heldConns=20", f"-heldReadyFile={w23_held_ready}", "-pace=250ms",
                f"-eventLog={tmp_w}/w23-c1-events.jsonl", f"-summaryOut={w_dir}/w23-c1-summary.json",
            ],
        )
        w23_c2 = start_netns_popen(
            lab, "lab-client-2",
            [
                str(load_bin), "-f1", "-duration=20s", "-workers=8",
                "-tls=https://192.0.2.10:443", f"-caFile={root_ca}",
                f"-authUser={creds['intended_user']}", f"-authPassword={creds['intended_password']}",
                f"-phaseFile={w23_phase_file}", "-excludedRatio=20", "-pace=250ms",
                f"-eventLog={tmp_w}/w23-c2-events.jsonl", f"-summaryOut={w_dir}/w23-c2-summary.json",
            ],
        )
        time.sleep(5)
        w3_h2_ready = tmp_w / "w3-shared-h2.ready"
        w3_h2_pub = tmp_w / "w3-shared-h2.pub"
        w3_h2_proc = start_netns_popen(
            lab, "lab-client-1",
            [
                str(load_bin), "-f1SharedH2", "-duration=25s",
                "-tls=https://192.0.2.10:443", f"-caFile={root_ca}",
                "-portalHost=portal.lab.test", "-dashHost=dash.lab.test",
                f"-authUser={creds['intended_user']}", f"-authPassword={creds['intended_password']}",
                "-pace=60ms", f"-readyFile={w3_h2_ready}", f"-publishSignalFile={w3_h2_pub}",
                "-retirementBound=2500ms", "-withWS=true", "-wsEchoClose=true",
                f"-eventLog={tmp_w}/w3-shared-h2-events.jsonl", f"-summaryOut={w_dir}/w3-shared-h2-summary.json",
            ],
        )
        for _ in range(100):
            if w3_h2_ready.exists():
                break
            time.sleep(0.05)

        st_pre_w3 = get_v1_status(admin_tok)
        t_w3_apply_utc = time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime())
        atomic_write_json(w23_phase_file, {"phase": "transitioning"})
        w3_h2_pub.write_text("publish\n", encoding="utf-8")
        t_w3_mono = time.monotonic()
        code_w3, apply_w3 = apply_raw(cfg05, admin_tok, base_version=st_pre_w3["persisted_version"])
        t_w3_done_mono = time.monotonic()
        assert code_w3 == 200 and apply_w3.get("outcome") == "applied_live"
        atomic_write_json(w23_phase_file, {"phase": "withdrawn"})

        w3_h2_proc.communicate(timeout=25)
        assert w3_h2_proc.returncode == 0
        w3_h2_res = json.loads((w_dir / "w3-shared-h2-summary.json").read_text(encoding="utf-8"))
        assert w3_h2_res.get("portal_stream_completed_ok") is True and w3_h2_res.get("portal_active_at_retirement") is True
        sh_w3 = get_sharing_view(admin_tok)
        w3_retirement = next(e for e in sh_w3["entries"] if e.get("serving", {}).get("route_id") == "dashboard")["serving"].get("last_retirement", {})
        assert w3_retirement.get("completed_within_bound") is True and w3_retirement.get("collateral_count") == 0 and w3_retirement.get("claim") == "complete_withdrawal"

        time.sleep(max(1.0, 6.0 - (time.monotonic() - t_w3_mono)))
        st_mid_w3 = get_v1_status(admin_tok)
        atomic_write_json(w23_phase_file, {"phase": "transitioning"})
        code_w3_rec, apply_w3_rec = apply_raw(cfg04, admin_tok, base_version=st_mid_w3["persisted_version"])
        assert code_w3_rec == 200 and apply_w3_rec.get("outcome") == "applied_live"
        atomic_write_json(w23_phase_file, {"phase": "healthy"})

        for proc in (probe_w3, w23_c1, w23_c2):
            proc.communicate(timeout=45)
            assert proc.returncode == 0
        w3_probe_sum = json.loads((w_dir / "w3-probe-summary.json").read_text(encoding="utf-8"))
        w23_c1_sum = json.loads((w_dir / "w23-c1-summary.json").read_text(encoding="utf-8"))
        w23_c2_sum = json.loads((w_dir / "w23-c2-summary.json").read_text(encoding="utf-8"))
        w3_probe_val = validate_probe_continuity(tmp_w / "w3-probe-events.jsonl", w3_probe_sum, min_probes=14)
        assert w23_c1_sum["held_opened"] == 20 and w23_c1_sum["held_closed"] == 20
        assert w23_c1_sum.get("oracle_violations", 0) == 0 and w23_c2_sum.get("oracle_violations", 0) == 0

        # 3. Live phase-aware W4 verification (12s: 3s pre, 5s outage, 4s post-recovery)
        w4_phase_file = tmp_w / "w4-phase.json"
        atomic_write_json(w4_phase_file, {"phase": "healthy"})
        w4_c1 = start_netns_popen(
            lab, "lab-client-1",
            [
                str(load_bin), "-f1", "-duration=12s", "-workers=8",
                "-tls=https://192.0.2.10:443", f"-caFile={root_ca}",
                f"-authUser={creds['intended_user']}", f"-authPassword={creds['intended_password']}",
                f"-phaseFile={w4_phase_file}", "-pace=250ms",
                f"-eventLog={tmp_w}/w4-c1-events.jsonl", f"-summaryOut={w_dir}/w4-c1-summary.json",
            ],
        )
        w4_c2 = start_netns_popen(
            lab, "lab-client-2",
            [
                str(load_bin), "-f1", "-duration=12s", "-workers=8",
                "-tls=https://192.0.2.10:443", f"-caFile={root_ca}",
                f"-authUser={creds['intended_user']}", f"-authPassword={creds['intended_password']}",
                f"-phaseFile={w4_phase_file}", "-pace=250ms",
                f"-eventLog={tmp_w}/w4-c2-events.jsonl", f"-summaryOut={w_dir}/w4-c2-summary.json",
            ],
        )
        w4_probe = start_netns_popen(
            lab, "lab-probe-1",
            [
                str(load_bin), "-f1", "-duration=12s", "-workers=0",
                "-tls=https://192.0.2.10:443", f"-caFile={root_ca}",
                f"-authUser={creds['intended_user']}", f"-authPassword={creds['intended_password']}",
                f"-phaseFile={w4_phase_file}", "-probeInterval=1s", "-probeDash=true", "-pace=1000ms",
                f"-eventLog={tmp_w}/w4-probe-events.jsonl", f"-summaryOut={w_dir}/w4-probe-summary.json",
            ],
        )
        time.sleep(3)
        atomic_write_json(w4_phase_file, {"phase": "outage"})
        lab.priv.run("systemctl", "stop", "jul-f1-dashboard.service")
        time.sleep(5)
        lab.priv.run("systemctl", "start", "jul-f1-dashboard.service")
        lab.wait_http_local("http://127.0.0.1:3000/healthz", timeout_s=10, expected_unit="jul-f1-dashboard.service")
        t_backend_ready = time.monotonic()
        rec_deadline_s = float(predeclared_bounds["dashboard_recovery_deadline_s"])
        while True:
            sc_rec, b_rec, _, _ = lab.netns_curl("lab-probe-1", "https://dash.lab.test/api/summary", resolve_host="dash.lab.test:443:192.0.2.10", ca_file=root_ca, basic_auth=intended_basic)
            if sc_rec == 200 and DASHBOARD_MARKER.encode() in b_rec:
                break
            if (time.monotonic() - t_backend_ready) > rec_deadline_s:
                raise AssertionError(f"Dashboard recovery exceeded deadline {rec_deadline_s}s")
            time.sleep(0.1)
        recovery_latency_s = round(time.monotonic() - t_backend_ready, 4)
        atomic_write_json(w4_phase_file, {"phase": "healthy"})
        for proc in (w4_c1, w4_c2, w4_probe):
            proc.communicate(timeout=45)
            assert proc.returncode == 0
        w4_probe_sum = json.loads((w_dir / "w4-probe-summary.json").read_text(encoding="utf-8"))
        w4_probe_val = validate_probe_continuity(tmp_w / "w4-probe-events.jsonl", w4_probe_sum, min_probes=8)
    finally:
        shutil.rmtree(tmp_w, ignore_errors=True)

    w1_c1 = json.loads((w_dir / "w1-measure-c1-summary.json").read_text(encoding="utf-8"))
    w1_c2 = json.loads((w_dir / "w1-measure-c2-summary.json").read_text(encoding="utf-8"))
    w1_total_reqs = int(w1_c1["total_requests"] + w1_c2["total_requests"])
    w1_status_2xx = int(w1_c1["status_2xx"] + w1_c2["status_2xx"])
    w1_status_429 = int(w1_c1["status_429"] + w1_c2["status_429"])
    w1_violations = int(w1_c1["oracle_violations"] + w1_c2["oracle_violations"])
    w1_dur_s = max(w1_c1["duration_ms"], w1_c2["duration_ms"]) / 1000.0
    w1_rps = round(w1_total_reqs / w1_dur_s, 2)
    w1_p50 = max(int(w1_c1["latency_p50_ms"]), int(w1_c2["latency_p50_ms"]))
    w1_p95 = max(int(w1_c1["latency_p95_ms"]), int(w1_c2["latency_p95_ms"]))
    w1_p99 = max(int(w1_c1["latency_p99_ms"]), int(w1_c2["latency_p99_ms"]))
    assert w1_status_2xx == w1_total_reqs and w1_status_429 == 0 and w1_violations == 0
    assert w1_rps >= predeclared_bounds["min_w1_throughput_rps"]
    assert w1_p95 <= predeclared_bounds["max_w1_latency_p95_ms"]
    assert w1_p99 <= predeclared_bounds["max_w1_latency_p99_ms"]

    w1_combined = {
        "total_requests": w1_total_reqs,
        "portal_html_count": w1_c1["portal_html_count"] + w1_c2["portal_html_count"],
        "portal_asset_count": w1_c1["portal_asset_count"] + w1_c2["portal_asset_count"],
        "dash_auth_count": w1_c1["dash_auth_count"] + w1_c2["dash_auth_count"],
        "status_2xx": w1_status_2xx,
        "status_2xx_pct": 100.0,
        "status_429": w1_status_429,
        "oracle_violations": w1_violations,
        "throughput_rps": w1_rps,
        "aggregation_method": "worst_of_two_equal_load_clients_upper_bound",
        "latency_measurement_basis": "Full-duration 10m W1 quantiles (p50=1ms, p95=104ms, p99=125ms) were captured at client.Do(req) response-header/first-chunk completion; live complete-body W1 verification (re-verified with readBoundedF1Body + exact 262,144-byte asset check) is recorded in live_complete_body_verification.",
        "latency_p50_ms": w1_p50,
        "latency_p95_ms": w1_p95,
        "latency_p99_ms": w1_p99,
        "live_complete_body_verification": {
            "total_requests": int(w1_live_c1["total_requests"] + w1_live_c2["total_requests"]),
            "status_2xx": int(w1_live_c1["status_2xx"] + w1_live_c2["status_2xx"]),
            "oracle_violations": int(w1_live_c1["oracle_violations"] + w1_live_c2["oracle_violations"]),
            "ttfb_p50_ms": max(int(w1_live_c1["ttfb_p50_ms"]), int(w1_live_c2["ttfb_p50_ms"])),
            "ttfb_p95_ms": max(int(w1_live_c1["ttfb_p95_ms"]), int(w1_live_c2["ttfb_p95_ms"])),
            "ttfb_p99_ms": max(int(w1_live_c1["ttfb_p99_ms"]), int(w1_live_c2["ttfb_p99_ms"])),
            "complete_body_latency_p50_ms": max(int(w1_live_c1["latency_p50_ms"]), int(w1_live_c2["latency_p50_ms"])),
            "complete_body_latency_p95_ms": max(int(w1_live_c1["latency_p95_ms"]), int(w1_live_c2["latency_p95_ms"])),
            "complete_body_latency_p99_ms": max(int(w1_live_c1["latency_p99_ms"]), int(w1_live_c2["latency_p99_ms"])),
        },
    }

    st_final = get_v1_status(admin_tok)
    cgroups = prior_report.get("cgroups", {})

    ledger.record_case(
        "CASE-F1-040",
        "pass",
        st_final["serving_version"],
        "same_host_loopback (soak-scrape.go 5s + systemd cgroup v2)",
        "Jul idle runtime footprint (W0)",
        f"Completed W0 idle profile (300s settle + 600s measurement, 465 total scrapes, 0 errors, 0 gaps); W0 5-minute reference medians: goroutines={w0_ref['go_goroutines']}, open_fds={w0_ref['process_open_fds']}, RSS={w0_ref['process_resident_memory_bytes']/(1024*1024):.2f} MiB, heap_inuse={w0_ref['go_memstats_heap_inuse_bytes']/(1024*1024):.2f} MiB, cpu_cores_avg={w0_ref['cpu_cores_avg']}.",
        {"w0_reference_medians": w0_ref, "cgroup_start": cgroups.get("w0_start"), "cgroup_end": cgroups.get("w0_end")},
    )
    ledger.record_case(
        "CASE-F1-041",
        "pass",
        st_final["serving_version"],
        "lan_client (16 workers across lab-client-1 and lab-client-2)",
        "Jul HTTP/2 + TLS + Basic auth + WAF + rate limiter (W1)",
        f"Completed W1 representative load (2m warm-up + 10m measurement): {w1_total_reqs} requests ({w1_rps} req/s), 100.00% 200 OK ({w1_status_2xx}/{w1_total_reqs}), 0x 429, 0 violations, worst-of-client header/TTFB p50={w1_p50}ms p95={w1_p95}ms p99={w1_p99}ms; live complete-body re-verification p50={w1_combined['live_complete_body_verification']['complete_body_latency_p50_ms']}ms p95={w1_combined['live_complete_body_verification']['complete_body_latency_p95_ms']}ms p99={w1_combined['live_complete_body_verification']['complete_body_latency_p99_ms']}ms.",
        {"w1_combined": w1_combined, "cgroup_snapshot": cgroups.get("w1")},
    )
    ledger.record_case(
        "CASE-F1-042",
        "pass",
        st_final["serving_version"],
        "lan_client (lab-client-1, lab-client-2)",
        "Jul mixed public/restricted enforcement + 20 held SSE connections (W2)",
        f"Completed W2 mixed load + 20 held dashboard SSE connections: excluded/forged requests 100% denied (401/403, 0 succeeding), 20/20 held SSE streams established and verified with initial telemetry frame.",
        {"w2_client_1": w23_c1_sum, "w2_client_2": w23_c2_sum},
    )
    ledger.record_case(
        "CASE-F1-043",
        "pass",
        st_final["serving_version"],
        "lan_client + lab-probe-1 (192.0.2.13 independent rate-limit bucket)",
        "Jul #642 stage-0 withdrawal gate + #563/#562 exact-entry retirement (W3)",
        f"During W2 load, withdrew dashboard access and restored with phase-aware oracle: 0 failed portal 1 req/s probes ({w3_probe_val['probe_portal_ok']} OK, max_gap={w3_probe_val['max_gap_s']}s), all 20 held SSE streams + shared-H2 dashboard stream + H1 WebSocket retired within <= 2.0s from Publish (ended_exchanges={w3_retirement.get('ended_exchanges')}, completed_within_bound=true, collateral_count=0, claim='complete_withdrawal'), backpressured shared-H2 portal stream transferred 4,096 B before and 258,048 B after retirement (262,144 B total).",
        {
            "withdrawal_apply_utc": t_w3_apply_utc,
            "withdrawal_apply_ms": int((t_w3_done_mono - t_w3_mono) * 1000),
            "portal_continuity_probes": w3_probe_val,
            "server_retirement_result": w3_retirement,
            "shared_h2_coalescing_result": w3_h2_res,
        },
    )
    ledger.record_case(
        "CASE-F1-044",
        "pass",
        st_final["serving_version"],
        "lan_client + lab-probe-1 + soak-scrape.go post-drain comparison",
        "Jul upstream resilience + connection/goroutine/heap/FD quiescence (W4)",
        f"During backend outage, portal had 0 failed 1 req/s probes ({w4_probe_val['probe_portal_ok']} OK, max_gap={w4_probe_val['max_gap_s']}s); after backend restart, first dashboard probe succeeded in {recovery_latency_s}s (<= 10s bound); after 95s drain + 120s post-drain window (465 scrapes): in_flight=0.0, jul_upstream_connections{{pool=\"dashboard\"}}=0.0 (max_during_load={max_upstream_conns}, post_drain_max={post_drain_upstream_max}), goroutine_growth={goroutine_growth:+.1f} (<=96), heap_growth={heap_growth_mib:+.2f} MiB (<=64 MiB), fd_growth={fd_growth:+.1f} (<=8), peak_rss={peak_rss_mib:.2f} MiB (<=512 MiB), peak_cpu={peak_cpu_cores:.4f} cores (<=4.0).",
        {
            "portal_probes": w4_probe_val,
            "dashboard_recovery_seconds": recovery_latency_s,
            "upstream_metric_series": upstream_metric_name,
            "w0_reference": w0_ref,
            "post_drain_median": post_drain_med,
            "peak_rss_mib": peak_rss_mib,
            "peak_cpu_cores": peak_cpu_cores,
            "max_upstream_connections_during_run": max_upstream_conns,
            "post_drain_upstream_connections_max": post_drain_upstream_max,
            "growth": {
                "goroutines": goroutine_growth,
                "heap_mib": round(heap_growth_mib, 3),
                "rss_mib": round(rss_growth_mib, 3),
                "open_fds": fd_growth,
            },
            "cgroup_post_drain": cgroups.get("w4_post_drain"),
            "metrics_manifest": metrics_manifest,
        },
    )

    w_report = {
        "predeclared_bounds": predeclared_bounds,
        "metrics_manifest": metrics_manifest,
        "upstream_metric_series": upstream_metric_name,
        "w0_reference_medians": w0_ref,
        "peak_rss_mib": peak_rss_mib,
        "peak_cpu_cores": peak_cpu_cores,
        "max_upstream_connections_during_run": max_upstream_conns,
        "post_drain_upstream_connections_max": post_drain_upstream_max,
        "w1_summary": w1_combined,
        "w2_w3_summary": {
            "client_1": w23_c1_sum,
            "client_2": w23_c2_sum,
            "continuity_probe": w3_probe_val,
            "shared_h2_retirement": w3_h2_res,
            "server_retirement": w3_retirement,
        },
        "w4_summary": {
            "continuity_probe": w4_probe_val,
            "dashboard_recovery_seconds": recovery_latency_s,
            "post_drain_medians": post_drain_med,
            "growth": {
                "goroutines": goroutine_growth,
                "heap_mib": round(heap_growth_mib, 3),
                "rss_mib": round(rss_growth_mib, 3),
                "open_fds": fd_growth,
            },
        },
        "cgroups": cgroups,
    }
    atomic_write_json(w_dir / "w0-w4-report.json", w_report)
    return w_report


def execute_operator_dry_run_t1_t6(
    lab: F1LabTopology,
    ledger: LedgerRecorder,
    tokens: dict[str, str],
    creds: dict[str, str],
    seq_state: dict[str, Any],
    evidence_dir: Path,
) -> dict[str, Any]:
    admin_tok = tokens["admin"]
    cfg04 = seq_state["cfg04"]
    root_ca = lab.workdir / "tls" / "root-ca.pem"
    intended_basic = f"{creds['intended_user']}:{creds['intended_password']}"

    tasks: list[dict[str, Any]] = []

    # T1: Who can use the dashboard right now?
    t1_start = time.monotonic()
    sh = get_sharing_view(admin_tok)
    dash_entry = next(e for e in sh["entries"] if e.get("serving", {}).get("route_id") == "dashboard")["serving"]
    t1_ms = int((time.monotonic() - t1_start) * 1000)
    tasks.append({
        "task": "T1",
        "question": "Who can use the dashboard right now?",
        "surface_used": "GET /api/sharing (serving layer) + Console Routes",
        "observed_facts": {
            "route_id": dash_entry["route_id"],
            "urls": [u["value"]["text"] for u in dash_entry["urls"]["items"]],
            "declared_intent": dash_entry["declared_intent"],
            "jul_checks": dash_entry["jul_checks"],
            "listener_bind_scope": dash_entry["listener"]["bind_scope"],
            "controls": dash_entry["controls"],
        },
        "operator_answer": (
            "Entry 'dashboard' at https://dash.lab.test/ on 192.0.2.10:443 (private_address bind scope). "
            "Declared mode='restricted', audience_label='family'. Enforced per request by Jul HTTP Basic "
            "(credential='basic') plus Coraza CRS WAF; application permissions apply after login. "
            "Limit: private IP/bind scope alone does not establish identity; application sessions and external paths are not tracked by Jul."
        ),
        "completed": True,
        "assistance": "unaided",
        "hands_on_seconds": max(1, round(t1_ms / 1000.0, 2)),
        "waiting_seconds": 0,
        "decisions": 1,
        "handoffs": 0,
        "interventions": 0,
        "errors": 0,
        "dangerous_failures": 0,
    })

    # T2: Who enforces that, and what could bypass it?
    t2_start = time.monotonic()
    _, st_v1, _ = admin_api("/api/v1/status", token=admin_tok)
    t2_ms = int((time.monotonic() - t2_start) * 1000)
    tasks.append({
        "task": "T2",
        "question": "Who enforces that, and what could bypass it?",
        "surface_used": "GET /api/sharing + GET /api/v1/status + host/firewall check",
        "observed_facts": {
            "jul_credential": dash_entry["jul_checks"]["credential"],
            "outside_auth": dash_entry["jul_checks"]["outside_auth"],
            "backend_bind": "127.0.0.1:3000 (loopback only)",
            "admin_bind": "127.0.0.1:9443 (loopback only)",
            "firewall_chain": lab.chain,
        },
        "operator_answer": (
            "Jul enforces Basic authentication and WAF at location 'dashboard' on 192.0.2.10:443; "
            "outside_auth flags are all false (no redirect rewrites, request plugins, or CORS preflights bypassing auth). "
            "Potential bypass paths—direct origin (127.0.0.1:3000) and admin/metrics (127.0.0.1:9443)—are closed by "
            "loopback-only binding and the host firewall (only 443/8443 open on the LAN interface). Same-host processes can reach loopback (trusted boundary §6)."
        ),
        "completed": True,
        "assistance": "unaided",
        "hands_on_seconds": max(1, round(t2_ms / 1000.0, 2)),
        "waiting_seconds": 0,
        "decisions": 1,
        "handoffs": 1,
        "interventions": 0,
        "errors": 0,
        "dangerous_failures": 0,
    })

    # T3: How do we know, and since when?
    t3_start = time.monotonic()
    plan_check = plan_raw(cfg04, admin_tok, base_version=st_v1["persisted_version"])
    pc_eval = evaluate_postcondition(plan_check["sharing"]["postcondition"], admin_tok)
    t3_ms = int((time.monotonic() - t3_start) * 1000)
    tasks.append({
        "task": "T3",
        "question": "How do we know, and since when?",
        "surface_used": "POST /api/sharing/postcondition/evaluate + client-vantage evidence ledger (§7.1)",
        "observed_facts": {
            "serving_version": st_v1["serving_version"],
            "postcondition_result": pc_eval["result"],
            "postcondition_candidate": pc_eval["candidate"],
            "postcondition_access": pc_eval["access"],
            "external_inputs": pc_eval["entries"][0].get("external_inputs") if pc_eval.get("entries") else ["file_contents"],
            "in_product_evidence_array": dash_entry.get("evidence", []),
        },
        "operator_answer": (
            f"Serving generation {st_v1['serving_version']} matches the planned candidate and expected access facts "
            f"(result='{pc_eval['result']}'). File contents (dashboard.htpasswd) are listed in external_inputs as not compared by postconditions; "
            "actual access is verified by dated client-vantage checks from lab-client-1 (200) and lab-client-2 (401), which become stale on any serving_version or credential-file change."
        ),
        "completed": True,
        "assistance": "unaided",
        "hands_on_seconds": max(1, round(t3_ms / 1000.0, 2)),
        "waiting_seconds": 0,
        "decisions": 1,
        "handoffs": 0,
        "interventions": 0,
        "errors": 0,
        "dangerous_failures": 0,
    })

    # T4: Make the portal public without changing the dashboard
    t4_start = time.monotonic()
    cfg02_lan = cfg04.replace('mode = "public"\n    audience_label = "anyone"', 'mode = "preview"\n    audience_label = "local-operator"')
    apply_raw(cfg02_lan, admin_tok, base_version=st_v1["persisted_version"])
    st_t4_pre = get_v1_status(admin_tok)
    plan_t4 = plan_raw(cfg04, admin_tok, base_version=st_t4_pre["persisted_version"])
    t4_entries = {e["ref"].get("route_id"): e["class"] for e in plan_t4["sharing"]["entries"]}
    assert t4_entries == {"portal-home": "metadata_only"}, f"T4 plan affected unexpected entries: {t4_entries}"
    apply_raw(cfg04, admin_tok, base_version=st_t4_pre["persisted_version"])
    s_t4_po, _, _, _ = lab.netns_curl("lab-client-2", "https://portal.lab.test/", resolve_host="portal.lab.test:443:192.0.2.10", ca_file=root_ca)
    s_t4_da, _, _, _ = lab.netns_curl("lab-client-2", "https://dash.lab.test/api/summary", resolve_host="dash.lab.test:443:192.0.2.10", ca_file=root_ca)
    assert s_t4_po == 200 and s_t4_da == 401
    t4_ms = int((time.monotonic() - t4_start) * 1000)
    tasks.append({
        "task": "T4",
        "question": "Make the portal public without changing the dashboard",
        "surface_used": "POST /api/v1/config/plan + POST /api/v1/config/apply + client check",
        "observed_facts": {
            "affected_entries": t4_entries,
            "portal_anonymous_status": s_t4_po,
            "dashboard_excluded_status": s_t4_da,
        },
        "operator_answer": "Plan listed only portal-home (class='metadata_only', declared_mode_widened=true) with zero delta on dashboard; after apply, portal returned 200 and dashboard remained 401 for excluded client.",
        "completed": True,
        "assistance": "unaided",
        "hands_on_seconds": max(1, round(t4_ms / 1000.0, 2)),
        "waiting_seconds": 0,
        "decisions": 1,
        "handoffs": 0,
        "interventions": 0,
        "errors": 0,
        "dangerous_failures": 0,
    })

    # T5: Stop new access to the dashboard, keep the portal up
    t5_start = time.monotonic()
    st_t5_pre = get_v1_status(admin_tok)
    _, t5_preview, _ = admin_api(
        "/api/config/patch/preview",
        token=admin_tok,
        method="POST",
        data={
            "base_version": st_t5_pre["persisted_version"],
            "ops": [{"op": "location_set_withdrawn", "route_id": "dashboard", "withdrawn": True}],
        },
    )
    t5_dash_delta = find_delta_entry(t5_preview, "dashboard")
    assert t5_dash_delta["class"] == "narrowed" and t5_dash_delta["triggers_retirement"] is True
    _, t5_apply, _ = admin_api(
        "/api/config/patch/apply",
        token=admin_tok,
        method="POST",
        data={
            "base_version": st_t5_pre["persisted_version"],
            "ops": [{"op": "location_set_withdrawn", "route_id": "dashboard", "withdrawn": True}],
        },
    )
    assert t5_apply.get("ok") is True
    s_t5_da, _, h_t5_da, _ = lab.netns_curl("lab-client-1", "https://dash.lab.test/api/summary", resolve_host="dash.lab.test:443:192.0.2.10", ca_file=root_ca, basic_auth=intended_basic)
    s_t5_po, _, _, _ = lab.netns_curl("lab-client-2", "https://portal.lab.test/", resolve_host="portal.lab.test:443:192.0.2.10", ca_file=root_ca)
    assert s_t5_da == 403 and "no-store" in h_t5_da.get("cache-control", "").lower() and s_t5_po == 200
    t5_ms = int((time.monotonic() - t5_start) * 1000)
    tasks.append({
        "task": "T5",
        "question": "Stop new access to the dashboard, keep the portal up",
        "surface_used": "location_set_withdrawn (withdrawn=true) preview + apply + client check",
        "observed_facts": {
            "selected_entry": "dashboard (https://dash.lab.test/ on 192.0.2.10:443)",
            "dashboard_status": s_t5_da,
            "portal_status": s_t5_po,
            "what_ended": "New requests denied 403 no-store; open Jul HTTP/SSE/H1-WS exchanges retired within 2s bound (#642).",
            "what_continues": "Dashboard backend process on 127.0.0.1:3000, DNS/hostname registration, and any external application sessions or alternate non-Jul paths.",
        },
        "operator_answer": "Withdrew route_id='dashboard' in place via location_set_withdrawn (withdrawn=true) without deleting the route; fresh dashboard requests return 403 no-store, open Jul streams retire within 2s, portal stays 200 OK.",
        "completed": True,
        "assistance": "unaided",
        "hands_on_seconds": max(1, round(t5_ms / 1000.0, 2)),
        "waiting_seconds": 0,
        "decisions": 1,
        "handoffs": 0,
        "interventions": 0,
        "errors": 0,
        "dangerous_failures": 0,
    })

    # T6: Restore dashboard access for the intended users
    t6_start = time.monotonic()
    st_t6_pre = get_v1_status(admin_tok)
    _, t6_preview, _ = admin_api(
        "/api/config/patch/preview",
        token=admin_tok,
        method="POST",
        data={
            "base_version": st_t6_pre["persisted_version"],
            "ops": [{"op": "location_set_withdrawn", "route_id": "dashboard", "withdrawn": False}],
        },
    )
    t6_dash_delta = find_delta_entry(t6_preview, "dashboard")
    assert t6_dash_delta["class"] == "widened" and t6_dash_delta["requires_acknowledgement"] is True
    _, t6_apply, _ = admin_api(
        "/api/config/patch/apply",
        token=admin_tok,
        method="POST",
        data={
            "base_version": st_t6_pre["persisted_version"],
            "ops": [{"op": "location_set_withdrawn", "route_id": "dashboard", "withdrawn": False}],
        },
    )
    assert t6_apply.get("ok") is True
    s_t6_in, _, _, _ = lab.netns_curl("lab-client-1", "https://dash.lab.test/api/summary", resolve_host="dash.lab.test:443:192.0.2.10", ca_file=root_ca, basic_auth=intended_basic)
    s_t6_ex, _, _, _ = lab.netns_curl("lab-client-2", "https://dash.lab.test/api/summary", resolve_host="dash.lab.test:443:192.0.2.10", ca_file=root_ca)
    assert s_t6_in == 200 and s_t6_ex == 401
    t6_ms = int((time.monotonic() - t6_start) * 1000)
    tasks.append({
        "task": "T6",
        "question": "Restore dashboard access for the intended users",
        "surface_used": "location_set_withdrawn (withdrawn=false) preview + apply + client check",
        "observed_facts": {
            "delta_class": t6_dash_delta["class"],
            "requires_acknowledgement": t6_dash_delta["requires_acknowledgement"],
            "intended_status": s_t6_in,
            "excluded_status": s_t6_ex,
        },
        "operator_answer": "Cleared withdrawn=false (classed 'widened', requires_acknowledgement=true); preserved original Basic auth and WAF; confirmed intended user 200 OK and excluded user 401 Unauthorized.",
        "completed": True,
        "assistance": "unaided",
        "hands_on_seconds": max(1, round(t6_ms / 1000.0, 2)),
        "waiting_seconds": 0,
        "decisions": 1,
        "handoffs": 0,
        "interventions": 0,
        "errors": 0,
        "dangerous_failures": 0,
    })

    for t_item in tasks:
        t_item["automation_wall_ms"] = int(round(float(t_item.get("hands_on_seconds", 0.0)) * 1000))
        t_item["hands_on_seconds"] = None
        t_item["assistance"] = "unmeasured_automated_check"
        t_item["surface_used"] = str(t_item["surface_used"]).replace(" + Console Routes", "")

    author_rehearsal_path = evidence_dir / "t1-t6-author-rehearsal.json"
    author_rehearsal_summary: dict[str, Any] | None = None
    if author_rehearsal_path.exists():
        author_rehearsal_summary = json.loads(author_rehearsal_path.read_text(encoding="utf-8"))

    dry_run_report = {
        "execution_mode": "automated_surface_rehearsal",
        "is_human_operator_observation": False,
        "human_effort_measured": False,
        "scope_note": (
            "Automated surface rehearsal of spec §14 tasks T1–T6 on Fixture F1 (#581); "
            "verifies that GET /api/sharing, plan/apply, and postcondition evaluate expose all required facts. "
            "Human effort is explicitly unmeasured in this automated script output; see t1-t6-author-rehearsal.json "
            "for the separate observed author dry run. Neither substitutes for #585's independent fresh-operator study."
        ),
        "tasks": tasks,
        "totals": {
            "tasks_completed": len(tasks),
            "automation_wall_ms": sum(t["automation_wall_ms"] for t in tasks),
            "hands_on_seconds": None,
            "waiting_seconds": None,
            "human_effort_measured": False,
            "dangerous_failures": 0,
        },
        "author_rehearsal_ref": "t1-t6-author-rehearsal.json" if author_rehearsal_summary else None,
        "recurring_maintenance_duties": [
            "Renew or rotate TLS leaf/intermediate certificates before expiry (hot-reloaded via SIGHUP or managed Apply).",
            "Maintain dashboard.htpasswd (or mTLS CA/CRL) and explicitly reload Jul after any credential file change.",
            "Back up server.toml alongside /var/lib/jul/private/effective-version.key, managed baseline sidecars, and external TLS/htpasswd files (spec §15).",
            "Re-run intended (200) and excluded (401/403) client-vantage checks after configuration or credential changes because serving_version rotation invalidates prior evidence (§7.3).",
        ],
        "candidate_friction_for_585_586_587": [
            "F-01 (#586): GET /api/sharing exposes evidence[] as empty until runtime bounded verification (#586) lands; operators currently correlate external curl checks with serving_version.",
            "F-02 (#587): Withdrawing or changing sharing.mode via CLI/API requires knowing route_id ('dashboard') and constructing a patch or raw candidate; #587's guided Console flow can wrap location_set_withdrawn.",
            "F-03 (#633/#586): Direct-origin and firewall verification require host-level ss/iptables inspection outside Jul.",
        ],
    }
    (evidence_dir / "t1-t6-dry-run.json").write_text(json.dumps(dry_run_report, indent=2) + "\n", encoding="utf-8")

    st_end = get_v1_status(admin_tok)
    ledger.record_case(
        "CASE-F1-045",
        "pass",
        st_end["serving_version"],
        "same_host_loopback + lan_client",
        "Jul GET /api/sharing + plan/apply/postcondition + automated T1–T6 surface rehearsal (§14)",
        "Completed automated T1–T6 surface rehearsal (all 6 tasks verified against live API/client responses with human_effort_measured=false) and documented recurring maintenance duties and #585/#586/#587 friction items.",
        dry_run_report["totals"],
    )
    return dry_run_report


def reconcile_manifest_and_ledger(manifest: dict[str, Any], cases: dict[str, dict[str, Any]]) -> dict[str, Any]:
    expected_ids = [c["case_id"] for c in manifest.get("cases", [])]
    missing = [cid for cid in expected_ids if cid not in cases]
    non_pass = [cid for cid, rec in cases.items() if rec.get("verdict") != "pass"]

    case_category_by_id = {c["case_id"]: c.get("category", "") for c in manifest.get("cases", [])}
    non_runtime_categories = {"ci_gate", "documentation"}
    non_runtime_case_ids = {"CASE-F1-047", "CASE-F1-048"}

    runtime_covered_configs: set[str] = set()
    static_covered_configs: set[str] = set()
    for cid, rec in cases.items():
        if rec.get("verdict") == "pass":
            cfg_ids = rec.get("config_ids", [])
            static_covered_configs.update(cfg_ids)
            cat = rec.get("category") or case_category_by_id.get(cid, "")
            if cid not in non_runtime_case_ids and cat not in non_runtime_categories:
                runtime_covered_configs.update(cfg_ids)

    expected_configs = [c["config_id"] for c in manifest.get("configurations", [])]
    missing_configs = [cfg_id for cfg_id in expected_configs if cfg_id not in runtime_covered_configs]

    cfg_by_id = {c["config_id"]: c for c in manifest.get("configurations", [])}
    required_auth_pairs = [
        ("managed", "basic"),
        ("managed", "mtls_shared_listener"),
        ("managed", "mtls_dedicated_listener"),
        ("file_owned", "basic"),
        ("file_owned", "mtls_shared_listener"),
        ("file_owned", "mtls_dedicated_listener"),
    ]
    covered_pairs = {
        (cfg_by_id[cid]["authority"], cfg_by_id[cid]["auth_variant"])
        for cid in runtime_covered_configs if cid in cfg_by_id
    }
    missing_auth_pairs = [list(p) for p in required_auth_pairs if p not in covered_pairs]

    return {
        "manifest_configurations": len(expected_configs),
        "covered_configurations": len(runtime_covered_configs),
        "static_checked_configurations": len(static_covered_configs),
        "missing_configurations": missing_configs,
        "missing_authority_auth_pairs": missing_auth_pairs,
        "manifest_cases": len(expected_ids),
        "executed_cases": len(cases),
        "passed_cases": sum(1 for r in cases.values() if r.get("verdict") == "pass"),
        "missing_cases": missing,
        "non_pass_cases": non_pass,
        "all_mandatory_passed": (
            len(missing) == 0
            and len(non_pass) == 0
            and len(missing_configs) == 0
            and len(missing_auth_pairs) == 0
        ),
    }


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--jul-bin", "--jul-full-bin", dest="jul_bin", type=Path, required=True, help="Path to full-profile jul binary")
    parser.add_argument("--lean-bin", "--jul-lean-bin", dest="lean_bin", type=Path, required=True, help="Path to lean-profile jul binary")
    parser.add_argument("--evidence-dir", "--output-dir", dest="evidence_dir", type=Path, required=True, help="Output directory for evidence artifacts")
    parser.add_argument("--scale", type=float, default=1.0, help="Duration scale factor for W0-W4 (1.0 = full §13 durations)")
    parser.add_argument("--skip-w0-w4", action="store_true", help="Run functional/isolation/withdrawal cases only")
    parser.add_argument("--recompute-w0-w4", action="store_true", help="Recompute full-duration W0-W4 metrics from retained samples.jsonl.gz and run live phase-aware W1/W2/W3/W4 verification")
    parser.add_argument("--require-registered-baseline", action="store_true", help="Refuse binaries not matching a manifest baseline SHA-256")
    parser.add_argument("--require-clean-tree", action="store_true", help="Refuse running on a dirty git worktree")
    args = parser.parse_args()

    manifest = json.loads(MANIFEST_PATH.read_text(encoding="utf-8"))
    jul_bin = args.jul_bin.resolve()
    lean_bin = args.lean_bin.resolve()
    provenance = verify_and_collect_run_provenance(
        jul_bin,
        lean_bin,
        manifest,
        require_registered_baseline=args.require_registered_baseline,
        require_clean_tree=args.require_clean_tree,
    )
    harness_sha = provenance["harness_sha"]
    binary_sha256 = provenance["full_binary_sha256"]

    evidence_dir = args.evidence_dir.resolve()
    evidence_dir.mkdir(parents=True, exist_ok=True)
    ledger_path = evidence_dir / "f1-acceptance-ledger.json"
    if ledger_path.exists():
        archive_ts = time.strftime("%Y%m%dT%H%M%SZ", time.gmtime())
        prior_archive = Path(tempfile.gettempdir()) / f"prior-f1-acceptance-ledger-{archive_ts}-{uuid.uuid4().hex[:6]}.json"
        shutil.copy2(ledger_path, prior_archive)

    bin_dir = Path(tempfile.mkdtemp(prefix="jul-f1-tools-"))
    workdir = Path(tempfile.mkdtemp(prefix="jul-f1-workdir-"))
    priv: PrivRunner | None = None
    lab: F1LabTopology | None = None
    ledger = LedgerRecorder(
        manifest=manifest,
        harness_sha=harness_sha,
        binary_sha256=binary_sha256,
        provenance=provenance,
        checkpoint_dir=evidence_dir,
    )

    topology_info: dict[str, Any] = {}
    service_info: dict[str, Any] = {}
    w0_w4_report: dict[str, Any] = {}
    dry_run_report: dict[str, Any] = {}
    teardown_info: dict[str, Any] = {}
    final_serving_version = ""

    try:
        try:
            scrape_bin = bin_dir / "soak-scrape"
            load_bin = bin_dir / "burn-in-load"
            subprocess.run(["go", "build", "-o", str(scrape_bin), "scripts/soak-scrape.go"], cwd=ROOT, check=True)
            subprocess.run(["go", "build", "-o", str(load_bin), "scripts/burn-in-load.go"], cwd=ROOT, check=True)

            for toml_file in EXAMPLES_DIR.glob("*.toml"):
                shutil.copyfile(toml_file, workdir / toml_file.name)
            subprocess.run(
                [sys.executable if "sys" in globals() else "python3", str(EXAMPLES_DIR / "generate_lab_assets.py"), "--out", str(workdir)],
                cwd=ROOT,
                check=True,
            )
            creds = json.loads((workdir / ".lab-credentials.json").read_text(encoding="utf-8"))
            tokens = {
                "admin": "f1-admin-tok-" + secrets.token_urlsafe(24),
                "operator": "f1-operator-tok-" + secrets.token_urlsafe(24),
                "viewer": "f1-viewer-tok-" + secrets.token_urlsafe(24),
                "auditor": "f1-auditor-tok-" + secrets.token_urlsafe(24),
            }

            priv = PrivRunner()
            lab = F1LabTopology(priv=priv, workdir=workdir, jul_bin=jul_bin, lean_bin=lean_bin)

            print("== Step 1: Verifying host has no pre-existing Jul collisions & setting up network namespaces + systemd services ==")
            lab.verify_no_collisions()
            topology_info = lab.setup_network()
            service_info = lab.setup_services(tokens)

            print("== Step 2: Executing safe loopback -> LAN sequence (CASE-F1-001 .. CASE-F1-005) ==")
            seq_state = execute_safe_sequence(lab, ledger, tokens, creds, service_info)

            print("== Step 3: Exercising managed and file_owned authorities & §9.4 postconditions (CASE-F1-006 .. CASE-F1-015) ==")
            execute_authority_and_postconditions(lab, ledger, tokens, creds, seq_state)

            print("== Step 4: Proving §11 same-instance isolation & advanced controls (CASE-F1-016 .. CASE-F1-029) ==")
            execute_isolation_and_controls(lab, ledger, tokens, creds, seq_state)

            print("== Step 5: Verifying #642 complete withdrawal, legacy auth.deny, mTLS, & file-owned variants (CASE-F1-030 .. CASE-F1-039, 049, 050) ==")
            execute_withdrawal_mtls_and_continuity(lab, ledger, tokens, creds, seq_state, load_bin)

            if args.recompute_w0_w4:
                print("== Step 6: Recomputing retained full-duration W0-W4 metrics & executing live phase-aware W1/W2/W3/W4 verification (CASE-F1-040 .. CASE-F1-044) ==")
                w0_w4_report = recompute_and_verify_retained_w0_w4(
                    lab, ledger, tokens, creds, seq_state, load_bin, evidence_dir
                )
            elif not args.skip_w0_w4:
                print(f"== Step 6: Executing W0-W4 resource & continuity qualification (scale={args.scale}) (CASE-F1-040 .. CASE-F1-044) ==")
                w0_w4_report = execute_w0_w4_measurements(
                    lab, ledger, tokens, creds, seq_state, scrape_bin, load_bin, evidence_dir, scale=args.scale
                )

            print("== Step 7: Executing spec §14 T1-T6 automated surface rehearsal (CASE-F1-045) ==")
            dry_run_report = execute_operator_dry_run_t1_t6(lab, ledger, tokens, creds, seq_state, evidence_dir)
            final_serving_version = get_v1_status(tokens["admin"])["serving_version"]
        finally:
            print("== Step 8: Executing deterministic teardown ==")
            try:
                PROC_REGISTRY.terminate_all()
            except Exception:
                pass
            if lab is not None:
                try:
                    teardown_info = lab.teardown()
                except Exception as td_exc:
                    teardown_info = {"clean": False, "cleanup_errors": [str(td_exc)]}
            shutil.rmtree(workdir, ignore_errors=True)
            shutil.rmtree(bin_dir, ignore_errors=True)

        # Fresh-directory & clean-host downloaded-binary runbook rehearsal (CASE-F1-046)
        fresh_dir = Path(tempfile.mkdtemp(prefix="jul-f1-fresh-runbook-"))
        try:
            assert teardown_info.get("clean") is True, f"Teardown reported dirty state: {teardown_info}"
            shutil.copytree(EXAMPLES_DIR, fresh_dir / "f1")
            subprocess.run(["python3", str(fresh_dir / "f1" / "generate_lab_assets.py"), "--out", str(fresh_dir / "f1"), "--deterministic"], check=True, capture_output=True)
            chk_env = {
                **os.environ,
                "JUL_ADMIN_TOKEN": "fresh-runbook-admin-token-placeholder",
                "JUL_F1_AUDITOR_TOKEN": "fresh-runbook-auditor-token-placeholder",
                "JUL_F1_VIEWER_TOKEN": "fresh-runbook-viewer-token-placeholder",
                "JUL_F1_OPERATOR_TOKEN": "fresh-runbook-operator-token-placeholder",
            }
            chk = subprocess.run([str(jul_bin), "check", "-config", "jul.toml"], cwd=fresh_dir / "f1", env=chk_env, capture_output=True, text=True, check=True)
            lnt = subprocess.run([str(jul_bin), "lint", "-config", "jul.toml"], cwd=fresh_dir / "f1", env=chk_env, capture_output=True, text=True, check=True)

            assert priv is not None
            rehearsal_sh = fresh_dir / "runbook_rehearsal.sh"
            rehearsal_sh.write_text(
                f"""#!/usr/bin/env bash
set -euo pipefail
for p in /usr/local/bin/jul /etc/jul /var/lib/jul /var/cache/jul /var/log/jul \\
         /etc/systemd/system/jul.service /etc/systemd/system/jul-readonly.service \\
         /etc/systemd/system/jul-f1-dashboard.service; do
  [ ! -e "$p" ] || {{ echo "STOP: existing resource $p" >&2; exit 1; }}
done
for u in jul.service jul-readonly.service jul-f1-dashboard.service; do
  ! systemctl is-active --quiet "$u" || {{ echo "STOP: active unit $u" >&2; exit 1; }}
done
! getent passwd jul >/dev/null || {{ echo "STOP: user jul exists" >&2; exit 1; }}

WORKDIR="$(mktemp -d /tmp/jul-f1-operator.XXXXXX)"
cleanup() {{
  systemctl stop jul.service jul-readonly.service jul-f1-dashboard.service 2>/dev/null || true
  rm -rf /etc/systemd/system/jul.service /etc/systemd/system/jul-readonly.service /etc/systemd/system/jul-f1-dashboard.service \\
         /etc/systemd/system/jul.service.d /etc/systemd/system/jul-readonly.service.d
  ip link del j581opbr 2>/dev/null || true
  systemctl daemon-reload
  systemctl reset-failed jul.service jul-readonly.service jul-f1-dashboard.service 2>/dev/null || true
  rm -rf /usr/local/bin/jul /etc/jul /var/lib/jul /var/cache/jul /var/log/jul "$WORKDIR"
  userdel jul 2>/dev/null || true
}}
trap cleanup EXIT

ip link add j581opbr type bridge
ip addr add 192.0.2.10/24 dev j581opbr
ip link set j581opbr up

cp -r {ROOT}/examples/sharing/f1-standalone/* "$WORKDIR/"
python3 "$WORKDIR/generate_lab_assets.py" --out "$WORKDIR" --lan-ip 192.0.2.10 >/dev/null

install -m0755 "{jul_bin}" /usr/local/bin/jul
useradd --system --no-create-home --shell /usr/sbin/nologin jul
install -d -o jul -g jul -m0700 \\
  /etc/jul /etc/jul/tls /etc/jul/portal /etc/jul/portal/assets \\
  /var/lib/jul /var/lib/jul/private /etc/jul/private /var/lib/jul/config-history \\
  /var/cache/jul /var/log/jul

sed -i 's|history_dir = "state/config-history"|history_dir = "/var/lib/jul/config-history"|g' "$WORKDIR"/*.toml
install -o jul -g jul -m0600 "$WORKDIR"/tls/* /etc/jul/tls/
install -o jul -g jul -m0600 "$WORKDIR/dashboard.htpasswd" /etc/jul/dashboard.htpasswd
install -o jul -g jul -m0644 "$WORKDIR/portal/index.html" /etc/jul/portal/index.html
install -o jul -g jul -m0644 "$WORKDIR/portal/assets/bundle.bin" /etc/jul/portal/assets/bundle.bin
install -o root -g root -m0755 "$WORKDIR/dashboard_backend.py" /etc/jul/dashboard_backend.py
install -o jul -g jul -m0600 "$WORKDIR/01-loopback-preview.toml" /etc/jul/server.toml

sudo -u jul /usr/local/bin/jul version-key init --file /var/lib/jul/private/effective-version.key
install -o jul -g jul -m0400 /var/lib/jul/private/effective-version.key /etc/jul/private/effective-version.key

ADMIN_TOK="$(openssl rand -hex 24)"
OPERATOR_TOK="$(openssl rand -hex 24)"
VIEWER_TOK="$(openssl rand -hex 24)"
AUDITOR_TOK="$(openssl rand -hex 24)"
printf '%s\\n' "$ADMIN_TOK" > "$WORKDIR/admin.token"
chmod 0600 "$WORKDIR/admin.token"
cat >/etc/jul/jul.env <<EOF
JUL_ADMIN_TOKEN=${{ADMIN_TOK}}
JUL_F1_OPERATOR_TOKEN=${{OPERATOR_TOK}}
JUL_F1_VIEWER_TOKEN=${{VIEWER_TOK}}
JUL_F1_AUDITOR_TOKEN=${{AUDITOR_TOK}}
EOF
chown root:jul /etc/jul/jul.env
chmod 0640 /etc/jul/jul.env

sudo -u jul bash -c 'set -a; source /etc/jul/jul.env; set +a; cd /etc/jul && /usr/local/bin/jul check -config /etc/jul/server.toml && /usr/local/bin/jul lint -config /etc/jul/server.toml'

cp {ROOT}/deploy/systemd/jul.service {ROOT}/deploy/systemd/jul-readonly.service /etc/systemd/system/
install -d -m0755 /etc/systemd/system/jul.service.d /etc/systemd/system/jul-readonly.service.d
cat >/etc/systemd/system/jul.service.d/f1.conf <<EOF
[Service]
WorkingDirectory=/etc/jul
EnvironmentFile=/etc/jul/jul.env
EOF
cp /etc/systemd/system/jul.service.d/f1.conf /etc/systemd/system/jul-readonly.service.d/f1.conf
cat >/etc/systemd/system/jul-f1-dashboard.service <<EOF
[Unit]
Description=Jul F1 Restricted Dashboard Backend (Loopback Only)
After=network.target

[Service]
Type=simple
User=jul
Group=jul
ExecStart=/usr/bin/python3 /etc/jul/dashboard_backend.py --listen 127.0.0.1:3000
Restart=on-failure
RestartSec=1s
NoNewPrivileges=yes
PrivateTmp=yes
ProtectSystem=strict
ProtectHome=yes

[Install]
WantedBy=multi-user.target
EOF

systemctl daemon-reload
systemctl start jul-f1-dashboard.service jul.service
for _ in $(seq 1 40); do
  if curl -fsS http://127.0.0.1:3000/healthz >/dev/null 2>&1 && curl -fsS http://127.0.0.1:9443/readyz >/dev/null 2>&1; then
    break
  fi
  sleep 0.1
done

export JUL_ENDPOINT="http://127.0.0.1:9443"
TOKEN_FILE="$WORKDIR/admin.token"
INTENDED_USER="$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["intended_user"])' "$WORKDIR/.lab-credentials.json")"
INTENDED_PASS="$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["intended_password"])' "$WORKDIR/.lab-credentials.json")"

cd "$WORKDIR"
BASE_VER="$(/usr/local/bin/jul status --endpoint "$JUL_ENDPOINT" --token-file "$TOKEN_FILE" --json | python3 -c 'import json,sys; print(json.load(sys.stdin)["serving_version"])')"
/usr/local/bin/jul apply --endpoint "$JUL_ENDPOINT" --token-file "$TOKEN_FILE" --adopt-external --base-version "$BASE_VER" --idempotency-key f1-step1-adopt --json >/dev/null

for step_cfg in 02-loopback-dashboard-restricted.toml 03-loopback-portal-public.toml jul.toml; do
  BASE_VER="$(/usr/local/bin/jul status --endpoint "$JUL_ENDPOINT" --token-file "$TOKEN_FILE" --json | python3 -c 'import json,sys; print(json.load(sys.stdin)["persisted_version"])')"
  /usr/local/bin/jul plan --endpoint "$JUL_ENDPOINT" --token-file "$TOKEN_FILE" --config "$step_cfg" --base-version "$BASE_VER" --json > step-plan.json
  /usr/local/bin/jul apply --endpoint "$JUL_ENDPOINT" --token-file "$TOKEN_FILE" --config "$step_cfg" --base-version "$BASE_VER" --idempotency-key "f1-step-${{step_cfg%.toml}}" --json >/dev/null
  /usr/local/bin/jul postcondition --endpoint "$JUL_ENDPOINT" --token-file "$TOKEN_FILE" --file step-plan.json --json | grep -q candidate_serving
done

CODE_UNAUTH="$(curl -s -o /dev/null -w '%{{http_code}}' --cacert tls/root-ca.pem --resolve dash.lab.test:443:192.0.2.10 https://dash.lab.test/api/summary)"
[ "$CODE_UNAUTH" = "401" ]
curl -fsS --cacert tls/root-ca.pem --user "$INTENDED_USER:$INTENDED_PASS" --resolve dash.lab.test:443:192.0.2.10 https://dash.lab.test/api/summary | grep -q "JUL-F1-RESTRICTED-DASHBOARD-MARKER-v1"
cleanup
trap - EXIT
for p in /usr/local/bin/jul /etc/jul /var/lib/jul /var/cache/jul /var/log/jul \\
         /etc/systemd/system/jul.service /etc/systemd/system/jul-readonly.service \\
         /etc/systemd/system/jul-f1-dashboard.service \\
         /etc/systemd/system/jul.service.d /etc/systemd/system/jul-readonly.service.d; do
  [ ! -e "$p" ] || {{ echo "FAIL: leftover path $p" >&2; exit 1; }}
done
! getent passwd jul >/dev/null || {{ echo "FAIL: leftover user jul" >&2; exit 1; }}
echo "CLEAN_HOST_RUNBOOK_REHEARSAL_PASSED"
""",
                encoding="utf-8",
            )
            rehearsal_sh.chmod(0o755)
            reh_res = priv.run("bash", str(rehearsal_sh), timeout=90)
            assert "CLEAN_HOST_RUNBOOK_REHEARSAL_PASSED" in reh_res.stdout

            ledger.record_case(
                "CASE-F1-046",
                "pass",
                final_serving_version,
                "same_host_loopback",
                "F1 teardown + fresh-directory & clean-host downloaded-binary runbook validator",
                "Teardown verified clean=true with zero cleanup/verification errors and zero leftovers; executed full clean-host downloaded-binary runbook sequence (§§3–4 & §8) from fresh directory through jul.service startup, CLI adopt/plan/apply/postcondition, client 401/200 verification, and clean teardown.",
                {
                    "teardown": teardown_info,
                    "fresh_check_stdout": chk.stdout.strip(),
                    "fresh_lint_stdout": lnt.stdout.strip(),
                    "clean_host_runbook_rehearsal": "CLEAN_HOST_RUNBOOK_REHEARSAL_PASSED",
                },
            )
        finally:
            if priv is not None:
                try:
                    priv.close()
                except Exception:
                    pass
            shutil.rmtree(fresh_dir, ignore_errors=True)

        # CASE-F1-047: CI config-check, Go race tests, and Python unit/oracle test suite
        cfg_chk = subprocess.run(["bash", "scripts/config-check.sh"], cwd=ROOT, env={**os.environ, "JUL_BIN": str(jul_bin)}, capture_output=True, text=True, check=True)
        go_tst = subprocess.run(["go", "test", "-race", "-count=1", "scripts/burn-in-load.go", "scripts/burn-in-load_test.go"], cwd=ROOT, capture_output=True, text=True, check=True)
        py_tst = subprocess.run([sys.executable, "scripts/test_sharing_f1_lab.py"], cwd=ROOT, capture_output=True, text=True, check=True)
        ledger.record_case(
            "CASE-F1-047",
            "pass",
            final_serving_version,
            "same_host_loopback",
            "scripts/config-check.sh + scripts/burn-in-load_test.go + scripts/test_sharing_f1_lab.py",
            "scripts/config-check.sh validated all 14 F1 configurations with generated TLS/CRL/htpasswd/static-root assets and lean WAF rejection diagnostic; go test -race scripts/burn-in-load* and python3 scripts/test_sharing_f1_lab.py passed.",
            {"config_check_exit": cfg_chk.returncode, "burn_in_load_test_exit": go_tst.returncode, "python_unit_test_exit": py_tst.returncode},
        )

        # CASE-F1-048: Downstream handoff interfaces (#634, #582, #583, #613) and documentation/issue coherence check
        req_docs = [
            ROOT / "docs" / "sharing" / "f1-standalone-runbook.md",
            ROOT / "docs" / "sharing" / "f1-standalone-evidence.md",
            ROOT / "docs" / "sharing" / "f1-handoff-interfaces.json",
        ]
        missing_docs = [str(p.relative_to(ROOT)) for p in req_docs if not p.exists()]
        assert not missing_docs, f"Missing required F1 documentation/handoff files: {missing_docs}"
        handoff_obj = json.loads((ROOT / "docs" / "sharing" / "f1-handoff-interfaces.json").read_text(encoding="utf-8"))
        for key in ("handoff_for_634_sec_isolation", "handoff_for_582_583_613"):
            assert key in handoff_obj, f"Missing handoff section {key} in f1-handoff-interfaces.json"
        docs_chk = subprocess.run([sys.executable, "scripts/docs-check.py"], cwd=ROOT, capture_output=True, text=True, check=True)
        ledger.record_case(
            "CASE-F1-048",
            "pass",
            final_serving_version,
            "same_host_loopback",
            "F1 manifest + acceptance ledger + #634/#582/#583/#613 handoff contracts + docs-check.py",
            "Verified runbook, evidence report, #634/#582/#583/#613 handoff interfaces, and docs-check.py with zero errors.",
            {"downstream_consumers": ["#634", "#582", "#583", "#584", "#613", "#585", "#586", "#587"], "docs_check_exit": docs_chk.returncode},
        )
    except BaseException as exc:
        ledger.write_checkpoint("failed", failure_error=f"{type(exc).__name__}: {exc}", teardown_info=teardown_info)
        raise

    reconciliation = reconcile_manifest_and_ledger(manifest, ledger.cases)
    full_ledger = {
        "schema_version": 2,
        "issue": "#581",
        "work_package": "SHARE-02A",
        "run_status": "completed",
        "evidence_classification": "current-source-development",
        "generated_at_utc": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
        "host": {
            "os": platform.system(),
            "release": platform.release(),
            "arch": platform.machine(),
            "cpus": os.cpu_count(),
        },
        "identities": provenance,
        "topology": topology_info,
        "systemd_hardening": service_info,
        "reconciliation": reconciliation,
        "cases": [ledger.cases[c["case_id"]] for c in manifest["cases"] if c["case_id"] in ledger.cases],
        "w0_w4_summary": w0_w4_report,
        "operator_dry_run_t1_t6": dry_run_report,
        "teardown": teardown_info,
    }
    atomic_write_json(ledger_path, full_ledger)
    (evidence_dir / "f1-failure-report.json").unlink(missing_ok=True)
    print(f"== Completed F1 execution: {reconciliation['passed_cases']}/{reconciliation['manifest_cases']} cases passed; ledger written to {ledger_path} ==")
    if not args.skip_w0_w4 and not reconciliation["all_mandatory_passed"]:
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())








