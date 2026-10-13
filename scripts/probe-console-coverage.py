#!/usr/bin/env python3
"""Prove each #517 Vitest gate independently without weakening another floor."""
import json
import math
from pathlib import Path
import re
import hashlib
import subprocess

ROOT = Path(__file__).resolve().parents[1]
UI = ROOT / "internal/admin/ui"
POLICY = UI / "coverage-policy.json"
OUT = ROOT / "docs/audit/evidence/2026-10-13-console/probes"


def run(command):
    result = subprocess.run(command, cwd=UI, text=True, stdout=subprocess.PIPE,
                            stderr=subprocess.STDOUT, check=False)
    return result.returncode, result.stdout


def main():
    original = POLICY.read_bytes()
    policy = json.loads(original)
    OUT.mkdir(parents=True, exist_ok=True)
    source = {
        "head": subprocess.check_output(["git", "rev-parse", "HEAD"], cwd=ROOT, text=True).strip(),
        "tree": subprocess.check_output(["git", "rev-parse", "HEAD^{tree}"], cwd=ROOT, text=True).strip(),
        "pnpm": subprocess.check_output(["pnpm", "--version"], cwd=UI, text=True).strip(),
        "node": subprocess.check_output(["node", "--version"], cwd=UI, text=True).strip(),
        "policy_sha256": hashlib.sha256(original).hexdigest(),
    }
    subprocess.run(["git", "diff", "--exit-code", "--", "internal/admin/ui", "Makefile",
                    "scripts/probe-console-coverage.py", ".github/workflows/ci.yml"], cwd=ROOT, check=True)
    (OUT / "source.json").write_text(json.dumps(source, indent=2) + "\n")
    results = []
    try:
        code, log = run(["pnpm", "run", "test:coverage"])
        (OUT / "before.log").write_text(log)
        if code:
            raise RuntimeError("Unmodified coverage gate must pass before probing")
        baseline = json.loads((UI / "coverage/contract-summary.json").read_text())
        for number, (path, floors) in enumerate(policy["paths"].items()):
            for metric in floors:
                modified = json.loads(original)
                measured = baseline["targets"][path]["summary"][metric]["pct"]
                violation = min(101, math.floor(measured + 1))
                modified["paths"][path][metric] = violation
                POLICY.write_text(json.dumps(modified, indent=2) + "\n")
                code, log = run(["pnpm", "exec", "vitest", "run", "--coverage"])
                file = f"{number + 1}-{metric}.log"
                (OUT / file).write_text(log)
                errors = [line for line in log.splitlines() if "ERROR: Coverage" in line]
                tests_pass = bool(re.search(r"Tests\s+\d+ passed \(\d+\)", log)) and not re.search(r"\d+ failed", log)
                intended = len(errors) == 1 and f'Coverage for {metric} ' in errors[0] and f'"{path}" threshold' in errors[0]
                report = json.loads((UI / "coverage/coverage-summary.json").read_text())
                # The coverage companion cannot run after Vitest intentionally
                # fails. Compare all other policy gates against the unchanged
                # baseline and require unchanged full-suite global counts.
                same_global = report["total"] == baseline["global"]
                entry = {"path": path, "metric": metric, "intended_floor": floors[metric],
                         "temporary_floor": violation, "measured": measured, "exit_code": code,
                         "tests_pass": tests_pass, "only_intended_threshold_fails": intended,
                         "unchanged_global_counts": same_global, "diagnostics": errors, "log": file}
                results.append(entry)
                if code == 0 or not tests_pass or not intended or not same_global:
                    raise RuntimeError(f"Probe did not independently prove {path} {metric}: {entry}")
                print(f"PASS probe {path} {metric}: suite passed; named gate failed (exit {code})", flush=True)
                POLICY.write_bytes(original)
    finally:
        POLICY.write_bytes(original)
        (OUT / "results.json").write_text(json.dumps(results, indent=2) + "\n")
    code, log = run(["pnpm", "run", "test:coverage"])
    (OUT / "restored.log").write_text(log)
    if code:
        raise RuntimeError("Restored final coverage gate failed")
    print(f"PASS: {len(results)} independent gates; restored full gate passed")


if __name__ == "__main__":
    main()
