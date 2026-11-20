#!/usr/bin/env python3
"""Deterministic control-flow tests; gh verification is mocked, not cryptographic evidence."""

import hashlib
import importlib.util
import io
import json
import os
import subprocess
import tarfile
import tempfile
import unittest
import zipfile
from pathlib import Path
from unittest.mock import patch

import yaml

ROOT = Path(__file__).resolve().parent.parent
spec = importlib.util.spec_from_file_location("release_verifier", ROOT / "scripts/verify-release-packages.py")
verifier = importlib.util.module_from_spec(spec)
spec.loader.exec_module(verifier)

TAG = "v9.8.7-rc.1"
COMMIT = "a" * 40


class VerificationTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)
        self.events = []
        self.statements = {}
        self.failure = None

    def fixture(self, os_name="linux", profile="lean", contents=b"fixture"):
        name = f"jul_{TAG}_{os_name}_amd64_{profile}"
        archive = self.root / (name + (".zip" if os_name == "windows" else ".tar.gz"))
        files = {"jul.exe" if os_name == "windows" else "jul": contents,
                 "README.md": b"readme", "SECURITY.md": b"security",
                 "server.toml": b"config", "jul.spdx.json": b'{"spdxVersion":"SPDX-2.3"}',
                 "LICENSE": b"license"}
        if os_name == "windows":
            with zipfile.ZipFile(archive, "w") as package:
                for path, data in files.items():
                    package.writestr(f"{name}/{path}", data)
        else:
            with tarfile.open(archive, "w:gz") as package:
                for path, data in files.items():
                    member = tarfile.TarInfo(f"{name}/{path}")
                    member.size = len(data)
                    package.addfile(member, io.BytesIO(data))
        digest = hashlib.sha256(archive.read_bytes()).hexdigest()
        self.statements[digest] = name + (".zip" if os_name == "windows" else ".tar.gz")
        self.checksums(archive)
        return archive

    def checksums(self, archive):
        digest = hashlib.sha256(archive.read_bytes()).hexdigest()
        Path(str(archive) + ".sha256").write_text(digest)
        (self.root / "SHA256SUMS").write_text(f"{digest}  {archive.name}\n")

    def fake_command(self, *args, **kwargs):
        self.events.append(args)
        if args[0] == "gh":
            self.assertEqual(list(args[4:]), [
                "--repo", verifier.REPOSITORY, "--signer-workflow", verifier.WORKFLOW,
                "--source-ref", f"refs/tags/{TAG}", "--source-digest", COMMIT,
                "--deny-self-hosted-runners", "--predicate-type", args[args.index("--predicate-type") + 1],
            ] + (["--format", "json"] if "--format" in args else []))
            if self.failure:
                raise RuntimeError(self.failure)
            if "--format" in args:
                digest = hashlib.sha256(Path(args[3]).read_bytes()).hexdigest()
                name = self.statements.get(digest)
                if name is None:
                    raise RuntimeError("no valid evidence")
                return json.dumps([{"verificationResult": {"statement": {
                    "predicateType": verifier.PROVENANCE,
                    "subject": [{"name": name, "digest": {"sha256": digest}}]}}}])
            return ""
        if args[1] == "version":
            return json.dumps({"version": TAG, "commit": COMMIT, "os": self.os_name,
                               "arch": "amd64", "dirty": False})
        return json.dumps({"features": {"waf": self.profile == "full", "console": self.profile == "full"}})

    def verify(self, os_name="linux", profile="lean", **kwargs):
        self.os_name, self.profile = os_name, profile
        with patch.object(verifier, "command", side_effect=self.fake_command):
            return verifier.verify_archive(self.root, TAG, COMMIT, os_name, "amd64", profile, **kwargs)

    def test_both_formats_profiles_and_order(self):
        for os_name in ("linux", "windows"):
            for profile in ("lean", "full"):
                with self.subTest(os=os_name, profile=profile):
                    self.events.clear()
                    archive = self.fixture(os_name, profile)
                    binary, report = self.verify(os_name, profile)
                    self.assertTrue(binary.is_file())
                    self.assertEqual(report["archive_subject"], archive.name)
                    self.assertEqual(report["archive_provenance"], "passed")
                    self.assertEqual(report["binary_provenance"], "passed")
                    self.assertEqual(report["sbom"], "passed")
                    self.assertEqual([x[0] for x in self.events[:3]], ["gh", "gh", "gh"])
                    self.assertEqual(self.events[3][1], "version")

    def test_changed_and_sibling_with_replaced_checksums_stop_before_unpack(self):
        for sibling in (False, True):
            with self.subTest(sibling=sibling):
                requested = self.fixture()
                if sibling:
                    other = self.fixture(profile="full", contents=b"sibling")
                    requested.write_bytes(other.read_bytes())
                else:
                    requested.write_bytes(requested.read_bytes() + b"changed")
                self.checksums(requested)
                with patch.object(verifier, "unpack") as unpack:
                    with self.assertRaises(RuntimeError):
                        self.verify()
                    unpack.assert_not_called()
                self.assertTrue(all(event[0] == "gh" for event in self.events))

    def test_invalid_evidence_stops_before_unpack(self):
        self.fixture()
        for failure in ("missing evidence", "corrupt evidence", "wrong repository",
                        "wrong workflow", "wrong ref", "wrong source", "unsupported runner",
                        "gh unavailable"):
            with self.subTest(failure=failure), patch.object(verifier, "unpack") as unpack:
                self.failure = failure
                with self.assertRaisesRegex(RuntimeError, failure):
                    self.verify()
                unpack.assert_not_called()
        self.failure = None
        with patch.object(verifier, "command", return_value="not json"), patch.object(verifier, "unpack") as unpack:
            with self.assertRaises(json.JSONDecodeError):
                verifier.verify_archive(self.root, TAG, COMMIT, "linux", "amd64", "lean")
            unpack.assert_not_called()

    def test_name_and_digest_must_match_same_statement(self):
        archive = self.fixture()
        digest = hashlib.sha256(archive.read_bytes()).hexdigest()
        statements = [{"verificationResult": {"statement": {"predicateType": verifier.PROVENANCE,
                       "subject": [{"name": archive.name, "digest": {"sha256": "0" * 64}}]}}},
                      {"verificationResult": {"statement": {"predicateType": verifier.PROVENANCE,
                       "subject": [{"name": "sibling.tar.gz", "digest": {"sha256": digest}}]}}}]
        with patch.object(verifier, "command", return_value=json.dumps(statements)), patch.object(verifier, "unpack") as unpack:
            with self.assertRaisesRegex(RuntimeError, "subject mismatch"):
                verifier.verify_archive(self.root, TAG, COMMIT, "linux", "amd64", "lean")
            unpack.assert_not_called()

    def test_binary_verification_failure_stops_execution(self):
        self.fixture()
        original = self.fake_command
        def fail_binary(*args, **kwargs):
            if args[0] == "gh" and "--format" not in args:
                raise RuntimeError("binary evidence unavailable")
            return original(*args, **kwargs)
        with patch.object(verifier, "command", side_effect=fail_binary):
            with self.assertRaisesRegex(RuntimeError, "binary evidence"):
                verifier.verify_archive(self.root, TAG, COMMIT, "linux", "amd64", "lean")
        self.assertTrue(all(event[0] == "gh" for event in self.events))

    def test_historical_v220_digest_pins_auxiliary_files(self):
        with patch.dict(globals(), TAG="v2.2.0", COMMIT=verifier.HISTORICAL_RELEASES["v2.2.0"]):
            archive = self.fixture(profile="full")
            with patch.object(verifier, "unpack") as unpack:
                with self.assertRaisesRegex(RuntimeError, "historical archive digest mismatch"):
                    self.verify(profile="full", legacy="v2.2.0")
                unpack.assert_not_called()
            self.assertNotEqual(hashlib.sha256(archive.read_bytes()).hexdigest(),
                                verifier.V220_FULL_DIGESTS[("linux", "amd64")])

    def test_legacy_is_explicit_and_pinned(self):
        self.fixture()
        for legacy in ("v2.1.0", "v2.2.0", TAG):
            with self.subTest(legacy=legacy), patch.object(verifier, "unpack") as unpack:
                with self.assertRaisesRegex(RuntimeError, "pinned historical"):
                    self.verify(legacy=legacy)
                unpack.assert_not_called()
        historical_tag = "v2.1.0"
        historical_commit = verifier.HISTORICAL_RELEASES[historical_tag]
        with patch.object(verifier, "attest") as attest, patch.object(verifier, "unpack") as unpack:
            with self.assertRaises(FileNotFoundError):
                verifier.verify_archive(self.root, historical_tag, historical_commit, "linux", "amd64", "lean")
            attest.assert_not_called()
            unpack.assert_not_called()
        with patch.dict(globals(), TAG=historical_tag, COMMIT=historical_commit):
            self.fixture()
            binary, evidence = self.verify(legacy=historical_tag)
            self.assertTrue(binary.is_file())
            self.assertEqual(evidence["archive_provenance"], "unavailable (pinned historical release)")
            self.assertIsNone(evidence["archive_subject"])
            self.assertEqual(evidence["binary_provenance"], "passed")
            self.assertNotIn("--format", self.events[-4])


