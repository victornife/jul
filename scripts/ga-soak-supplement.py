#!/usr/bin/env python3
"""Retain authorised additional scrapes of an existing #590 process, without editing its evidence."""

import argparse
import hashlib
import http.client
import importlib.util
import json
from pathlib import Path
import shutil
import sys
import time


def require(condition, message):
    if not condition:
        raise RuntimeError(message)


def sha256(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


def write_json(path, value):
    path.write_text(json.dumps(value, indent=2, sort_keys=True) + "\n")


def process_identity(pid):
    stat = Path(f"/proc/{pid}/stat").read_text().rsplit(") ", 1)[1].split()
    return {"pid": pid, "start_ticks": stat[19],
            "boot_id": Path("/proc/sys/kernel/random/boot_id").read_text().strip()}


def collect(primary, out):
    require(not out.exists(), "refusing to overwrite supplemental evidence")
    out.mkdir(mode=0o700)
    manifest_path = primary / "manifest.json"
    manifest = json.loads(manifest_path.read_text())
    pid = manifest["jul_pid"]
    identity = process_identity(pid)
    binary = Path(f"/proc/{pid}/exe")
    require(sha256(binary) == manifest["binary_sha256"], "running binary differs from primary manifest")
    token_entries = [entry.split(b"=", 1)[1] for entry in Path(f"/proc/{pid}/environ").read_bytes().split(b"\0")
                     if entry.startswith(b"JUL_GA_SOAK_TOKEN=")]
    require(len(token_entries) == 1, "test authentication reference unavailable")
    token = token_entries[0].decode()
    with (primary / "samples.jsonl").open() as stream:
        first = json.loads(next(stream))
    origin = first["boottime"] - first["elapsed"]
    finish = origin + manifest["seconds"] - 5
    require(time.clock_gettime(time.CLOCK_BOOTTIME) < finish, "active workload window already ended")
    harness = Path(__file__).with_name("ga-soak.py")
    require(sha256(harness) == manifest["harness_sha256"], "primary parser source changed")
    shutil.copyfile(harness, out / "primary-harness.py")
    for name in ("ga_soak_contract.py", "ga_soak_fixtures.py"):
        if name in manifest["harness_files"]:
            source = harness.with_name(name)
            require(sha256(source) == manifest["harness_files"][name], f"primary helper source changed: {name}")
            shutil.copyfile(source, out / name)
    shutil.copyfile(__file__, out / "collector.py")
    spec = importlib.util.spec_from_file_location("primary_harness", out / "primary-harness.py")
    module = importlib.util.module_from_spec(spec)
    sys.path.insert(0, str(out))
    try:
        spec.loader.exec_module(module)
    finally:
        sys.path.pop(0)
    metadata = {
        "purpose": "additional real scrapes from the same continuously exercised process",
        "authorisation": "maintainer approved supplemental collection before the active window ended",
        "primary_manifest_sha256": sha256(manifest_path),
        "primary_harness_sha256": manifest["harness_sha256"],
        "collector_sha256": sha256(Path(__file__)),
        "source_sha": manifest["package"]["metadata"]["commit"],
        "binary_sha256": manifest["binary_sha256"], "process": identity,
        "started_utc": module.utc(), "interval_seconds": 10,
        "original_evidence_modified": False, "automatic_promotion": False,
        "origin_boottime": origin, "active_window_end_boottime": finish + 5,
    }
    write_json(out / "manifest.json", metadata)
    samples = 0
    last = None
    next_sample = time.monotonic()
    try:
        while time.clock_gettime(time.CLOCK_BOOTTIME) < finish:
            require(process_identity(pid) == identity, "original process identity changed")
            connection = http.client.HTTPConnection("127.0.0.1", manifest["ports"]["admin"], timeout=3)
            try:
                connection.request("GET", "/metrics", headers={"Authorization": "Bearer " + token})
                response = connection.getresponse()
                require(response.status == 200, f"metrics HTTP {response.status}")
                values = module.metrics(response.read())
            finally:
                connection.close()
            now = time.clock_gettime(time.CLOCK_BOOTTIME)
            require(now < origin + manifest["seconds"], "scrape completed outside active workload window")
            sample = {"utc": module.utc(), "elapsed": now - origin, "boottime": now,
                      "walltime": time.time(), "metrics": values, "quiet": False,
                      "collector": "supplemental", "jul_pid": pid}
            if last:
                require(0 < now - last["boottime"] <= 25, "supplemental recording gap")
                require(abs((sample["walltime"] - last["walltime"]) - (now - last["boottime"])) <= 5,
                        "supplemental clock discontinuity")
            with (out / "samples.jsonl").open("a") as output:
                output.write(json.dumps(sample, sort_keys=True) + "\n")
            samples += 1
            write_json(out / "live.json", {"state": "collecting", "samples": samples, "elapsed": sample["elapsed"],
                                         "utc": sample["utc"], "jul_pid": pid})
            last = sample
            next_sample += 10
            remaining = finish - time.clock_gettime(time.CLOCK_BOOTTIME)
            time.sleep(max(0, min(next_sample - time.monotonic(), remaining)))
        write_json(out / "result.json", {"collection_completed": True, "samples": samples,
                                         "finished_utc": module.utc(), "automatic_promotion": False})
    except Exception as error:
        write_json(out / "failure.json", {"error": str(error), "samples": samples, "utc": module.utc()})
        raise
    finally:
        write_json(out / "digests.json", {path.name: sha256(path) for path in out.iterdir()
                                        if path.is_file() and path.name != "digests.json"})


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--primary", type=Path, required=True)
    parser.add_argument("--out", type=Path, required=True)
    args = parser.parse_args()
    collect(args.primary.resolve(), args.out.resolve())
