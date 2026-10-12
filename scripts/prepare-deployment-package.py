#!/usr/bin/env python3
"""Download and verify an unchanged native full release package for CI journeys."""

import argparse
import importlib.util
import platform
from pathlib import Path

from deployment_lab import command, write_report


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--os", choices=("linux", "windows", "darwin"), required=True)
    parser.add_argument("--arch", choices=("amd64", "arm64"), required=True)
    parser.add_argument("--directory", type=Path, required=True)
    parser.add_argument("--report", type=Path, required=True)
    parser.add_argument("--path-output", type=Path, required=True)
    args = parser.parse_args()
    spec = importlib.util.spec_from_file_location("release_verifier", Path(__file__).with_name("verify-release-packages.py"))
    verifier = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(verifier)
    import os
    native_os = {"Linux": "linux", "Windows": "windows", "Darwin": "darwin"}[platform.system()]
    native_arch = verifier.native_architecture(platform.machine(), os.environ, os.name == "nt")
    assert (args.os, args.arch) == (native_os, native_arch), "deployment package must match the native runner"
    tag, source = "v2.2.0", "b12e051139f652b400cff37d9f063f5637a7d550"
    args.directory.mkdir(parents=True, exist_ok=True)
    command("gh", "release", "download", tag, "--repo", "victornife/jul", "--pattern",
            f"jul_{tag}_{args.os}_{args.arch}_full.*", "--pattern", "SHA256SUMS", "--dir", args.directory)
    binary, evidence = verifier.verify_archive(args.directory.resolve(), tag, source, args.os, args.arch, "full")
    evidence["harness_source"] = command("git", "rev-parse", "HEAD")
    write_report(args.report, evidence)
    args.path_output.write_text(str(binary.parent) + "\n")
    print(f"Verified native published full package: {args.os}/{args.arch}, source {source}")


if __name__ == "__main__":
    main()
