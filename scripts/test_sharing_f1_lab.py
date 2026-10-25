#!/usr/bin/env python3
# Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
# SPDX-License-Identifier: agpl

"""Unit tests for the Fixture F1 (#581) manifest, asset generator, backend, metrics/probe validators, teardown, and ledger reconciliation."""

from __future__ import annotations

import base64
import gzip
import hashlib
import json
from pathlib import Path
import socket
import struct
import subprocess
import tempfile
import time
import unittest
from unittest import mock
import urllib.request

import sharing_f1_lab

ROOT = Path(__file__).resolve().parent.parent
MANIFEST_PATH = ROOT / "docs" / "sharing" / "f1-configuration-manifest.json"
EXAMPLES_DIR = ROOT / "examples" / "sharing" / "f1-standalone"


class TestSharingF1Lab(unittest.TestCase):
    def test_manifest_structure_and_referenced_files_exist(self) -> None:
        manifest = json.loads(MANIFEST_PATH.read_text(encoding="utf-8"))
        self.assertEqual(manifest["issue"], "#581")
        self.assertEqual(manifest["work_package"], "SHARE-02A")
        self.assertEqual(len(manifest["configurations"]), 14)
        self.assertEqual(len(manifest["cases"]), 50)
        cfg_ids = {c["config_id"] for c in manifest["configurations"]}
        for cfg in manifest["configurations"]:
            path = ROOT / cfg["file"]
            self.assertTrue(path.is_file(), f"missing config file: {path}")
        # Verify all 6 (authority, auth_variant) pairs are present in manifest configurations
        pairs = {(c["authority"], c["auth_variant"]) for c in manifest["configurations"]}
        for authority in ("managed", "file_owned"):
            for auth_variant in ("basic", "mtls_shared_listener", "mtls_dedicated_listener"):
                self.assertIn((authority, auth_variant), pairs)
        case_ids = set()
        for case in manifest["cases"]:
            cid = case["case_id"]
            self.assertNotIn(cid, case_ids, f"duplicate case_id {cid}")
            case_ids.add(cid)
            self.assertTrue(case["config_ids"], f"case {cid} has no config_ids")
            for ref in case["config_ids"]:
                self.assertIn(ref, cfg_ids, f"case {cid} references unknown config_id {ref}")

    def test_reconcile_manifest_and_ledger_detects_missing_and_failed_rows(self) -> None:
        manifest = json.loads(MANIFEST_PATH.read_text(encoding="utf-8"))
        # 1. Empty cases -> fails
        r_empty = sharing_f1_lab.reconcile_manifest_and_ledger(manifest, {})
        self.assertFalse(r_empty["all_mandatory_passed"])
        self.assertEqual(len(r_empty["missing_cases"]), 50)

        # 2. All present and pass -> succeeds
        all_pass = {
            c["case_id"]: {
                "case_id": c["case_id"],
                "category": c["category"],
                "verdict": "pass",
                "config_ids": c["config_ids"],
            }
            for c in manifest["cases"]
        }
        r_ok = sharing_f1_lab.reconcile_manifest_and_ledger(manifest, all_pass)
        self.assertTrue(r_ok["all_mandatory_passed"])
        self.assertEqual(r_ok["passed_cases"], 50)
        self.assertEqual(r_ok["missing_configurations"], [])
        self.assertEqual(r_ok["missing_authority_auth_pairs"], [])

        # 3. One failed case -> fails
        one_fail = dict(all_pass)
        one_fail["CASE-F1-033"] = {**one_fail["CASE-F1-033"], "verdict": "fail"}
        r_fail = sharing_f1_lab.reconcile_manifest_and_ledger(manifest, one_fail)
        self.assertFalse(r_fail["all_mandatory_passed"])
        self.assertEqual(r_fail["non_pass_cases"], ["CASE-F1-033"])

        # 4. A06/A12 regression: parser/CI-only case CASE-F1-047 must NOT satisfy runtime configuration coverage
        without_runtime_cfg12 = {
            cid: {
                **rec,
                "config_ids": (
                    rec["config_ids"]
                    if cid in ("CASE-F1-047", "CASE-F1-048")
                    else [cfg for cfg in rec["config_ids"] if cfg != "CFG-F1-12"]
                ),
            }
            for cid, rec in all_pass.items()
        }
        r_no_runtime_cfg12 = sharing_f1_lab.reconcile_manifest_and_ledger(manifest, without_runtime_cfg12)
        self.assertFalse(r_no_runtime_cfg12["all_mandatory_passed"])
        self.assertIn("CFG-F1-12", r_no_runtime_cfg12["missing_configurations"])
        self.assertIn(["file_owned", "mtls_shared_listener"], r_no_runtime_cfg12["missing_authority_auth_pairs"])

        # 5. Scaled non-qualifying W0-W4 verdict ("smoke_non_qualifying") must fail full reconciliation
        scaled_w = dict(all_pass)
        scaled_w["CASE-F1-040"] = {**scaled_w["CASE-F1-040"], "verdict": "smoke_non_qualifying"}
        r_scaled = sharing_f1_lab.reconcile_manifest_and_ledger(manifest, scaled_w)
        self.assertFalse(r_scaled["all_mandatory_passed"])
        self.assertIn("CASE-F1-040", r_scaled["non_pass_cases"])

    def test_metrics_series_median_and_cpu_validators_reject_missing_or_short_series(self) -> None:
        samples = [
            {
                "t": "2026-04-20T21:10:00Z",
                "values": {
                    "go_goroutines": 25.0,
                    'jul_upstream_connections{pool="dashboard"}': 0.0,
                    "process_cpu_seconds_total": 1.0,
                },
            },
            {
                "t": "2026-04-20T21:10:05Z",
                "values": {
                    "go_goroutines": 27.0,
                    'jul_upstream_connections{pool="dashboard"}': 2.0,
                    "process_cpu_seconds_total": 1.5,
                },
            },
            {
                "t": "2026-04-20T21:10:10Z",
                "values": {
                    "go_goroutines": 26.0,
                    'jul_upstream_connections{pool="dashboard"}': 0.0,
                    "process_cpu_seconds_total": 1.75,
                },
            },
        ]
        self.assertEqual(sharing_f1_lab.compute_series_median(samples, "go_goroutines", 3), 26.0)
        self.assertEqual(
            sharing_f1_lab.compute_series_median(samples, 'jul_upstream_connections{pool="dashboard"}', 3),
            0.0,
        )

        # A02 regression: nonexistent metric name must raise ValueError/KeyError, never return 0.0 silently
        with self.assertRaises((KeyError, ValueError)):
            sharing_f1_lab.compute_series_median(
                samples,
                'jul_upstream_connections{state="active",upstream="dashboard"}',
                2,
            )

        # Insufficient samples for tail window must raise ValueError
        with self.assertRaises(ValueError):
            sharing_f1_lab.compute_series_median(samples, "go_goroutines", 4)

        # CPU rate cores and peak CPU cores
        self.assertAlmostEqual(sharing_f1_lab.compute_cpu_rate_cores(samples), 0.075, places=4)
        self.assertAlmostEqual(sharing_f1_lab.compute_peak_cpu_cores(samples), 0.10, places=4)
        with self.assertRaises(ValueError):
            sharing_f1_lab.compute_cpu_rate_cores(samples[:1])

    def test_validate_probe_continuity_checks_event_log_and_gaps(self) -> None:
        with tempfile.TemporaryDirectory(prefix="test-f1-probe-") as tmp:
            ev_path = Path(tmp) / "probe.jsonl"
            # Missing event log must raise FileNotFoundError
            with self.assertRaises(FileNotFoundError):
                sharing_f1_lab.validate_probe_continuity(ev_path, {"probe_portal_ok": 3, "probe_portal_fail": 0}, min_probes=3)

            events = [
                {"t": "2026-04-20T21:20:00Z", "event": "probe_portal", "status": 200, "ok": True},
                {"t": "2026-04-20T21:20:01Z", "event": "probe_portal", "status": 200, "ok": True},
                {"t": "2026-04-20T21:20:02Z", "event": "probe_portal", "status": 200, "ok": True},
                {"t": "2026-04-20T21:20:03Z", "event": "probe_portal", "status": 200, "ok": True},
            ]
            ev_path.write_text("\n".join(json.dumps(e) for e in events) + "\n", encoding="utf-8")
            summary = {"probe_portal_ok": 4, "probe_portal_fail": 0}
            res = sharing_f1_lab.validate_probe_continuity(ev_path, summary, min_probes=3, max_gap_s=2.0)
            self.assertEqual(res["event_count"], 4)
            self.assertAlmostEqual(res["max_gap_s"], 1.0, places=2)

            # Excessive gap must fail
            events_gap = list(events)
            events_gap[-1] = {"t": "2026-04-20T21:20:10Z", "event": "probe_portal", "status": 200, "ok": True}
            ev_path.write_text("\n".join(json.dumps(e) for e in events_gap) + "\n", encoding="utf-8")
            with self.assertRaises(AssertionError):
                sharing_f1_lab.validate_probe_continuity(ev_path, summary, min_probes=3, max_gap_s=3.5)

            # Failed probe status in event log must fail even if summary claims 0 failures
            events_bad = list(events)
            events_bad[2] = {"t": "2026-04-20T21:20:02Z", "event": "probe_portal", "status": 502, "ok": False}
            ev_path.write_text("\n".join(json.dumps(e) for e in events_bad) + "\n", encoding="utf-8")
            with self.assertRaises(AssertionError):
                sharing_f1_lab.validate_probe_continuity(ev_path, summary, min_probes=3, max_gap_s=3.5)

    def test_ledger_recorder_atomic_checkpoint_preserves_partial_progress_on_failure(self) -> None:
        manifest = json.loads(MANIFEST_PATH.read_text(encoding="utf-8"))
        with tempfile.TemporaryDirectory(prefix="test-f1-ledger-") as tmp:
            ckpt_dir = Path(tmp)
            rec = sharing_f1_lab.LedgerRecorder(
                manifest=manifest,
                harness_sha="deadbeef",
                binary_sha256="cafebabe",
                provenance={"worktree_dirty": True},
                checkpoint_dir=ckpt_dir,
            )
            rec.record_case(
                "CASE-F1-001",
                "pass",
                "sha256:1111",
                "same_host_loopback",
                "Jul loopback listener",
                "Loopback ok",
                {"status": 200},
            )
            in_prog = json.loads((ckpt_dir / "f1-acceptance-ledger.json").read_text(encoding="utf-8"))
            self.assertEqual(in_prog["run_status"], "in_progress")
            self.assertEqual(len(in_prog["cases"]), 1)

            rec.write_checkpoint("failed", failure_error="RuntimeError: simulated failure", teardown_info={"clean": True})
            failed_ckpt = json.loads((ckpt_dir / "f1-failure-report.json").read_text(encoding="utf-8"))
            self.assertEqual(failed_ckpt["run_status"], "failed")
            self.assertIn("simulated failure", failed_ckpt["failure_error"])
            self.assertEqual(len(failed_ckpt["cases"]), 1)
            self.assertEqual(failed_ckpt["cases"][0]["case_id"], "CASE-F1-001")

    def test_verify_and_collect_run_provenance_enforces_registered_baseline_and_clean_tree(self) -> None:
        manifest = json.loads(MANIFEST_PATH.read_text(encoding="utf-8"))
        with tempfile.TemporaryDirectory(prefix="test-f1-prov-") as tmp:
            fake_full = Path(tmp) / "jul-full"
            fake_lean = Path(tmp) / "jul-lean"
            fake_full.write_text("#!/bin/sh\nif [ \"$1\" = \"version\" ]; then echo 'jul v2.2.0-dev+52b09adfbccf'; else echo '{\"build_profile\":\"full\",\"waf\":true}'; fi\n", encoding="utf-8")
            fake_lean.write_text("#!/bin/sh\nif [ \"$1\" = \"version\" ]; then echo 'jul v2.2.0-dev+52b09adfbccf'; else echo '{\"build_profile\":\"lean\",\"waf\":false}'; fi\n", encoding="utf-8")
            fake_full.chmod(0o755)
            fake_lean.chmod(0o755)

            # Unregistered SHA must fail when require_registered_baseline=True
            with self.assertRaises(RuntimeError) as ctx:
                sharing_f1_lab.verify_and_collect_run_provenance(
                    fake_full, fake_lean, manifest, require_registered_baseline=True, require_clean_tree=False
                )
            self.assertIn("does not match any registered baseline", str(ctx.exception))

            # Unregistered SHA is recorded as provenance_verified=False when require_registered_baseline=False
            prov = sharing_f1_lab.verify_and_collect_run_provenance(
                fake_full, fake_lean, manifest, require_registered_baseline=False, require_clean_tree=False
            )
            self.assertFalse(prov["provenance_verified"])
            self.assertIn("worktree_clean", prov)
            self.assertIsInstance(prov["dirty_files"], list)

    def test_verify_no_collisions_and_teardown_check_all_resource_classes(self) -> None:
        class FakePriv:
            def __init__(self, responses: dict[tuple[str, ...], subprocess.CompletedProcess[str]]) -> None:
                self.responses = responses
                self.calls: list[tuple[str, ...]] = []

            def run(self, *args: str, check: bool = True, input_text: str | None = None) -> subprocess.CompletedProcess[str]:
                self.calls.append(args)
                for prefix, cp in sorted(self.responses.items(), key=lambda kv: len(kv[0]), reverse=True):
                    if args[:len(prefix)] == prefix:
                        if check and cp.returncode != 0:
                            raise subprocess.CalledProcessError(cp.returncode, list(args), cp.stdout, cp.stderr)
                        return cp
                return subprocess.CompletedProcess(list(args), 0, "", "")

        with tempfile.TemporaryDirectory(prefix="test-f1-topo-") as tmp:
            # 1. Collision detection when jul-f1-dashboard.service is active
            priv_collide = FakePriv({
                ("systemctl", "is-active", "--quiet", "jul-f1-dashboard.service"): subprocess.CompletedProcess(
                    ["systemctl", "is-active", "--quiet", "jul-f1-dashboard.service"], 0, "", ""
                ),
                ("systemctl", "is-active"): subprocess.CompletedProcess(["systemctl"], 3, "inactive\n", ""),
                ("ip", "netns", "list"): subprocess.CompletedProcess(["ip"], 0, "", ""),
                ("ss", "-tlnH"): subprocess.CompletedProcess(["ss"], 0, "", ""),
                ("iptables", "-S", "JUL_F1_LAB"): subprocess.CompletedProcess(["iptables"], 1, "", "No chain"),
                ("id", "-u", "jul-f1-backend"): subprocess.CompletedProcess(["id"], 1, "", "no such user"),
            })
            lab = sharing_f1_lab.F1LabTopology(priv_collide, Path(tmp), Path("/bin/true"), Path("/bin/true"))
            with self.assertRaises(RuntimeError) as ctx:
                lab.verify_no_collisions()
            self.assertIn("jul-f1-dashboard.service", str(ctx.exception))

            # 2. Teardown reports clean=False if a cleanup command fails or a link/unit/user remains
            priv_dirty_td = FakePriv({
                ("ip", "link", "del", "veth-julf1-br"): subprocess.CompletedProcess(
                    ["ip", "link", "del", "veth-julf1-br"], 1, "", "Operation not permitted"
                ),
                ("userdel", "jul"): subprocess.CompletedProcess(
                    ["userdel", "jul"], 1, "", "user jul is currently used"
                ),
                ("ip", "netns", "list"): subprocess.CompletedProcess(["ip"], 0, "", ""),
                ("ip", "-o", "link", "show"): subprocess.CompletedProcess(["ip"], 0, "99: veth-julf1-br: <BROADCAST>\n", ""),
                ("iptables", "-S"): subprocess.CompletedProcess(["iptables"], 0, "", ""),
            })
            lab2 = sharing_f1_lab.F1LabTopology(priv_dirty_td, Path(tmp), Path("/bin/true"), Path("/bin/true"))
            lab2.created_units.append("jul.service")
            lab2.created_links.append("veth-julf1-br")
            lab2.created_user = True
            td = lab2.teardown()
            self.assertFalse(td["clean"])
            self.assertTrue(td["cleanup_errors"])
            self.assertIn("veth-julf1-br", td["leftover_links"])
            self.assertTrue(td["user_remaining"])
            # Ensure reset-failed was scoped to owned units, never global
            reset_calls = [c for c in priv_dirty_td.calls if c[:2] == ("systemctl", "reset-failed")]
            self.assertEqual(len(reset_calls), 1)
            self.assertEqual(reset_calls[0], ("systemctl", "reset-failed", "jul.service"))

    def test_generate_lab_assets_refuses_collision_without_force(self) -> None:
        with tempfile.TemporaryDirectory(prefix="test-f1-assets-") as tmp:
            out_dir = Path(tmp)
            proc1 = subprocess.run(
                ["python3", str(EXAMPLES_DIR / "generate_lab_assets.py"), "--out", str(out_dir), "--deterministic"],
                capture_output=True, text=True, check=True,
            )
            summary = json.loads(proc1.stdout)
            self.assertEqual(int(summary["portal_bundle_bytes"]), 262144)
            self.assertTrue((out_dir / "tls" / "lab.crt").is_file())
            self.assertTrue((out_dir / "dashboard.htpasswd").is_file())

            # Second run without --force must fail
            proc2 = subprocess.run(
                ["python3", str(EXAMPLES_DIR / "generate_lab_assets.py"), "--out", str(out_dir), "--deterministic"],
                capture_output=True, text=True,
            )
            self.assertNotEqual(proc2.returncode, 0)
            self.assertIn("refusing to overwrite", proc2.stderr)

    def test_dashboard_backend_endpoints_counters_and_idle_websocket_survival(self) -> None:
        with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as s:
            s.bind(("127.0.0.1", 0))
            port = s.getsockname()[1]
        addr = f"127.0.0.1:{port}"
        proc = subprocess.Popen(
            ["python3", str(EXAMPLES_DIR / "dashboard_backend.py"), "--listen", addr],
            stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True,
        )
        try:
            deadline = time.monotonic() + 5
            ready = False
            while time.monotonic() < deadline:
                try:
                    with urllib.request.urlopen(f"http://{addr}/healthz", timeout=1) as resp:
                        if resp.status == 200:
                            ready = True
                            break
                except Exception:
                    time.sleep(0.05)
            self.assertTrue(ready, "dashboard_backend.py did not become ready")

            req = urllib.request.Request(
                f"http://{addr}/api/summary",
                headers={"X-Forwarded-For": "192.0.2.11", "Tailscale-User-Login": "test@example.com"},
            )
            with urllib.request.urlopen(req, timeout=2) as resp:
                self.assertEqual(resp.status, 200)
                data = json.loads(resp.read().decode("utf-8"))
                self.assertEqual(data["marker"], sharing_f1_lab.DASHBOARD_MARKER)
                self.assertEqual(data["observed_headers"]["x-forwarded-for"], "192.0.2.11")

            with urllib.request.urlopen(f"http://{addr}/__control/counters", timeout=2) as resp:
                counters = json.loads(resp.read().decode("utf-8"))
                self.assertEqual(counters["dashboard_requests"], 1)

            # A05 regression: verify an idle WebSocket survives past multiple 0.25s socket timeouts (e.g. 0.8s idle)
            ws_key = base64.b64encode(b"0123456789abcdef").decode("ascii")
            with socket.create_connection(("127.0.0.1", port), timeout=5) as ws_sock:
                req_bytes = (
                    f"GET /ws HTTP/1.1\r\n"
                    f"Host: {addr}\r\n"
                    f"Upgrade: websocket\r\n"
                    f"Connection: Upgrade\r\n"
                    f"Sec-WebSocket-Key: {ws_key}\r\n"
                    f"Sec-WebSocket-Version: 13\r\n\r\n"
                ).encode("ascii")
                ws_sock.sendall(req_bytes)
                resp_buf = b""
                while b"\r\n\r\n" not in resp_buf:
                    resp_buf += ws_sock.recv(1024)
                self.assertIn(b"101 Switching Protocols", resp_buf)

                # Read initial server greeting text frame
                b0, b1 = ws_sock.recv(2)
                self.assertEqual(b0 & 0x0F, 0x1)
                plen = b1 & 0x7F
                greeting = ws_sock.recv(plen).decode("utf-8")
                self.assertIn(sharing_f1_lab.DASHBOARD_MARKER, greeting)

                # Stay completely silent for 0.85s (> 3x the backend's 0.25s poll timeout)
                time.sleep(0.85)
                with urllib.request.urlopen(f"http://{addr}/__control/counters", timeout=2) as resp:
                    c_mid = json.loads(resp.read().decode("utf-8"))
                    self.assertEqual(c_mid["ws_active"], 1)
                    self.assertEqual(c_mid["ws_closed"], 0)

                # Now send a masked Close frame (1008 Policy Violation) and verify clean echo
                mask_key = b"\x11\x22\x33\x44"
                close_payload = struct.pack("!H", 1008)
                masked = bytes(b ^ mask_key[i % 4] for i, b in enumerate(close_payload))
                ws_sock.sendall(bytes([0x88, 0x80 | 2]) + mask_key + masked)
                cb0, cb1 = ws_sock.recv(2)
                self.assertEqual(cb0 & 0x0F, 0x8)
                cbody = ws_sock.recv(cb1 & 0x7F)
                self.assertEqual(struct.unpack("!H", cbody[:2])[0], 1008)

            reset_req = urllib.request.Request(f"http://{addr}/__control/reset", data=b"{}", method="POST")
            with urllib.request.urlopen(reset_req, timeout=2) as resp:
                self.assertEqual(resp.status, 200)
            with urllib.request.urlopen(f"http://{addr}/__control/counters", timeout=2) as resp:
                counters_after = json.loads(resp.read().decode("utf-8"))
                self.assertEqual(counters_after["dashboard_requests"], 0)
        finally:
            proc.terminate()
            proc.communicate(timeout=5)


if __name__ == "__main__":
    unittest.main(verbosity=2)

