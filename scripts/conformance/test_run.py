import contextlib
from argparse import Namespace
import hashlib
import io
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

import run


class H2specResultsTests(unittest.TestCase):
    def test_runner_preserves_failures_and_records_actual_tool_and_outcomes(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            tool = root / "h2spec"
            tool.write_bytes(b"tested h2spec binary")
            version = "Version: 2.6.0+jul.early-response.1 (base+patch.hash)"

            def invoke(command, *, stdout, stderr):
                report = Path(command[command.index("-j") + 1])
                report.write_text(
                    '<testsuite><testcase package="http2/8.1.2.6" classname="early"/>'
                    '<testcase package="http2/8.1.2.6" classname="broken">'
                    '<failure>bare NO_ERROR</failure></testcase></testsuite>')
                stdout.write("CONTENT_LENGTH_RESULT stream=1 outcome=early_response\n")

            with (patch.object(run, "self_signed", return_value=(root / "cert", root / "key")),
                  patch.object(run, "static_root", return_value=root),
                  patch.object(run, "static_backend", return_value=contextlib.nullcontext(8080)),
                  patch.object(run, "run_jul", return_value=contextlib.nullcontext()),
                  patch.object(run.subprocess, "check_output", return_value=version + "\n") as identify,
                  patch.object(run.subprocess, "run", side_effect=invoke),
                  contextlib.redirect_stdout(io.StringIO())):
                status = run.suite_h2spec(Namespace(jul="tested-jul", h2spec=str(tool)), root)
            identify.assert_called_once_with([str(tool), "--version"], text=True)
            report = json.loads((root / "h2spec-verdict.json").read_text())
            self.assertEqual(status, 1)
            self.assertEqual(report["total"], 8)
            self.assertEqual(report["passed"], 4)
            self.assertEqual(len(report["unexpected_failures"]), 4)
            self.assertTrue(all(case["detail"] == "bare NO_ERROR" for case in report["unexpected_failures"]))
            self.assertEqual(report["tool"], {
                "version": version, "sha256": hashlib.sha256(tool.read_bytes()).hexdigest(),
            })
            for target in report["targets"].values():
                self.assertEqual(target, {
                    "total": 2, "passed": 1, "content_length_outcomes": ["early_response"],
                })


class VerdictTests(unittest.TestCase):
    def verdict(self, entries, results, evidence=None, suite="h2spec"):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / "allowlist.yaml").write_text(json.dumps({suite: entries}))
            with patch.object(run, "DATA", root), contextlib.redirect_stdout(io.StringIO()):
                status = run.judge(suite, results, root, evidence)
            return status, json.loads((root / f"{suite}-verdict.json").read_text())

    def test_exact_entry_becomes_stale(self):
        status, report = self.verdict([{"id": "case", "rationale": "known", "issue": "owner"}], {"case": (True, "")})
        self.assertEqual(status, 0)
        self.assertEqual(report["stale_allowlist_entries"], ["case"])

    def test_wildcard_becomes_stale_only_when_every_target_passes(self):
        entry = {"id": "case [*", "rationale": "known", "issue": "owner"}
        status, report = self.verdict([entry], {"case [tls]": (True, ""), "case [h2c]": (False, "error")})
        self.assertEqual(status, 0)
        self.assertEqual(report["stale_allowlist_entries"], [])
        self.assertEqual(len(report["allow_listed_failures"]), 1)
        status, report = self.verdict([entry], {"case [tls]": (True, ""), "case [h2c]": (True, "")})
        self.assertEqual(report["stale_allowlist_entries"], ["case [*"])

    def test_unexpected_case_is_not_hidden(self):
        status, report = self.verdict([], {"new case": (False, "error")}, {"targets": {"tls": {"passed": 0, "total": 1}}})
        self.assertEqual(status, 1)
        self.assertEqual(report["unexpected_failures"], [{"id": "new case", "detail": "error"}])
        self.assertEqual(report["targets"]["tls"]["total"], 1)

    def test_unexercised_entry_is_not_claimed_fixed(self):
        _, report = self.verdict([{"id": "case [*", "rationale": "known", "issue": "owner"}], {})
        self.assertEqual(report["stale_allowlist_entries"], [])

    def test_temporary_exception_requires_same_versions_and_failing_case(self):
        versions = {"github.com/quic-go/quic-go": "v0.63.0", "h3spec_sha256": "pinned"}
        entry = {"id": "known case", "rationale": "dependency", "issue": "owner",
                 "temporary": True, "verified_with": versions}
        scenarios = (
            ({"known case": (False, "error")}, {"versions": versions}, 0, False),
            ({"known case": (True, "")}, {"versions": versions}, 1, True),
            ({}, {"versions": versions}, 1, True),
            ({"known case": (False, "error")}, {"versions": {**versions, "github.com/quic-go/quic-go": "v0.64.0"}}, 1, True),
            ({"known case": (False, "error")}, {"versions": {**versions, "h3spec_sha256": "changed"}}, 1, True),
            ({"known case": (False, "error")}, None, 1, True),
            ({"known case": (False, "error"), "new case": (False, "error")}, {"versions": versions}, 1, False),
        )
        for results, evidence, expected_status, needs_review in scenarios:
            with self.subTest(results=results, evidence=evidence):
                status, report = self.verdict([entry], results, evidence, suite="h3")
                self.assertEqual(status, expected_status)
                self.assertEqual(bool(report["temporary_exceptions_needing_review"]), needs_review)
                self.assertEqual(report["passed"], sum(p for p, _ in results.values()))
                self.assertEqual(len(report["unexpected_failures"]), int("new case" in results))

    def test_temporary_exception_cannot_use_a_wildcard_or_omit_versions(self):
        for extra in ({"id": "case*", "verified_with": {"dependency": "v1"}}, {"id": "case"}):
            with self.subTest(extra=extra), self.assertRaises(SystemExit):
                self.verdict([{"rationale": "dependency", "issue": "owner", "temporary": True, **extra}], {})

    def test_cache_setup_retry_does_not_suppress_assertion_failures(self):
        entry = {"id": "cache-tests conditional-etag-vary-headers [setup-retry]",
                 "rationale": "known setup retry", "issue": "owner"}
        for value in (["Setup", "retry"], ["Assertion", "wrong Vary headers"],
                      ["Setup", "retry", "assertion failure"], ["Setup", "other"],
                      '["Setup", "retry"]', False):
            with self.subTest(value=value):
                case = run.cache_case_id("conditional-etag-vary-headers", value)
                known_retry = value == ["Setup", "retry"]
                self.assertEqual(case, entry["id"] if known_retry else "cache-tests conditional-etag-vary-headers")
                status, report = self.verdict([entry], {case: (False, json.dumps(value))}, suite="cache")
                self.assertEqual(status, 0 if known_retry else 1)
                self.assertEqual(len(report["allow_listed_failures"]), int(known_retry))
                self.assertEqual(len(report["unexpected_failures"]), int(not known_retry))
                self.assertEqual(report["total"], 1)

    def test_passing_cache_case_marks_setup_retry_entry_stale(self):
        entry = {"id": "cache-tests conditional-etag-vary-headers [setup-retry]",
                 "rationale": "known setup retry", "issue": "owner"}
        case = run.cache_case_id("conditional-etag-vary-headers", True)
        status, report = self.verdict([entry], {case: (True, "true")}, suite="cache")
        self.assertEqual(status, 0)
        self.assertEqual(report["stale_allowlist_entries"], [entry["id"]])
        self.assertEqual(report["total"], 1)
        self.assertEqual(report["passed"], 1)


