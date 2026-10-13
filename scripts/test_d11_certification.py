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

    def test_resolved_defect_preserves_original_review_and_failing_source(self):
        self.current["rows"][0].update(verdict="defect filed/linked", open_defects=[],
                                     resolved_defects=[{"issue": "https://github.com/victornife/jul/issues/1",
                                                        "corrective_pr": "https://github.com/victornife/jul/pull/2",
                                                        "reviewed_head": "a" * 40, "merge": "b" * 40}])
        self.assertEqual(self.errors(), [])

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
            "status": "completed", "reviewer": "assigned human", "source": "a" * 40,
            "result": "accepted", "sampled_rows": ["D11-001"], "review_kind": "maintainer-assigned-human", **changes}

    def test_only_actual_original_rows_count_toward_human_sample(self):
        for sample in (["D11-N001"], ["D11-999"], ["D11-001", "D11-N001"]):
            with self.subTest(sample=sample):
                self.review(sampled_rows=sample)
                self.assertTrue(any("original sample" in e for e in self.errors()))

    def test_completed_state_requires_accepted_review_but_ready_packet_does_not(self):
        self.current["state"] = "ready_for_review"
        self.assertEqual(self.errors(), [])
        self.current["state"] = "completed"
        self.assertTrue(any("accepted reviewer" in e for e in self.errors()))
        self.review(review_kind="agent-self-check")
        self.assertTrue(any("actual human" in e for e in self.errors()))
        self.review()
        self.assertEqual(self.errors(), [])

    def test_explicitly_authorized_ai_review_preserves_its_nonhuman_identity(self):
        self.current["state"] = "completed"
        self.review(reviewer="GitHub Copilot", review_kind="maintainer-authorized-ai",
                    independent_human_review=False,
                    maintainer_authorization={"maintainer": "victornife", "status": "approved",
                                             "reference": "https://github.com/victornife/jul/issues/515#issuecomment-1"})
        self.assertEqual(self.errors(), [])

    def test_ai_review_without_explicit_authorization_or_with_human_claim_is_rejected(self):
        approval = {"maintainer": "victornife", "status": "approved",
                    "reference": "https://github.com/victornife/jul/issues/515#issuecomment-1"}
        for changes in ({}, {"maintainer_authorization": None},
                        {"maintainer_authorization": {**approval, "status": "pending"}},
                        {"maintainer_authorization": {**approval, "maintainer": "unknown"}},
                        {"maintainer_authorization": {**approval, "reference": "unrecorded"}},
                        {"maintainer_authorization": {**approval, "reference": 515}},
                        {"maintainer_authorization": []},
                        {"independent_human_review": True}, {"independent_human_review": None}):
            with self.subTest(changes=changes):
                self.review(reviewer="GitHub Copilot", review_kind="maintainer-authorized-ai",
                            independent_human_review=False, maintainer_authorization=approval)
                if not changes:
                    self.current["reviewer_spot_check"].pop("maintainer_authorization")
                else:
                    self.current["reviewer_spot_check"].update(changes)
                self.assertTrue(any("explicit maintainer" in e for e in self.errors()))

    def test_authorized_ai_review_still_requires_original_sample_and_exact_source(self):
        approval = {"maintainer": "victornife", "status": "approved",
                    "reference": "https://github.com/victornife/jul/issues/515#issuecomment-1"}
        for changes in ({"sampled_rows": ["D11-N001"]}, {"source": "main"}):
            with self.subTest(changes=changes):
                self.review(reviewer="GitHub Copilot", review_kind="maintainer-authorized-ai",
                            independent_human_review=False, maintainer_authorization=approval, **changes)
                self.assertTrue(self.errors())

    def test_ai_identity_cannot_be_recorded_as_a_human_review(self):
        for kind in ("maintainer-human", "maintainer-assigned-human"):
            for identity in ({"reviewer": "GitHub Copilot"}, {"independent_human_review": False}):
                with self.subTest(kind=kind, identity=identity):
                    self.review(review_kind=kind, **identity)
                    self.assertTrue(any("nonhuman reviewer" in error for error in self.errors()))

    def test_freeze_and_companion_cannot_rewrite_the_original_path_together(self):
        name = "2026-10-13-d11-original.json"
        frozen = json.loads((self.audit / name).read_text())
        frozen["frozen_rows"][0]["original_path"] = "invented replacement"
        self.current["rows"][0]["original_path"] = "invented replacement"
        self.write(name, frozen)
        self.assertTrue(any("original historical ledger" in e for e in self.errors()))

    def test_freeze_and_companion_cannot_replace_an_original_id_together(self):
        name = "2026-10-13-d11-original.json"
        frozen = json.loads((self.audit / name).read_text())
        frozen["frozen_rows"][0]["id"] = "D11-999"
        self.current["rows"][0]["id"] = "D11-999"
        self.write(name, frozen)
        self.assertTrue(any("original row IDs" in e for e in self.errors()))

    def test_human_review_source_must_be_an_exact_git_identity(self):
        for source in ("main", "reviewed SHA", "a" * 8, {"sha": "a" * 40}):
            with self.subTest(source=source):
                self.review(source=source)
                self.assertTrue(any("full reviewed Git source" in e for e in self.errors()))

    def test_explicit_source_move_preserves_original_identity_and_passes(self):
        (self.root / "internal/config/schema.go").rename(self.root / "internal/config/renamed.go")
        self.current["rows"][0].update(current_paths=["internal/config/renamed.go"],
                                     implementation_and_authority=["internal/config/renamed.go"],
                                     path_reconciliation="Original source moved; frozen wildcard and reason retained")
        self.assertEqual(self.errors(), [])


if __name__ == "__main__":
    unittest.main()
