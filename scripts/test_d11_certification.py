#!/usr/bin/env python3
"""Contradiction fixtures for the frozen D11 evidence contract."""
import hashlib
import importlib.util
import json
from pathlib import Path
import tempfile
import unittest

SPEC = importlib.util.spec_from_file_location("d11", Path(__file__).with_name("check-d11-certification.py"))
D11 = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(D11)


class DispositionContract(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)
        self.audit = self.root / "docs/audit"
        self.audit.mkdir(parents=True)
        (self.root / "internal/config").mkdir(parents=True)
        (self.root / "cmd").mkdir()
        (self.root / "internal/config/schema.go").write_text("package config\n")
        (self.root / "internal/config/schema_test.go").write_text("package config\nfunc TestKnown(t *testing.T) {}\n")
        original = self.audit / "coverage.tsv"
        original.write_text("path_or_surface\tdisposition\treason_and_limit\ninternal/config/**\tnot reviewed\told scope\n")
        frozen = {"id": "D11-001", "original_path": "internal/config/**", "original_disposition": "not reviewed",
                  "original_reason_and_limit": "old scope", "current_paths": ["internal/config/schema.go"]}
        self.write("2026-10-13-d11-original.json", {"sha256": hashlib.sha256(original.read_bytes()).hexdigest(),
                   "original_ledger": "docs/audit/coverage.tsv", "original_total": 1, "original_unreviewed": 1,
                   "frozen_rows": [frozen]})
        (self.audit / "run.txt").write_text("passed: source/profile identified\n")
        self.current = {"rows": [{**frozen, "responsible_owner": "config owner", "review_boundary": "config defaults",
                    "status": "reviewed", "verdict": "correct", "claims": ["fixed default"], "implementation_and_authority": ["internal/config/schema.go"],
                    "named_tests": ["TestKnown"], "execution_evidence": ["run.txt"], "limitations": ["not production host"],
                    "establishing_pr": "https://github.com/victornife/jul/pull/1"}], "new_rows": [],
                    "reviewer_spot_check": {"status": "pending"}}

    def write(self, name, value):
        (self.audit / name).write_text(json.dumps(value))

    def errors(self, complete=True):
        self.write("2026-10-13-d11-certification.json", self.current)
        return D11.check(self.root, complete)

    def test_source_backed_row_passes_without_fabricating_reviewer_completion(self):
        self.assertEqual(self.errors(), [])

    def test_historical_reason_and_denominator_cannot_be_rewritten(self):
        self.current["rows"][0]["original_reason_and_limit"] = "new generic reviewed wording"
        self.assertTrue(any("identity/reason" in e for e in self.errors()))
        self.current["rows"] = []
        self.assertTrue(any("denominator" in e for e in self.errors()))

    def test_wildcard_narrowing_requires_explicit_reconciliation(self):
        self.current["rows"][0]["current_paths"] = []
        self.assertTrue(any("wildcard" in e for e in self.errors()))

    def test_nonexistent_test_or_missing_evidence_is_rejected(self):
        self.current["rows"][0]["named_tests"] = ["TestInvented"]
        self.current["rows"][0]["execution_evidence"] = ["missing.txt"]
        errors = self.errors()
        self.assertTrue(any("nonexistent named test" in e for e in errors))
        self.assertTrue(any("missing execution evidence" in e for e in errors))

    def test_reviewed_defect_needs_a_filed_owner(self):
        self.current["rows"][0]["verdict"] = "defect filed/linked"
        self.assertTrue(any("owner link" in e for e in self.errors()))

    def test_pending_area_allowed_for_area_pr_but_not_final_packet(self):
        self.current["rows"][0]["verdict"] = None
        self.assertEqual(self.errors(complete=False), [])
        self.assertTrue(any("final disposition" in e for e in self.errors()))

    def test_completion_cannot_relabel_an_agent_check_as_a_reviewer_sample(self):
        self.current["reviewer_spot_check"] = {"status": "completed", "reviewer": None, "sampled_rows": []}
        self.assertTrue(any("spot-check" in e for e in self.errors()))

    def test_unknown_original_id_cannot_replace_a_row_at_same_count(self):
        self.current["rows"][0]["id"] = "D11-999"
        self.assertTrue(any("denominator" in e for e in self.errors()))

    def test_supplemental_ids_cannot_duplicate_or_overlap_original_ids(self):
        row = dict(self.current["rows"][0], id="D11-N001")
        self.current["new_rows"] = [row, dict(row)]
        self.assertTrue(any("Supplemental" in e for e in self.errors()))
        self.current["new_rows"] = [dict(row, id="D11-001")]
        self.assertTrue(any("Supplemental" in e for e in self.errors()))

    def test_missing_or_escaping_current_paths_are_rejected(self):
        for path in ("missing.md", "../outside", "/absolute"):
            with self.subTest(path=path):
                self.current["rows"][0].update(current_paths=[path], path_reconciliation="moved")
                self.assertTrue(any("invalid current path" in e for e in self.errors()))
        self.current["rows"][0].update(current_paths=[], path_reconciliation="")
        self.assertTrue(any("current paths missing" in e for e in self.errors()))

    def test_pending_pr_and_row_status_do_not_establish_final_disposition(self):
        self.current["rows"][0].update(establishing_pr="pending draft", status="pending")
        errors = self.errors()
        self.assertTrue(any("concrete reviewable PR" in e for e in errors))
        self.assertTrue(any("nonfinal row status" in e for e in errors))

    def review(self, **changes):
        self.current["reviewer_spot_check"] = {
            "status": "completed", "reviewer": "assigned human", "source": "reviewed SHA",
            "result": "accepted", "sampled_rows": ["D11-001"], "review_kind": "maintainer-assigned-human", **changes}

    def test_only_actual_original_rows_count_toward_human_sample(self):
        for sample in (["D11-N001"], ["D11-999"], ["D11-001", "D11-N001"]):
            with self.subTest(sample=sample):
                self.review(sampled_rows=sample)
                self.assertTrue(any("original sample" in e for e in self.errors()))

    def test_completed_state_requires_human_review_but_ready_packet_does_not(self):
        self.current["state"] = "ready_for_review"
        self.assertEqual(self.errors(), [])
        self.current["state"] = "completed"
        self.assertTrue(any("actual human" in e for e in self.errors()))
        self.review(review_kind="agent-self-check")
        self.assertTrue(any("actual human" in e for e in self.errors()))
        self.review()
        self.assertEqual(self.errors(), [])


if __name__ == "__main__":
    unittest.main()
