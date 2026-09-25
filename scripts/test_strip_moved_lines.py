#!/usr/bin/env python3
"""Tests for scripts/strip_moved_lines.py."""
import importlib.util
import pathlib
import unittest

spec = importlib.util.spec_from_file_location(
    "strip_moved_lines", pathlib.Path(__file__).with_name("strip_moved_lines.py")
)
mod = importlib.util.module_from_spec(spec)
spec.loader.exec_module(mod)

DIFF = """diff --git a/a.go b/a.go
--- a/a.go
+++ b/a.go
@@ -10,3 +9,0 @@ func x() {
-func moved() int {
-	return compute(1)
-}
@@ -20,0 +20,1 @@
+	newStatement()
diff --git a/b.go b/b.go
--- /dev/null
+++ b/b.go
@@ -0,0 +1,5 @@
+package p
+func moved() int {
+	return compute(2)
+}
+
"""


def added(diff):
    out, cur = {}, None
    for line in diff.splitlines():
        if line.startswith("+++ b/"):
            cur = line[6:]
            out.setdefault(cur, set())
        elif line.startswith("@@") and cur:
            import re

            m = re.search(r"\+(\d+)(?:,(\d+))?", line)
            s, n = int(m.group(1)), int(m.group(2) or "1")
            out[cur].update(range(s, s + n))
    return out


class StripMovedTest(unittest.TestCase):
    def test_moved_lines_dropped_edited_and_new_kept(self):
        got = added(mod.strip_moved(DIFF))
        self.assertEqual(got["a.go"], {20})
        # "package p" is new, the edited return (compute(2)) is new, the blank
        # line has no text; the verbatim signature and brace are moved.
        self.assertEqual(got["b.go"], {1, 3, 5})

    def test_each_removed_line_matches_once(self):
        diff = "--- a/a.go\n+++ b/a.go\n@@ -1,1 +1,2 @@\n-x := 1\n+x := 1\n+x := 1\n"
        self.assertEqual(added(mod.strip_moved(diff))["a.go"], {2})

    def test_no_removals_is_identity_for_line_numbers(self):
        diff = "--- /dev/null\n+++ b/c.go\n@@ -0,0 +4,2 @@\n+a()\n+b()\n"
        self.assertEqual(added(mod.strip_moved(diff))["c.go"], {4, 5})

    def test_empty(self):
        self.assertEqual(mod.strip_moved(""), "")


if __name__ == "__main__":
    unittest.main()
