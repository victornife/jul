#!/usr/bin/env python3
"""Exercise an extracted Darwin archive as a first-time operator would."""

from __future__ import annotations

import hashlib
import signal
import socket
import subprocess
import sys
import tempfile
import time
import urllib.error
import urllib.request
from pathlib import Path


def main() -> None:
    package, archive, checksum = (Path(arg).resolve() for arg in sys.argv[1:4])
    actual = hashlib.sha256(archive.read_bytes()).hexdigest()
    expected = checksum.read_text().split()[0]
    if actual != expected:
        raise RuntimeError(f"archive checksum mismatch: {actual} != {expected}")

    binary = package / "jul"
    for name in ("jul", "server.toml", "README.md", "SECURITY.md"):
        if not (package / name).is_file():
            raise RuntimeError(f"missing packaged file: {name}")
    subprocess.run([binary, "--version"], cwd=package, check=True)
    # The shipped config is a template with an operator-owned static root.
    subprocess.run(
        [binary, "check", "-config", "server.toml", "-skip-static-roots"],
        cwd=package,
        check=True,
    )

    site = package / "public"
    site.mkdir()
    (site / "index.html").write_text("<h1>Hello from Jul.IA</h1>\n")
    with socket.socket() as reserved:
        reserved.bind(("127.0.0.1", 0))
        port = reserved.getsockname()[1]

    with tempfile.TemporaryFile(mode="w+t") as output:
        process = subprocess.Popen(
            [binary, "run", "--serve", "./public", "--listen", f"127.0.0.1:{port}"],
            cwd=package,
            stdout=output,
            stderr=subprocess.STDOUT,
        )
        try:
            url = f"http://127.0.0.1:{port}/"
            deadline = time.monotonic() + 20
            while time.monotonic() < deadline:
                if process.poll() is not None:
                    raise RuntimeError(f"server exited early: {process.returncode}")
                try:
                    with urllib.request.urlopen(url, timeout=1) as response:
                        body = response.read()
                        if response.status != 200 or body != b"<h1>Hello from Jul.IA</h1>\n":
                            raise RuntimeError(f"unexpected HTTP response: {response.status}, {body!r}")
                        print(f"packaged first run passed: {package.name}, HTTP 200")
                        return
                except (urllib.error.URLError, TimeoutError):
                    time.sleep(0.2)
            raise RuntimeError("server did not answer within 20 seconds")
        except Exception:
            output.seek(0)
            print(output.read(), file=sys.stderr)
            raise
        finally:
            if process.poll() is None:
                process.send_signal(signal.SIGINT)
                try:
                    process.wait(timeout=10)
                except subprocess.TimeoutExpired:
                    process.kill()
                    process.wait()


if __name__ == "__main__":
    if len(sys.argv) != 4:
        raise SystemExit("usage: macos-package-first-run.py PACKAGE ARCHIVE SHA256_FILE")
    main()
