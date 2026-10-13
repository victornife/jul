"""Regression fixtures for missing coverage, block overlap and merged profiles."""
import importlib.util
import tempfile
import unittest
from pathlib import Path

spec = importlib.util.spec_from_file_location("scorer", Path(__file__).with_name("check-added-go-coverage.py"))
scorer = importlib.util.module_from_spec(spec)
spec.loader.exec_module(scorer)


class AddedCoverageTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        (self.root / "tool.go").write_text("package main\nfunc f() {\nreturn\n}\n")
        self.diff = "+++ b/tool.go\n@@ -0,0 +3,1 @@\n+return\n"
        self.profile = "mode: atomic\njul/tool.go:2.1,4.1 2 0\n"

    def test_uncovered_block_fails_score(self):
        self.assertEqual(scorer.score(self.diff, [self.profile], self.root)["percent"], 0)

    def test_duplicate_profiles_aggregate_executions(self):
        result = scorer.score(self.diff, [self.profile, self.profile.replace("2 0", "2 1")], self.root)
        self.assertEqual((result["covered"], result["total"], result["percent"]), (2, 2, 100))

    def test_missing_changed_file_is_rejected(self):
        with self.assertRaisesRegex(ValueError, "no measured statements"):
            scorer.score(self.diff, ["mode: atomic\n"], self.root)

    def test_absolute_standalone_paths(self):
        profile = self.profile.replace("jul/tool.go", str(self.root / "tool.go")).replace("2 0", "2 1")
        self.assertEqual(scorer.score(self.diff, [profile], self.root)["percent"], 100)

    def test_comment_and_test_only_changes_are_not_production(self):
        (self.root / "tool.go").write_text("package main\n// comment\n")
        diff = "+++ b/tool.go\n@@ -0,0 +2,1 @@\n+// comment\n+++ b/tool_test.go\n@@ -0,0 +1,1 @@\n+panic()\n"
        self.assertIsNone(scorer.score(diff, ["mode: atomic\n"], self.root)["percent"])


if __name__ == "__main__":
    unittest.main()
