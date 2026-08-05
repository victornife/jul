#!/usr/bin/env python3
"""Unit tests for scripts/check-package-coverage.py."""

from __future__ import annotations

import json
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

SCRIPT = Path(__file__).with_name("check-package-coverage.py")


class CoverageGateTests(unittest.TestCase):
    def run_gate(self, packages: dict[str, dict[str, object]], profile: str) -> subprocess.CompletedProcess[str]:
        manifest = {
            "version": 1,
            "profile": "full-tags",
            "baseline": {
                "sha": "test",
                "workflow_run": 1,
                "measured_on": "2026-08-05",
            },
            "packages": packages,
        }
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            manifest_path = root / "manifest.json"
            profile_path = root / "cover.out"
            manifest_path.write_text(json.dumps(manifest), encoding="utf-8")
            profile_path.write_text(profile, encoding="utf-8")
            return subprocess.run(
                [
                    sys.executable,
                    str(SCRIPT),
                    "--profile",
                    str(manifest_path),
                    "--coverprofile",
                    str(profile_path),
                ],
                check=False,
                capture_output=True,
                text=True,
            )

    @staticmethod
    def rule(label: str, floor: float = 50.0, baseline: float = 60.0) -> dict[str, object]:
        return {"label": label, "baseline": baseline, "floor": floor}

    def test_passes_and_sorts_output_by_import_path(self) -> None:
        result = self.run_gate(
            {
                "jul/internal/waf": self.rule("waf"),
                "jul/internal/rbac": self.rule("rbac"),
            },
            """mode: atomic
jul/internal/waf/waf.go:1.1,2.1 4 1
jul/internal/rbac/policy.go:1.1,2.1 3 1
""",
        )
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(
            [line.split(":", 1)[0] for line in result.stdout.splitlines()],
            ["PASS rbac", "PASS waf"],
        )

    def test_fails_below_floor(self) -> None:
        result = self.run_gate(
            {"jul/internal/rbac": self.rule("rbac", floor=75.0, baseline=80.0)},
            """mode: atomic
jul/internal/rbac/policy.go:1.1,2.1 3 1
jul/internal/rbac/role.go:1.1,2.1 2 0
""",
        )
        self.assertEqual(result.returncode, 1)
        self.assertIn("FAIL rbac: 60.0%", result.stderr)

    def test_missing_package_is_a_distinct_input_failure(self) -> None:
        result = self.run_gate(
            {"jul/internal/plugins": self.rule("plugins")},
            """mode: atomic
jul/internal/rbac/policy.go:1.1,2.1 3 1
""",
        )
        self.assertEqual(result.returncode, 2)
        self.assertIn("absent from the coverage profile", result.stderr)

    def test_subpackage_does_not_satisfy_parent_package(self) -> None:
        result = self.run_gate(
            {"jul/internal/plugins": self.rule("plugins")},
            """mode: atomic
jul/internal/plugins/testguest/main.go:1.1,2.1 3 1
""",
        )
        self.assertEqual(result.returncode, 2)

    def test_malformed_profile_line_fails_closed(self) -> None:
        result = self.run_gate(
            {"jul/internal/waf": self.rule("waf")},
            """mode: atomic
this is not a coverage record
""",
        )
        self.assertEqual(result.returncode, 2)
        self.assertIn("malformed cover profile line", result.stderr)

    def test_manifest_rejects_floor_above_baseline(self) -> None:
        result = self.run_gate(
            {"jul/internal/waf": self.rule("waf", floor=80.0, baseline=70.0)},
            "mode: atomic\n",
        )
        self.assertEqual(result.returncode, 2)
        self.assertIn("exceeds recorded baseline", result.stderr)


if __name__ == "__main__":
    unittest.main()
