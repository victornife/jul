import contextlib
import io
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

import run


class VerdictTests(unittest.TestCase):
    def verdict(self, entries, results, evidence=None):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / "allowlist.yaml").write_text(json.dumps({"h2spec": entries}))
            with patch.object(run, "DATA", root), contextlib.redirect_stdout(io.StringIO()):
                status = run.judge("h2spec", results, root, evidence)
            return status, json.loads((root / "h2spec-verdict.json").read_text())

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


if __name__ == "__main__":
    unittest.main()