class H3specResultsTests(unittest.TestCase):
    def test_versions_use_tested_binary_and_notice_replacements(self):
        metadata = ("jul: go1.27.2\n"
                    "\tdep\tgithub.com/quic-go/quic-go\tv0.63.0\th1:checksum\n"
                    "\t=>\texample.test/quic-fork\tv0.1.0\th1:other\n"
                    "\tdep\tgithub.com/quic-go/qpack\tv0.6.0\th1:checksum\n"
                    "\tdep\tother/dependency\tv1.0.0\th1:checksum\n")
        with tempfile.TemporaryDirectory() as directory:
            tool = Path(directory) / "h3spec"
            tool.write_bytes(b"test binary")
            with patch.object(run.subprocess, "check_output", return_value=metadata) as command:
                versions = run.h3_versions("tested-jul", str(tool))
            command.assert_called_once_with(["go", "version", "-m", "tested-jul"], text=True)
            self.assertEqual(versions, {
                "github.com/quic-go/quic-go": "v0.63.0 => example.test/quic-fork v0.1.0 h1:other",
                "github.com/quic-go/qpack": "v0.6.0",
                "h3spec_sha256": hashlib.sha256(b"test binary").hexdigest(),
            })

    def test_ansi_status_and_duplicate_descriptions_preserve_each_example(self):
        output = ("QUIC servers\n"
                  "  same case [\x1b[32m✔\x1b[0m]\n"
                  "  same case [\x1b[31m✘\x1b[0m]\n"
                  "HTTP/3 servers\n"
                  "Warning: 0-RTT is not possible. Skipping this test.\n"
                  "  good case [\x1b[32m✔\x1b[0m]\n"
                  "Failures:\n\n  1) QUIC servers same case\n"
                  "3 examples, 1 failure\n")
        results = run.h3spec_results(output, 1)
        self.assertEqual(len(results), 3)
        self.assertTrue(results["h3spec QUIC servers: same case"][0])
        self.assertFalse(results["h3spec QUIC servers: same case [2]"][0])
        self.assertTrue(results["h3spec HTTP/3 servers: good case"][0])

    def test_incomplete_or_inconsistent_output_cannot_pass(self):
        for output, code in (("", 0), ("QUIC servers\n  case [✔]\n2 examples, 0 failures\n", 0),
                             ("QUIC servers\n  case [✔]\n1 example, 1 failure\n", 1),
                             ("QUIC servers\n  case [✔]\n1 example, 0 failures\n", 1)):
            with self.subTest(output=output, code=code):
                self.assertFalse(run.h3spec_results(output, code)["h3spec run"][0])


if __name__ == "__main__":
    unittest.main()
