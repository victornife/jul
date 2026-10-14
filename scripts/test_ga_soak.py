"""Deterministic rejection tests for #590's evidence acceptance boundary."""

import importlib.util
import json
import hashlib
from pathlib import Path
import tarfile
import tempfile
import unittest
from unittest.mock import Mock, patch

SPEC = importlib.util.spec_from_file_location("ga_soak", Path(__file__).with_name("ga-soak.py"))
SOAK = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(SOAK)
REVIEW_SPEC = importlib.util.spec_from_file_location("ga_review", Path(__file__).with_name("ga-soak-review.py"))
REVIEW = importlib.util.module_from_spec(REVIEW_SPEC)
REVIEW_SPEC.loader.exec_module(REVIEW)


class AcceptanceTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.frozen_directory = tempfile.TemporaryDirectory(prefix="jul-frozen-contract-")
        bundle = Path(__file__).resolve().parents[1] / "docs/audit/evidence/2026-10-14-ga590/executed-harness.tar.gz"
        with tarfile.open(bundle) as archive:
            data = archive.extractfile("ga-soak.py").read()
        assert hashlib.sha256(data).hexdigest() == REVIEW.FROZEN_HARNESS
        path = Path(cls.frozen_directory.name) / "ga-soak.py"
        path.write_bytes(data)
        spec = importlib.util.spec_from_file_location("frozen_contract", path)
        cls.frozen = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(cls.frozen)

    @classmethod
    def tearDownClass(cls):
        cls.frozen_directory.cleanup()

    def setUp(self):
        self.manifest = {"seconds": 28800}
        values = {
            "process_resident_memory_bytes": 100 << 20,
            "go_memstats_heap_inuse_bytes": 20 << 20,
            "go_goroutines": 50,
            "process_open_fds": 20,
            'jul_upstream_active_requests{pool="clean"}': 0,
            'jul_upstream_pending_requests{pool="clean"}': 0,
        }
        self.samples = [{"elapsed": second, "metrics": dict(values)} for second in range(0, 28801, 10)]
        self.events = [{"elapsed": second, "name": phase, "ok": True}
                       for second in range(0, 28801, 60) for phase in SOAK.REQUIRED]
        self.quiet = [{"elapsed": second, "metrics": dict(values)} for second in (28900, 28910, 28920)]

    def evaluate(self):
        actual = SOAK.evaluate(self.manifest, self.samples, self.events, self.quiet)
        expected = self.frozen.evaluate(self.manifest, self.samples, self.events, self.quiet)
        self.assertEqual(actual, expected, "maintained contract differs from the frozen executed gates")
        return actual

    def test_complete_fixed_contract_is_candidate_not_automatic_promotion(self):
        result = self.evaluate()
        self.assertTrue(result["passed"], result)
        self.assertTrue(result["criterion_5_candidate"])
        self.assertFalse(result["automatic_promotion"])

    def test_every_required_phase_is_independently_mandatory(self):
        original = self.events
        for phase in SOAK.REQUIRED:
            with self.subTest(phase=phase):
                self.events = [event for event in original if event["name"] != phase]
                self.assertIn(f"missing phase: {phase}", self.evaluate()["errors"])
        self.events = original

    def test_start_end_only_does_not_prove_continuous_phase(self):
        self.events = [event for event in self.events if event["name"] != "routing"
                       or event["elapsed"] in (0, 28800)]
        self.assertIn("phase not continuously repeated: routing", self.evaluate()["errors"])

    def test_early_stop_cannot_qualify(self):
        self.samples.pop()
        self.assertFalse(self.evaluate()["criterion_5_candidate"])

    def test_sample_gap_and_nonmonotonic_recording_rejected(self):
        self.samples[3]["elapsed"] = 60
        self.assertIn("recording gap/non-monotonic time", self.evaluate()["errors"])

    def test_unexpected_fault_rejected_even_with_all_successes(self):
        self.events.append({"elapsed": 100, "name": "failure", "ok": False})
        self.assertIn("unexpected workload failure", self.evaluate()["errors"])

    def test_short_preflight_never_becomes_eight_hour_candidate(self):
        self.manifest["seconds"] = 120
        self.samples = self.samples[:13]
        self.events = [event for event in self.events if event["elapsed"] <= 120]
        result = self.evaluate()
        self.assertTrue(result["passed"], result)
        self.assertFalse(result["criterion_5_candidate"])
        self.assertFalse(result["resource_trend_assessed"])

    def test_peak_limits_are_exact(self):
        for key, limit in (("process_resident_memory_bytes", SOAK.LIMITS["rss_growth_bytes"]),
                           ("go_memstats_heap_inuse_bytes", SOAK.LIMITS["heap_growth_bytes"]),
                           ("go_goroutines", SOAK.LIMITS["goroutine_growth"])):
            with self.subTest(key=key):
                original = self.samples[100]["metrics"][key]
                self.samples[100]["metrics"][key] = original + limit + 1
                self.assertIn(f"post-warmup growth: {key}", self.evaluate()["errors"])
                self.samples[100]["metrics"][key] = original

    def test_rss_slope_not_just_peak_is_enforced(self):
        for sample in self.samples:
            sample["metrics"]["process_resident_memory_bytes"] += sample["elapsed"] / 3600 * (17 << 20)
        self.assertIn("RSS trend exceeded/unavailable", self.evaluate()["errors"])

    def test_cardinality_growth_is_bounded(self):
        self.samples[-1]["metrics"].update({f"unexpected_{index}": 1 for index in range(129)})
        self.assertIn("metric cardinality exceeded", self.evaluate()["errors"])

    def test_quiet_evidence_and_admission_release_required(self):
        self.quiet[-1]["metrics"]['jul_upstream_active_requests{pool="clean"}'] = 1
        self.assertIn("quiescent admission leak", self.evaluate()["errors"])
        self.quiet = []
        self.assertIn("final quiescent evidence missing", self.evaluate()["errors"])

    def test_quiet_fd_growth_limit(self):
        self.quiet[-1]["metrics"]["process_open_fds"] = 29
        self.assertIn("quiescent FD growth", self.evaluate()["errors"])

    def test_empty_samples_never_pass(self):
        self.samples = []
        self.assertFalse(self.evaluate()["passed"])

    def test_missing_and_nonfinite_resource_metrics_fail(self):
        with self.assertRaisesRegex(RuntimeError, "missing/invalid metric"):
            SOAK.metrics(b'go_goroutines 1\n')
        with self.assertRaisesRegex(RuntimeError, "missing/invalid metric"):
            SOAK.metrics(b'process_resident_memory_bytes NaN\n')

    def test_refuses_existing_evidence_without_modifying_it(self):
        with tempfile.TemporaryDirectory() as directory:
            class Args:
                out = Path(directory)
            marker = Path(directory) / "failure.json"
            marker.write_text("original")
            with self.assertRaisesRegex(RuntimeError, "overwrite"):
                SOAK.Run(Args()).execute()
            self.assertEqual(marker.read_text(), "original")

    def test_config_keeps_admin_private_and_secrets_referenced(self):
        ports = {name: 10000 + index for index, name in enumerate((
            "admin", "http", "grpc", "l4", "l4fault", "grpc_backend", "backend_a",
            "backend_b", "auth", "auth_dns", "control", "forbidden", "tcp_backend"))}
        raw = SOAK.configuration(ports, Path("/private"))
        self.assertIn('token = "${env:JUL_GA_SOAK_TOKEN}"', raw)
        self.assertNotIn("0.0.0.0", raw)
        self.assertEqual(raw.count("[upstreams.resilience]"), 10)

    def test_health_fixture_changes_actual_probe_response(self):
        fixture = SOAK.Fixture()
        self.addCleanup(fixture.close)
        address = "127.0.0.1:" + str(fixture.http("127.0.0.1", "backend_b"))
        self.assertEqual(SOAK.request(address, "/health")[0], 200)
        fixture.healthy = False
        self.assertEqual(SOAK.request(address, "/health")[0], 503)
        self.assertEqual(SOAK.request(address, "/clean/")[0], 200)
        fixture.healthy = True
        self.assertEqual(SOAK.request(address, "/health")[0], 200)

    def test_scrape_overhead_does_not_accumulate_in_scheduling(self):
        with tempfile.TemporaryDirectory() as directory:
            class Args:
                out = Path(directory) / "run"
                seconds = 60

            clock = [0.0]
            observed = []
            run = SOAK.Run(Args())
            run.long_tcp = Mock()
            stopped = [False]
            run.halt = Mock()
            run.halt.is_set.side_effect = lambda: stopped[0]
            run.halt.set.side_effect = lambda: stopped.__setitem__(0, True)
            run.halt.wait.side_effect = lambda seconds: clock.__setitem__(0, clock[0] + seconds)

            def prepare():
                run.out.mkdir()
                run.start = clock[0]
                run.manifest = {"seconds": 60}

            def sample(quiet=False):
                if not quiet:
                    observed.append(clock[0])
                clock[0] += 0.25

            with patch.object(run, "prepare", prepare), patch.object(run, "sample", sample), \
                    patch.object(run, "profile"), patch.object(SOAK.time, "monotonic", lambda: clock[0]), \
                    patch.object(SOAK.time, "sleep", lambda seconds: clock.__setitem__(0, clock[0] + seconds)), \
                    patch.object(SOAK.threading, "Thread") as thread, \
                    patch.object(SOAK.signal, "signal"), \
                    patch.object(SOAK, "evaluate", return_value={"passed": True, "errors": []}):
                thread.return_value.is_alive.return_value = False
                run.execute()
            self.assertEqual(observed, [0, 10, 20, 30, 40, 50, 60])

    def test_supplement_rejects_wrong_process_source_or_quiet_samples(self):
        with tempfile.TemporaryDirectory() as directory:
            manifest_path = Path(directory) / "manifest.json"
            manifest_path.write_text("{}")
            primary = {"harness_sha256": "harness", "binary_sha256": "binary", "jul_pid": 123,
                       "seconds": 28800, "host": {"boot_id": "boot"},
                       "package": {"metadata": {"commit": "source"}}}
            metadata = {"primary_manifest_sha256": REVIEW.digest(manifest_path),
                        "primary_harness_sha256": "harness", "binary_sha256": "binary", "source_sha": "source",
                        "process": {"pid": 123, "boot_id": "boot"}, "origin_boottime": 100,
                        "original_evidence_modified": False}
            first = {"boottime": 100, "elapsed": 0}
            samples = [{"collector": "supplemental", "jul_pid": 123, "quiet": False, "elapsed": 200,
                        "boottime": 300, "walltime": 500}]
            REVIEW.validate_supplement(primary, manifest_path, first, metadata, samples)
            for key, wrong in (("source_sha", "other"), ("binary_sha256", "other"),
                               ("original_evidence_modified", True), ("origin_boottime", 101)):
                with self.subTest(key=key):
                    with self.assertRaises(RuntimeError):
                        REVIEW.validate_supplement(primary, manifest_path, first, dict(metadata, **{key: wrong}), samples)
            with self.assertRaisesRegex(RuntimeError, "outside active"):
                REVIEW.validate_supplement(primary, manifest_path, first, metadata,
                                          [dict(samples[0], quiet=True)])
            with self.assertRaisesRegex(RuntimeError, "record identity"):
                REVIEW.validate_supplement(primary, manifest_path, first, metadata,
                                          [dict(samples[0], jul_pid=456)])
            with self.assertRaisesRegex(RuntimeError, "gap/duplicate"):
                REVIEW.validate_supplement(primary, manifest_path, first, metadata, samples * 2)

    def test_archive_verification_rejects_modified_files(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            artifact = root / "sample.json"
            artifact.write_text("{}")
            (root / "digests.json").write_text(json.dumps({"sample.json": REVIEW.digest(artifact)}))
            REVIEW.verify_files(root)
            artifact.write_text('{"modified":true}')
            with self.assertRaisesRegex(RuntimeError, "digest mismatch"):
                REVIEW.verify_files(root)


if __name__ == "__main__":
    unittest.main()
