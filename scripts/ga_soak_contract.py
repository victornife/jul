"""Fixed #590 acceptance contract, shared by the workload and evidence readers."""

import math
from pathlib import Path

SOURCE = "b12e051139f652b400cff37d9f063f5637a7d550"
FEATURES = ["CGC-ROUTE", "CGC-RES", "SEC-EGRESS"]
PAYLOAD = b"soak590\n"
API_CONFIG = "/api/config"
CLEAN_ROUTE = "/clean/"
CORS_ROUTE = "/cors/"
DISCOVERY_ROUTE = "/discovery/"
HEALTH_ROUTE = "/healthprobe/"
AUTH_ROUTE = "/auth/"
AUTH_DNS_ROUTE = "/auth-dns/"
AUTH_TIGHT_ROUTE = "/auth-tight/"
AUTH_DENIED_ROUTE = "/auth-denied/"
PREDICATE_ROUTE = "/route/item?version=v2"
DISCOVERY_COUNTER = "control:/v1/health/service/soak"
BEARER_PREFIX = "Bearer "

REQUIRED = {
    "routing", "cors", "http", "grpc", "l4", "auth_allow", "auth_deny", "auth_dns",
    "discovery", "pressure", "retry_budget", "retry_deadline", "circuit_recovery", "health_recovery", "unsafe_post",
    "committed_response", "client_cancel", "grpc_pressure_cancel", "l4_pressure",
    "l4_fault_recovery", "tighten", "dns_deny", "invalid_candidate", "rollback", "redirect",
}
LIMITS = {
    "sample_seconds": 10, "max_gap_seconds": 25, "cycle_seconds": 60,
    "max_cycle_gap_seconds": 90, "warmup_seconds": 600,
    "rss_ceiling_bytes": 1024 << 20, "rss_growth_bytes": 128 << 20,
    "rss_slope_bytes_per_hour": 16 << 20, "heap_growth_bytes": 128 << 20,
    "goroutine_growth": 96, "fd_growth": 8, "series_growth": 128,
    "series_ceiling": 3000, "artifact_ceiling_bytes": 2 << 30,
    "quiet_seconds": 120,
}
RESOURCE_KEYS = ("process_resident_memory_bytes", "go_memstats_heap_inuse_bytes", "go_goroutines", "process_open_fds")


def require(condition, message):
    if not condition:
        raise RuntimeError(message)


def metrics(raw):
    result = {}
    for line in raw.decode().splitlines():
        if not line or line.startswith("#"):
            continue
        key, value = line.rsplit(" ", 1)
        if "_bucket{" not in key:
            result[key] = float(value)
    for key in RESOURCE_KEYS:
        require(key in result and math.isfinite(result[key]), f"missing/invalid metric {key}")
    return result


def slope(samples, key):
    origin = samples[0]["elapsed"]
    xs = [(sample["elapsed"] - origin) / 3600 for sample in samples]
    ys = [sample["metrics"][key] for sample in samples]
    mean_x, mean_y = sum(xs) / len(xs), sum(ys) / len(ys)
    denominator = sum((x - mean_x) ** 2 for x in xs)
    require(denominator > 0, "insufficient time span for resource trend")
    return sum((x - mean_x) * (y - mean_y) for x, y in zip(xs, ys)) / denominator


def timing_errors(manifest, samples, elapsed):
    errors = []
    if elapsed < manifest["seconds"]:
        errors.append("measured duration shorter than declared duration")
    if len(samples) < math.floor(manifest["seconds"] / LIMITS["sample_seconds"]) + 1:
        errors.append("required sample count missing")
    if not samples or samples[0]["elapsed"] > LIMITS["max_gap_seconds"]:
        errors.append("initial measured sample missing")
    if any(b["elapsed"] - a["elapsed"] > LIMITS["max_gap_seconds"]
           or b["elapsed"] <= a["elapsed"] for a, b in zip(samples, samples[1:])):
        errors.append("recording gap/non-monotonic time")
    return errors


