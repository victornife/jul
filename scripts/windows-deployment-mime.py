#!/usr/bin/env python3
"""Prepare and verify native registry and explicit MIME behavior over real HTTP."""

import argparse
import gzip
from pathlib import Path
import urllib.request

from deployment_lab import write_report


SAMPLES = {
    "live.m3u8": (b"#EXTM3U\n", "application/vnd.apple.mpegurl"),
    "manifest.mpd": (b"<MPD/>\n", "application/dash+xml"),
    "segment.ts": (b"\x47\x40\x00\x10" + bytes(184), "video/mp2t"),
    "segment.m4s": (b"\x00\x00\x00\x18styp" + bytes(16), "video/iso.segment"),
    "movie.mkv": (b"\x1a\x45\xdf\xa3" + bytes(28), "video/matroska"),
    "audio.aac": (b"\xff\xf1\x50\x80" + bytes(28), "audio/aac"),
}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("mode", choices=("prepare", "verify"))
    parser.add_argument("--extension", required=True)
    parser.add_argument("--directory", type=Path)
    parser.add_argument("--url")
    parser.add_argument("--report", type=Path)
    args = parser.parse_args()
    samples = {**SAMPLES, "ordinary" + args.extension: (b"opaque registry payload\x00\xfa", "application/x-jul516-native")}
    if args.mode == "prepare":
        assert args.directory
        for scope in ("native", "explicit"):
            destination = args.directory / scope
            destination.mkdir(parents=True, exist_ok=True)
            for name, (body, _) in samples.items():
                (destination / name).write_bytes(body)
                (destination / (name + ".gz")).write_bytes(gzip.compress(body, mtime=0))
        return
    assert args.url and args.report
    checks = []
    for scope in ("native", "explicit"):
        for name, (body, native_type) in samples.items():
            expected = native_type if scope == "native" else (
                "application/x-jul516-explicit" if name.endswith(args.extension) else "application/octet-stream")
            for compressed in (False, True):
                request = urllib.request.Request(args.url.rstrip("/") + f"/{scope}/{name}",
                                                 headers={"Accept-Encoding": "gzip" if compressed else "identity"})
                with urllib.request.urlopen(request, timeout=5) as response:
                    assert response.status == 200
                    assert response.headers.get("Content-Type") == expected, f"wrong MIME for {scope}/{name}"
                    received = response.read()
                    if compressed:
                        assert response.headers.get("Content-Encoding") == "gzip"
                        assert "accept-encoding" in response.headers.get("Vary", "").lower()
                        received = gzip.decompress(received)
                    else:
                        assert not response.headers.get("Content-Encoding")
                    assert received == body, f"wrong representation for {scope}/{name}"
                    checks.append({"scope": scope, "file": name, "gzip": compressed, "type": expected, "verdict": "passed"})
    write_report(args.report, {"extension": args.extension, "checks": checks})
    print("PASS: service-native registry MIME, six deterministic types, explicit-table precedence and gzip sidecars")


if __name__ == "__main__":
    main()
