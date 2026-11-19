#!/usr/bin/env python3
"""Run the synthetic guest diagnostic against an existing Jul checkout.

Usage: python3 reproduce.py REPOSITORY NEW_OUTPUT_DIRECTORY
Requires the pinned Go toolchain on PATH. Never use this instrumentation with
real request data: its temporary first-party overlay captures guest stderr.
"""

import hashlib
import json
import os
from pathlib import Path
import platform
import subprocess
import sys


def main():
    repo = Path(sys.argv[1]).resolve()
    out = Path(sys.argv[2]).resolve()
    source = Path(__file__).with_name("diagnostic_test.go.txt")
    target = repo / "internal/plugins/closeout_diagnostic_test.go"
    runtime = repo / "internal/plugins/runtime.go"
    text = runtime.read_text()
    marker = "cfg := wazero.NewModuleConfig().\n"
    if text.count(marker) != 1:
        raise RuntimeError("runtime source changed; inspect before adapting overlay")
    out.mkdir(parents=True, exist_ok=False)
    guest = out / "inspect-load.wasm"
    env = dict(os.environ, GOOS="wasip1", GOARCH="wasm", CGO_ENABLED="0")
    subprocess.run(
        ["go", "build", "-trimpath", "-buildvcs=false", "-buildmode=c-shared",
         "-o", str(guest), "./inspect-load"],
        cwd=repo / "examples/plugins", env=env, check=True,
    )
    instrumented = out / "runtime.go"
    instrumented.write_text(
        text.replace("import (\n", 'import (\n\t"os"\n', 1).replace(
            marker, marker + "\t\tWithStderr(os.Stderr).\n", 1
        )
    )
    overlay = out / "overlay.json"
    overlay.write_text(json.dumps({"Replace": {str(runtime): str(instrumented)}}))
    metadata = {
        "revision": subprocess.check_output(["git", "rev-parse", "HEAD"], cwd=repo, text=True).strip(),
        "go": subprocess.check_output(["go", "version"], text=True).strip(),
        "platform": platform.platform(),
        "guest_sha256": hashlib.sha256(guest.read_bytes()).hexdigest(),
        "instrument_sha256": hashlib.sha256(source.read_bytes()).hexdigest(),
        "instrumentation": "first-party module-config stderr only; upstream dependencies unchanged",
    }
    (out / "metadata.json").write_text(json.dumps(metadata, indent=2) + "\n")
    with target.open("x") as stream:
        stream.write(source.read_text())
    try:
        env = dict(os.environ, GOMAXPROCS="2", JUL_CLOSEOUT_GUEST=str(guest))
        with (out / "result.txt").open("w") as stream:
            subprocess.run(
                ["go", "test", "-overlay", str(overlay), "-tags", "wasmplugins",
                 "./internal/plugins", "-run", "^TestCloseoutGuest(HTTP)?Diagnostic$",
                 "-count=1", "-v", "-timeout=2m"],
                cwd=repo, env=env, stdout=stream, stderr=subprocess.STDOUT,
                check=True, timeout=180,
            )
    finally:
        target.unlink()


if __name__ == "__main__":
    main()