def phase_errors(events, elapsed):
    errors = []
    for name in REQUIRED:
        times = [event["elapsed"] for event in events if event["name"] == name and event["ok"]]
        if not times:
            errors.append(f"missing phase: {name}")
            continue
        gaps = [times[0], elapsed - times[-1], *(b - a for a, b in zip(times, times[1:]))]
        if max(gaps) > LIMITS["max_cycle_gap_seconds"]:
            errors.append(f"phase not continuously repeated: {name}")
    if any(not event["ok"] for event in events):
        errors.append("unexpected workload failure")
    return errors


def quiet_errors(quiet, baseline):
    if len(quiet) < 3 or quiet[-1]["elapsed"] - quiet[0]["elapsed"] < 20:
        return ["final quiescent evidence missing"]
    errors = []
    for sample in quiet:
        values = sample["metrics"]
        if values["process_open_fds"] > baseline["process_open_fds"] + LIMITS["fd_growth"]:
            errors.append("quiescent FD growth")
        if values["go_goroutines"] > baseline["go_goroutines"] + LIMITS["goroutine_growth"]:
            errors.append("quiescent goroutine growth")
        if any(value != 0 for key, value in values.items()
               if key.startswith(("jul_upstream_active_requests{", "jul_upstream_pending_requests{"))):
            errors.append("quiescent admission leak")
    return errors


def growth_errors(samples, active, baseline):
    errors = []
    for key, limit in (("process_resident_memory_bytes", LIMITS["rss_growth_bytes"]),
                       ("go_memstats_heap_inuse_bytes", LIMITS["heap_growth_bytes"]),
                       ("go_goroutines", LIMITS["goroutine_growth"])):
        if max(sample["metrics"][key] for sample in active) > baseline[key] + limit:
            errors.append(f"post-warmup growth: {key}")
    if max(sample["metrics"]["process_resident_memory_bytes"] for sample in samples) > LIMITS["rss_ceiling_bytes"]:
        errors.append("RSS ceiling exceeded")
    return errors


def resource_errors(manifest, samples, quiet):
    warmup = min(LIMITS["warmup_seconds"], manifest["seconds"] / 3)
    warm = [sample for sample in samples if sample["elapsed"] <= warmup]
    active = [sample for sample in samples if sample["elapsed"] >= warmup]
    if not warm or len(active) < 2:
        return ["insufficient resource samples"]
    baseline = {key: max(sample["metrics"][key] for sample in warm) for key in RESOURCE_KEYS}
    errors = growth_errors(samples, active, baseline)
    tail = [sample for sample in active if sample["elapsed"] >= manifest["seconds"] / 2]
    if manifest["seconds"] >= 3600 and (
            len(tail) < 2 or slope(tail, "process_resident_memory_bytes") > LIMITS["rss_slope_bytes_per_hour"]):
        errors.append("RSS trend exceeded/unavailable")
    initial_series = set().union(*(sample["metrics"] for sample in warm))
    all_series = set().union(*(sample["metrics"] for sample in samples))
    if len(all_series) > min(LIMITS["series_ceiling"], len(initial_series) + LIMITS["series_growth"]):
        errors.append("metric cardinality exceeded")
    return errors + quiet_errors(quiet, baseline)


def evaluate(manifest, samples, events, quiet):
    elapsed = samples[-1]["elapsed"] if samples else 0
    errors = timing_errors(manifest, samples, elapsed) + phase_errors(events, elapsed)
    errors += resource_errors(manifest, samples, quiet)
    return {"passed": not errors, "criterion_5_candidate": not errors and manifest["seconds"] >= 28800,
            "measured_seconds": elapsed, "samples": len(samples), "errors": sorted(set(errors)),
            "resource_trend_assessed": manifest["seconds"] >= 3600,
            "features": FEATURES, "automatic_promotion": False}


def resource_budget():
    group = next(line.split("::", 1)[1] for line in Path("/proc/self/cgroup").read_text().splitlines()
                 if line.startswith("0::"))
    directory = Path("/sys/fs/cgroup") / group.lstrip("/")
    cpu = (directory / "cpu.max").read_text().split()
    memory = (directory / "memory.max").read_text().strip()
    return {"cpu_max": " ".join(cpu), "memory_max": memory,
            "enforced": cpu[0] != "max" and int(cpu[0]) / int(cpu[1]) <= 2
            and memory != "max" and int(memory) <= 2 << 30}
