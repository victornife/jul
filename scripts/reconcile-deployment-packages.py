#!/usr/bin/env python3
"""Reconcile existing native release evidence; never claim a new native run."""

import argparse
import hashlib
import json
from pathlib import Path
import tempfile

from deployment_lab import command, write_report


SOURCE = "b12e051139f652b400cff37d9f063f5637a7d550"
TAG_OBJECT = "9115edc5961b92526b9ec76b2c752bc146ce3523"
REPORT_SHA256 = "ed67d54f2da3d7a262cfea2ade6d169f6b5cfe3d6166519554322b3d7791b516"


def reconcile(report, release, scope):
    assert report["release"] == "v2.2.0" and report["source"] == SOURCE and report["tag_object"] == TAG_OBJECT
    assert report["verification_workflow_run"] == 38185464698
    expected = {(os, arch) for os in ("linux", "windows", "darwin") for arch in ("amd64", "arm64")}
    actual = [(item["os"], item["arch"]) for item in report["native_reports"]]
    assert len(actual) == 6 and set(actual) == expected, "native matrix is missing or duplicates a platform"
    assets = {item["name"]: item for item in release["assets"]}
    profiles = []
    for native in report["native_reports"]:
        assert native["native"] is True and native["source"] == SOURCE and native["tag"] == "v2.2.0"
        assert set(native["profiles"]) == {"lean", "full"}
        for profile, evidence in native["profiles"].items():
            metadata = evidence["metadata"]
            assert metadata["commit"] == SOURCE and metadata["version"] == "v2.2.0" and metadata["dirty"] is False
            assert (metadata["os"], metadata["arch"]) == (native["os"], native["arch"])
            for key in ("checksums", "provenance", "sbom", "previous", "upgrade", "rollback"):
                assert evidence[key] == "passed", f"unverified native result: {key}"
            assert assets[evidence["archive"]]["digest"] == "sha256:" + evidence["sha256"], "published asset identity changed"
            if scope == "all" or native["os"] == "darwin":
                profiles.append({"os": native["os"], "arch": native["arch"], "profile": profile,
                                 "archive_sha256": evidence["sha256"], "verdict": "passed-existing-native-evidence"})
    assert len(profiles) == (12 if scope == "all" else 4)
    return profiles


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--scope", choices=("macos", "all"), required=True)
    parser.add_argument("--report", type=Path, required=True)
    args = parser.parse_args()
    reference = json.loads(command("gh", "api", "repos/victornife/jul/git/ref/tags/v2.2.0"))
    assert reference["object"]["sha"] == TAG_OBJECT and reference["object"]["type"] == "tag"
    tag = json.loads(command("gh", "api", "repos/victornife/jul/git/tags/" + TAG_OBJECT))
    assert tag["object"]["sha"] == SOURCE and tag["object"]["type"] == "commit"
    release = json.loads(command("gh", "api", "repos/victornife/jul/releases/tags/v2.2.0"))
    assert not release["draft"] and not release["prerelease"]
    with tempfile.TemporaryDirectory() as temporary:
        command("gh", "release", "download", "v2.2.0", "--repo", "victornife/jul", "--pattern",
                "package-verification.json", "--dir", temporary)
        raw = (Path(temporary) / "package-verification.json").read_bytes()
        assert hashlib.sha256(raw).hexdigest() == REPORT_SHA256, "native evidence report changed"
        report = json.loads(raw)
        profiles = reconcile(report, release, args.scope)
    write_report(args.report, {"journey": "macos" if args.scope == "macos" else "archives",
        "evidence_class": "reconciled-existing-native-evidence", "new_native_execution": False,
        "harness_source": command("git", "rev-parse", "HEAD"), "source": SOURCE, "tag_object": TAG_OBJECT,
        "original_verification_run": 38185464698, "original_verified_at": report["verified_at"],
        "original_report_sha256": REPORT_SHA256, "profiles": profiles, "limits": report["limitations"]})
    print(f"PASS: reconciled {len(profiles)} existing native {args.scope} profiles; no new native run claimed")


if __name__ == "__main__":
    main()