class WorkflowTests(unittest.TestCase):
    def test_release_matrix_and_required_archive_attestation(self):
        workflow = yaml.safe_load((ROOT / ".github/workflows/release.yml").read_text())
        build = workflow["jobs"]["build"]
        self.assertEqual(build["strategy"]["matrix"]["os"], ["linux", "windows", "darwin"])
        self.assertEqual(build["strategy"]["matrix"]["arch"], ["amd64", "arm64"])
        self.assertEqual(build["strategy"]["matrix"]["profile"], ["lean", "full"])
        steps = build["steps"]
        package = next(i for i, step in enumerate(steps) if step.get("id") == "pkg")
        archive = next(i for i, step in enumerate(steps) if step.get("name") == "Attest archive provenance")
        upload = next(i for i, step in enumerate(steps) if step.get("name") == "Upload build artifact")
        self.assertLess(package, archive)
        self.assertLess(archive, upload)
        self.assertEqual(steps[archive]["with"]["subject-path"], "${{ steps.pkg.outputs.archive }}")
        self.assertNotIn("continue-on-error", steps[archive])
        self.assertIn("${{ steps.pkg.outputs.archive }}", steps[upload]["with"]["path"])
        self.assertEqual(workflow["jobs"]["publish"]["needs"], "build")
        self.assertEqual(build["needs"], ["gate", "soak"])

    def test_v220_digest_pins_match_committed_evidence(self):
        record = json.loads((ROOT / "docs/audit/evidence/2026-10-12-deployment/journey-7-archives/archive-evidence.json").read_text())
        expected = {(entry["os"], entry["arch"]): entry["archive_sha256"]
                    for entry in record["profiles"] if entry["profile"] == "full"}
        self.assertEqual(verifier.V220_FULL_DIGESTS, expected)

    def test_dispatch_checks_tag_and_runs_current_verifier(self):
        workflow = yaml.safe_load((ROOT / ".github/workflows/release-package-verification.yml").read_text())
        steps = workflow["jobs"]["native-download"]["steps"]
        checkout = next(step for step in steps if step.get("uses", "").startswith("actions/checkout@"))
        self.assertNotIn("ref", checkout.get("with", {}))
        preflight = next(step["run"] for step in steps if step.get("name") == "Verify immutable source")
        self.assertIn("(-rc\\.[0-9]+)?", preflight)
        self.assertIn('refs/tags/$EXPECTED_TAG^{commit}', preflight)
        self.assertIn('"$EXPECTED_SOURCE"', preflight)
        self.assertIn('"$GITHUB_SHA"', preflight)
        with tempfile.TemporaryDirectory() as temporary:
            origin = Path(temporary) / "origin"
            checkout_dir = Path(temporary) / "checkout"
            subprocess.run(["git", "init", "-q", str(origin)], check=True)
            subprocess.run(["git", "-C", str(origin), "-c", "user.name=Test",
                            "-c", "user.email=test@example.invalid", "commit", "-q",
                            "--allow-empty", "-m", "fixture"], check=True)
            sha = subprocess.check_output(["git", "-C", str(origin), "rev-parse", "HEAD"], text=True).strip()
            for tag in ("v1.2.3", "v1.2.3-rc.1", "v1.2.3-beta.1"):
                subprocess.run(["git", "-C", str(origin), "tag", tag], check=True)
            subprocess.run(["git", "clone", "-q", "--no-tags", str(origin), str(checkout_dir)], check=True)
            for tag, source, expected in (("v1.2.3", sha, 0), ("v1.2.3-rc.1", sha, 0),
                                          ("v1.2.3-beta.1", sha, 1), ("v1.2.3", "0" * 40, 1)):
                with self.subTest(tag=tag, source=source):
                    result = subprocess.run(["bash", "-e", "-c", preflight], cwd=checkout_dir,
                                            env={**os.environ, "EXPECTED_TAG": tag,
                                                 "EXPECTED_SOURCE": source, "GITHUB_SHA": sha},
                                            capture_output=True)
                    self.assertEqual(result.returncode == 0, expected == 0, result.stderr.decode())

    def test_publish_requires_every_archive_and_matching_checksum(self):
        workflow = yaml.safe_load((ROOT / ".github/workflows/release.yml").read_text())
        script = next(step["run"] for step in workflow["jobs"]["publish"]["steps"]
                      if step.get("name") == "Aggregate checksums")
        with tempfile.TemporaryDirectory() as temporary:
            artifacts = Path(temporary) / "artifacts"
            artifacts.mkdir()
            for os_name in ("linux", "windows", "darwin"):
                for arch in ("amd64", "arm64"):
                    for profile in ("lean", "full"):
                        ext = "zip" if os_name == "windows" else "tar.gz"
                        archive = artifacts / f"jul_{TAG}_{os_name}_{arch}_{profile}.{ext}"
                        archive.write_bytes(archive.name.encode())
                        Path(str(archive) + ".sha256").write_text(hashlib.sha256(archive.read_bytes()).hexdigest())
            def run():
                return subprocess.run(["bash", "-c", script], cwd=temporary,
                                      env={**os.environ, "GITHUB_REF_NAME": TAG}, capture_output=True)
            self.assertEqual(run().returncode, 0)
            archive.unlink()
            self.assertNotEqual(run().returncode, 0)
            archive.write_bytes(b"modified")
            self.assertNotEqual(run().returncode, 0)



if __name__ == "__main__":
    unittest.main()
