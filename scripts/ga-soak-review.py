#!/usr/bin/env python3
"""Verify and export bounded evidence for #590's authorised collection deviation."""

import argparse
import gzip
import hashlib
import importlib.util
import json
from pathlib import Path

FROZEN_HARNESS = "2d251f12e67065a4b74d72f293f361001456af81f73aeb7cab82bbcc5f4cdd40"
PUBLIC_METRICS = (
    "process_resident_memory_bytes", "process_open_fds", "go_goroutines", "go_memstats_heap_inuse_bytes",
    "jul_upstream_", "jul_egress_", "jul_auth_decisions_total", "jul_grpc_proxy_streams_total",
    "jul_http_requests_total", "jul_stream_active_conns", "jul_stream_bytes_total",
)


def require(condition, message):
    if not condition:
        raise RuntimeError(message)


def digest(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


def records(path):
    with path.open() as stream:
        return [json.loads(line) for line in stream]


def verify_files(directory):
    listed = json.loads((directory / "digests.json").read_text())
    for name, expected in listed.items():
        path = (directory / name).resolve()
        require(path.is_relative_to(directory.resolve()), "artifact path escapes evidence directory")
        require(digest(path) == expected, f"artifact digest mismatch: {name}")
    return listed


def validate_supplement(primary_manifest, primary_path, first, metadata, samples):
    require(metadata["primary_manifest_sha256"] == digest(primary_path), "supplement primary manifest mismatch")
    require(metadata["primary_harness_sha256"] == primary_manifest["harness_sha256"], "supplement parser mismatch")
    require(metadata["binary_sha256"] == primary_manifest["binary_sha256"], "supplement binary mismatch")
    require(metadata["source_sha"] == primary_manifest["package"]["metadata"]["commit"], "supplement source mismatch")
    require(metadata["process"]["pid"] == primary_manifest["jul_pid"], "supplement process mismatch")
    require(metadata["process"]["boot_id"] == primary_manifest["host"]["boot_id"], "supplement host boot mismatch")
    require(metadata["original_evidence_modified"] is False, "supplement modified primary evidence")
    origin = first["boottime"] - first["elapsed"]
    require(abs(origin - metadata["origin_boottime"]) < 0.005, "supplement clock origin mismatch")
    require(samples, "supplement samples missing")
    for sample in samples:
        require(sample["collector"] == "supplemental" and sample["jul_pid"] == primary_manifest["jul_pid"],
                "supplement record identity mismatch")
        require(not sample["quiet"] and 0 < sample["elapsed"] < primary_manifest["seconds"],
                "supplement record outside active workload")
        require(abs(sample["boottime"] - origin - sample["elapsed"]) < 0.005,
                "supplement sample clock mismatch")
    for before, after in zip(samples, samples[1:]):
        elapsed = after["elapsed"] - before["elapsed"]
        require(0 < elapsed <= 25, "supplement sample gap/duplicate timestamp")
        require(abs(after["walltime"] - before["walltime"] - elapsed) <= 5, "supplement clock discontinuity")


def resources(module, manifest, samples, quiet):
    warm = [sample for sample in samples if sample["elapsed"] <= 600]
    active = [sample for sample in samples if sample["elapsed"] >= 600]
    tail = [sample for sample in samples if sample["elapsed"] >= manifest["seconds"] / 2]
    keys = ("process_resident_memory_bytes", "go_memstats_heap_inuse_bytes", "go_goroutines", "process_open_fds")
    return {
        "warmup_max": {key: max(sample["metrics"][key] for sample in warm) for key in keys},
        "active_max": {key: max(sample["metrics"][key] for sample in active) for key in keys},
        "rss_final_half_slope_bytes_per_hour": module.slope(tail, keys[0]),
        "max_sample_gap_seconds": max(after["elapsed"] - before["elapsed"]
                                      for before, after in zip(samples, samples[1:])),
        "warmup_series": len(set().union(*(sample["metrics"] for sample in warm))),
        "all_series": len(set().union(*(sample["metrics"] for sample in samples))),
        "quiet": [{"elapsed": sample["elapsed"], "metrics": {
            key: value for key, value in sample["metrics"].items()
            if key in keys or key.startswith(("jul_upstream_active_requests{", "jul_upstream_pending_requests{"))}}
                  for sample in quiet],
    }


def review(primary, supplemental, out):
    require(not out.exists(), "refusing to overwrite review evidence")
    original_digests = verify_files(primary)
    supplemental_digests = verify_files(supplemental)
    manifest_path = primary / "manifest.json"
    manifest = json.loads(manifest_path.read_text())
    harness = primary / "harness" / "ga-soak.py"
    require(digest(harness) == FROZEN_HARNESS == manifest["harness_sha256"], "frozen harness identity mismatch")
    spec = importlib.util.spec_from_file_location("frozen_ga_soak", harness)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    rows = records(primary / "samples.jsonl")
    samples = [row for row in rows if not row["quiet"]]
    quiet = [row for row in rows if row["quiet"]][-3:]
    extra = records(supplemental / "samples.jsonl")
    supplement_manifest = json.loads((supplemental / "manifest.json").read_text())
    validate_supplement(manifest, manifest_path, samples[0], supplement_manifest, extra)
    require(digest(supplemental / "collector.py") == supplement_manifest["collector_sha256"],
            "supplement collector source mismatch")
    require(digest(supplemental / "primary-harness.py") == FROZEN_HARNESS, "supplement frozen parser mismatch")
    require(json.loads((supplemental / "result.json").read_text())["collection_completed"],
            "supplement collection did not complete")
    events = records(primary / "events.jsonl")
    original = module.evaluate(manifest, samples, events, quiet)
    retained = json.loads((primary / "result.json").read_text())
    require(all(retained[key] == original[key] for key in ("passed", "criterion_5_candidate", "errors", "samples")),
            "original verdict does not reproduce")
    require(original["errors"] == ["required sample count missing"], "original failure is not solely collection count")
    combined = sorted(samples + extra, key=lambda sample: sample["elapsed"])
    require(all(after["elapsed"] > before["elapsed"] for before, after in zip(combined, combined[1:])),
            "combined evidence contains duplicate/non-increasing timestamps")
    augmented = module.evaluate(manifest, combined, events, quiet)
    require(augmented["criterion_5_candidate"], "augmented gate failed: " + "; ".join(augmented["errors"]))
    require(not any(key.startswith("forbidden:") for row in rows for key in row["fixture_counts"]),
            "forbidden auxiliary destination was contacted")
    coverage = {}
    for phase in sorted(module.REQUIRED):
        matched = [event for event in events if event["name"] == phase and event["ok"]]
        coverage[phase] = {"count": len(matched), "first_elapsed": matched[0]["elapsed"],
                           "last_elapsed": matched[-1]["elapsed"],
                           "max_gap_seconds": max((b["elapsed"] - a["elapsed"] for a, b in zip(matched, matched[1:])),
                                                  default=0)}
    log_records = 0
    reasons = {}
    for path in (primary / "private-runtime").glob("access*.log*"):
        with path.open() as stream:
            for line in stream:
                record = json.loads(line)
                log_records += 1
                reason = record.get("upstream_reason")
                if reason:
                    reasons[reason] = reasons.get(reason, 0) + 1
    out.mkdir(parents=True)
    report = {
        "runtime_source": manifest["package"]["metadata"]["commit"],
        "package": manifest["package"], "binary_sha256": manifest["binary_sha256"],
        "started_utc": manifest["started_utc"], "finished_utc": retained["finished_utc"],
        "features": manifest["features"], "declared_seconds": manifest["seconds"],
        "limits": manifest["limits"], "host_class": "shared ARM64 WSL2 Linux; loopback clients",
        "host_boot_sha256": hashlib.sha256(manifest["host"]["boot_id"].encode()).hexdigest(),
        "budget": manifest["budget"], "jul_pid": manifest["jul_pid"],
        "primary_harness_sha256": FROZEN_HARNESS, "harness_files": manifest["harness_files"],
        "supplemental_collector_sha256": supplement_manifest["collector_sha256"],
        "supplemental_samples": len(extra), "primary_manifest_sha256": digest(manifest_path),
        "original_evaluation": original, "augmented_evaluation": augmented,
        "original_verdict_preserved": True, "automatic_promotion": False,
        "primary_resources": resources(module, manifest, samples, quiet),
        "augmented_resources": resources(module, manifest, combined, quiet),
        "phase_coverage": coverage, "unexpected_events": sum(not event["ok"] for event in events),
        "final_fixture_counts": rows[-1]["fixture_counts"],
        "retained_access_window": {"valid_json_records": log_records, "reason_counts": reasons,
                                   "complete_request_archive": False},
        "profile_metadata": {stage: json.loads((primary / (stage + "-profiles.json")).read_text())
                             for stage in ("T0", "Tend")},
        "limitations": manifest["limitations"],
        "review_sha256": digest(Path(__file__)),
        "collection_deviation": "Authorised read-only supplemental scrapes of the same continuous process; "
                                "primary count failure remains unchanged. No threshold or product workload changed.",
    }
    (out / "review.json").write_text(json.dumps(report, indent=2, sort_keys=True) + "\n")
    (out / "raw-artifact-digests.json").write_text(json.dumps({
        "primary": original_digests, "supplemental": supplemental_digests}, indent=2, sort_keys=True) + "\n")
    with (out / "metrics.jsonl.gz").open("wb") as destination:
        with gzip.GzipFile(fileobj=destination, mode="wb", mtime=0) as compressed:
            for sample in sorted(rows + extra, key=lambda row: row["elapsed"]):
                value = {"utc": sample["utc"], "elapsed": sample["elapsed"], "quiet": sample["quiet"],
                         "collector": sample.get("collector", "primary"),
                         "metrics": {key: value for key, value in sample["metrics"].items()
                                     if key.startswith(PUBLIC_METRICS)}}
                compressed.write((json.dumps(value, sort_keys=True) + "\n").encode())
    (out / "digests.json").write_text(json.dumps({
        path.name: digest(path) for path in out.iterdir() if path.name != "digests.json"
    }, indent=2, sort_keys=True) + "\n")
    print(json.dumps({"original": original, "augmented": augmented, "supplemental_samples": len(extra),
                      "retained_access_records": log_records, "export": str(out)}, sort_keys=True))


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--primary", type=Path, required=True)
    parser.add_argument("--supplemental", type=Path, required=True)
    parser.add_argument("--out", type=Path, required=True)
    args = parser.parse_args()
    review(args.primary.resolve(), args.supplemental.resolve(), args.out.resolve())
