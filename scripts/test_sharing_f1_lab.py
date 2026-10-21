#!/usr/bin/env python3
# Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
# SPDX-License-Identifier: agpl

"""Unit tests for the Fixture F1 (#581) manifest, asset generator, backend, and ledger reconciliation."""

from __future__ import annotations

import json
from pathlib import Path
import socket
import subprocess
import tempfile
import time
import unittest
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
        self.assertEqual(len(manifest["configurations"]), 11)
        self.assertEqual(len(manifest["cases"]), 48)
        cfg_ids = {c["config_id"] for c in manifest["configurations"]}
        for cfg in manifest["configurations"]:
            path = ROOT / cfg["file"]
            self.assertTrue(path.is_file(), f"missing config file: {path}")
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
        self.assertEqual(len(r_empty["missing_cases"]), 48)

        # 2. All present and pass -> succeeds
        all_pass = {
            c["case_id"]: {"case_id": c["case_id"], "verdict": "pass", "config_ids": c["config_ids"]}
            for c in manifest["cases"]
        }
        r_ok = sharing_f1_lab.reconcile_manifest_and_ledger(manifest, all_pass)
        self.assertTrue(r_ok["all_mandatory_passed"])
        self.assertEqual(r_ok["passed_cases"], 48)
        self.assertEqual(r_ok["missing_configurations"], [])

        # 3. One failed case -> fails
        one_fail = dict(all_pass)
        one_fail["CASE-F1-033"] = {**one_fail["CASE-F1-033"], "verdict": "fail"}
        r_fail = sharing_f1_lab.reconcile_manifest_and_ledger(manifest, one_fail)
        self.assertFalse(r_fail["all_mandatory_passed"])
        self.assertEqual(r_fail["non_pass_cases"], ["CASE-F1-033"])

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

    def test_dashboard_backend_endpoints_and_counters(self) -> None:
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
