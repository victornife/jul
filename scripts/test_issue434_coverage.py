#!/usr/bin/env python3
"""Execute the actual workflow gates against coverage boundary fixtures."""
import contextlib
import io
import json
import os
import tempfile
import textwrap
import unittest
from pathlib import Path
from unittest.mock import patch


WORKFLOW = Path(__file__).resolve().parents[1] / ".github/workflows/issue434-quality.yml"


def gate_source(name):
    block = WORKFLOW.read_text().split("- name: " + name + "\n", 1)[1]
    source = block.split("python3 - <<'PY'\n", 1)[1].split("\n          PY", 1)[0]
    return textwrap.dedent(source)


class CoverageGateTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.go = self.root / "tmp/alpn-delivery"
        self.go.mkdir(parents=True)
        for name in ("head-lean", "head-full"):
            (self.go / (name + ".cover")).write_text("mode: atomic\njul/internal/stream/sni.go:1.1,2.1 1 1\n")
        (self.go / "complete.diff").write_text("")
        self.front = self.root / "internal/admin/ui/coverage"
        self.front.mkdir(parents=True)
        self.front_path = "internal/admin/ui/src/feature.ts"
        self.front_diff = "+++ b/" + self.front_path + "\n@@ -10 +10 @@\n+execute()\n"
        self.write_front(1, 1)

    def write_front(self, covered, total):
        data = {str(self.root / self.front_path): {
            "statementMap": {str(i): {"start": {"line": 10}, "end": {"line": 10}} for i in range(total)},
            "s": {str(i): int(i < covered) for i in range(total)},
        }}
        (self.front / "coverage-final.json").write_text(json.dumps(data))

    def run_gate(self, frontend=False, diff=""):
        name = ("Require changed frontend statements strictly above 90 percent" if frontend
                else "Enforce changed Go statements strictly above 90 percent")
        previous = Path.cwd()
        try:
            os.chdir(self.root)
            with patch.dict(os.environ, {"BASE_SHA": "base", "HEAD_SHA": "head"}), \
                    patch("subprocess.check_output", return_value=diff), \
                    contextlib.redirect_stdout(io.StringIO()):
                with self.assertRaises(SystemExit) as result:
                    exec(compile(gate_source(name), str(WORKFLOW), "exec"), {})
        finally:
            os.chdir(previous)
        report = (self.front / "alpn-diff.json" if frontend else self.go / "delivery-coverage.json")
        return result.exception.code, json.loads(report.read_text())

    def test_go_no_changed_executable_statements_is_not_applicable(self):
        (self.go / "patch.json").write_text(json.dumps({"covered": 0, "total": 0, "percent": None}))
        code, report = self.run_gate()
        self.assertEqual(code, 0)
        self.assertFalse(report["patch"]["applicable"])
        self.assertIsNone(report["patch"]["percent"])
        self.assertEqual((report["base_sha"], report["head_sha"]), ("base", "head"))

    def test_go_strict_floor_remains_enforced(self):
        for covered, expected in ((89, 1), (90, 1), (91, 0)):
            with self.subTest(covered=covered):
                (self.go / "patch.json").write_text(json.dumps({"covered": covered, "total": 100, "percent": covered}))
                code, report = self.run_gate()
                self.assertEqual(code, expected)
                self.assertTrue(report["patch"]["applicable"])

    def test_go_missing_patch_still_fails(self):
        with self.assertRaises(FileNotFoundError):
            self.run_gate()

    def test_go_missing_or_empty_profiles_still_fail_without_a_code_diff(self):
        (self.go / "patch.json").write_text('{"covered":0,"total":0,"percent":null}')
        profile = self.go / "head-full.cover"
        profile.unlink()
        with self.assertRaises(FileNotFoundError):
            self.run_gate()
        profile.write_text("mode: atomic\n")
        with self.assertRaisesRegex(ValueError, "empty coverage profile"):
            self.run_gate()

    def test_frontend_no_changed_production_files_is_not_applicable(self):
        code, report = self.run_gate(frontend=True)
        self.assertEqual(code, 0)
        self.assertEqual((report["covered"], report["total"]), (0, 0))
        self.assertFalse(report["applicable"])
        self.assertIsNone(report["percent"])

    def test_frontend_strict_floor_remains_enforced(self):
        for covered, expected in ((89, 1), (90, 1), (91, 0)):
            with self.subTest(covered=covered):
                self.write_front(covered, 100)
                code, report = self.run_gate(frontend=True, diff=self.front_diff)
                self.assertEqual(code, expected)
                self.assertTrue(report["applicable"])

    def test_frontend_nonexecutable_changed_spans_are_not_applicable(self):
        code, report = self.run_gate(frontend=True, diff=self.front_diff.replace("10", "20"))
        self.assertEqual(code, 0)
        self.assertFalse(report["applicable"])

    def test_frontend_test_only_changes_are_not_production(self):
        code, report = self.run_gate(frontend=True, diff=self.front_diff.replace("/src/", "/src/test/"))
        self.assertEqual(code, 0)
        self.assertFalse(report["applicable"])

    def test_frontend_missing_changed_file_instrumentation_still_fails(self):
        with self.assertRaisesRegex(ValueError, "missing changed-file instrumentation"):
            self.run_gate(frontend=True, diff=self.front_diff.replace("feature.ts", "missing.ts"))

    def test_frontend_missing_or_empty_artifact_still_fails_without_a_code_diff(self):
        profile = self.front / "coverage-final.json"
        profile.unlink()
        with self.assertRaises(FileNotFoundError):
            self.run_gate(frontend=True)
        profile.write_text("{}")
        with self.assertRaisesRegex(ValueError, "empty frontend coverage instrumentation"):
            self.run_gate(frontend=True)


if __name__ == "__main__":
    unittest.main()
