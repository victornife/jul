#!/usr/bin/env python3
"""Exercise actual shell harness failure propagation and profile consumers."""
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import sys
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[1]
FAKE_GO = '''#!/usr/bin/env python3
import json, os, sys
from pathlib import Path
args=sys.argv[1:]
path=Path(os.environ["JUL_HARNESS_CALLS"])
previous=[json.loads(line) for line in path.read_text().splitlines()] if path.exists() else []
with path.open("a") as stream: stream.write(json.dumps(args)+"\\n")
mode=os.environ["JUL_HARNESS_MODE"]
if args[0]=="list":
 if mode=="package-error": print("synthetic package listing failure",file=sys.stderr);sys.exit(42)
 print("jul/internal/synthetic")
elif "-list" in args:
 if mode=="target-error": print("synthetic target compilation failure",file=sys.stderr);sys.exit(43)
 if mode!="no-targets": print("FuzzSynthetic")
 print("ok jul/internal/synthetic")
elif "-fuzz" in args:
 number=1+sum("-fuzz" in call for call in previous)
 if mode=="persistent-error" or (mode=="retry-once" and number==1): sys.exit(1)
'''


class HarnessContracts(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory(prefix="jul-harness-tests-")
        self.addCleanup(self.tmp.cleanup)
        self.base = Path(self.tmp.name)
        self.bin = self.base / "bin"
        self.bin.mkdir()
        go = self.bin / "go"
        go.write_text(FAKE_GO)
        go.chmod(0o755)
        self.calls = self.base / "calls.jsonl"
        self.tags = re.search(r"^FULL_TAGS\s*:=\s*(.+)$", (ROOT / "Makefile").read_text(), re.M)[1]

    def invoke(self, script, mode="success", args=(), override=None):
        env = dict(os.environ, PATH=str(self.bin)+":"+os.environ["PATH"],
                   JUL_HARNESS_CALLS=str(self.calls), JUL_HARNESS_MODE=mode,
                   FUZZTIME="1ms", BENCHTIME="1x")
        env.pop("BENCH_TAGS", None)
        env.pop("FUZZ_TAGS", None)
        if override is not None:
            env["BENCH_TAGS" if script == "bench.sh" else "FUZZ_TAGS"] = override
        result = subprocess.run(["bash", str(ROOT / "scripts" / script), *args],
                                env=env, text=True, capture_output=True, timeout=10)
        calls = [json.loads(line) for line in self.calls.read_text().splitlines()]
        return result, calls

    def test_module_package_listing_failure_fails_before_fuzz(self):
        result, calls = self.invoke("fuzz.sh", "package-error")
        self.assertEqual(result.returncode, 42, result.stdout+result.stderr)
        self.assertIn("synthetic package listing failure", result.stderr)
        self.assertEqual(len(calls), 1)

    def test_explicit_package_target_listing_failure_fails(self):
        result, calls = self.invoke("fuzz.sh", "target-error", ("./synthetic",))
        self.assertEqual(result.returncode, 43, result.stdout+result.stderr)
        self.assertIn("synthetic target compilation failure", result.stderr)
        self.assertEqual(len(calls), 1)

    def test_successful_package_without_targets_is_allowed(self):
        result, calls = self.invoke("fuzz.sh", "no-targets")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertFalse(any("-fuzz" in call for call in calls))

    def test_bounded_retry_recovers_without_changing_profile(self):
        result, calls = self.invoke("fuzz.sh", "retry-once")
        self.assertEqual(result.returncode, 0, result.stdout+result.stderr)
        attempts = [call for call in calls if "-fuzz" in call]
        self.assertEqual(len(attempts), 2)
        self.assertEqual(attempts[0], attempts[1])
        self.assertEqual(attempts[0][attempts[0].index("-tags")+1], self.tags)
        self.assertEqual(attempts[0][attempts[0].index("-fuzztime")+1], "1ms")

    def test_two_fuzz_failures_fail_the_harness(self):
        result, calls = self.invoke("fuzz.sh", "persistent-error")
        self.assertEqual(result.returncode, 1, result.stdout+result.stderr)
        self.assertEqual(sum("-fuzz" in call for call in calls), 2)

    def test_benchmark_default_executes_complete_profile(self):
        result, calls = self.invoke("bench.sh", args=("./internal/waf",))
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(len(calls), 1)
        self.assertEqual(calls[0][calls[0].index("-tags")+1], self.tags)
        self.assertEqual(calls[0][-1], "./internal/waf")

    def test_explicit_profile_overrides_are_preserved(self):
        for script in ("bench.sh", "fuzz.sh"):
            with self.subTest(script=script):
                self.calls.unlink(missing_ok=True)
                result, calls = self.invoke(script, override="grpc")
                self.assertEqual(result.returncode, 0, result.stdout+result.stderr)
                self.assertTrue(calls)
                for call in calls:
                    self.assertEqual(call[call.index("-tags")+1], "grpc")

    def test_guard_rejects_each_real_consumer_drift(self):
        clone = self.base / "clone"
        for path in ("Makefile", ".github/workflows/ci.yml", "scripts/bench.sh",
                     "scripts/fuzz.sh", "scripts/check-full-tags-sync.py"):
            target = clone / path
            target.parent.mkdir(parents=True, exist_ok=True)
            shutil.copyfile(ROOT / path, target)
        def check():
            return subprocess.run([sys.executable, str(clone / "scripts/check-full-tags-sync.py")],
                                  text=True, capture_output=True, timeout=10)
        self.assertEqual(check().returncode, 0)
        for name in ("bench.sh", "fuzz.sh"):
            target = clone / "scripts" / name
            source = target.read_text()
            for changed in (source.replace(" kubernetes waf\"", " kubernetes\""),
                            re.sub(r'^DEFAULT_TAGS=.*$', '', source, flags=re.M)):
                target.write_text(changed)
                result = check()
                self.assertNotEqual(result.returncode, 0)
                self.assertIn(name, result.stderr)
            target.write_text(source)


if __name__ == "__main__":
    unittest.main(verbosity=2)
