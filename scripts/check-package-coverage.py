#!/usr/bin/env python3
"""Enforce deterministic statement-coverage floors for selected Go packages."""

from __future__ import annotations

import argparse
import json
import re
import sys
from dataclasses import dataclass
from pathlib import Path
from typing import Any

PROFILE_LINE_RE = re.compile(
    r"^(?P<path>.+):(?P<start_line>\d+)\.(?P<start_col>\d+),"
    r"(?P<end_line>\d+)\.(?P<end_col>\d+) "
    r"(?P<statements>\d+) (?P<count>\d+)$"
)


class GateInputError(ValueError):
    """Raised when the manifest or coverage profile is malformed."""


@dataclass(frozen=True)
class PackageRule:
    import_path: str
    label: str
    baseline: float
    floor: float


@dataclass
class PackageCoverage:
    covered_statements: int = 0
    total_statements: int = 0

    @property
    def percent(self) -> float:
        if self.total_statements == 0:
            return 0.0
        return (self.covered_statements / self.total_statements) * 100.0


def _number(value: Any, field: str) -> float:
    if isinstance(value, bool) or not isinstance(value, (int, float)):
        raise GateInputError(f"{field} must be a number")
    result = float(value)
    if result < 0.0 or result > 100.0:
        raise GateInputError(f"{field} must be between 0 and 100")
    return result


def load_rules(path: Path) -> list[PackageRule]:
    try:
        raw = json.loads(path.read_text(encoding="utf-8"))
    except OSError as exc:
        raise GateInputError(f"cannot read profile {path}: {exc}") from exc
    except json.JSONDecodeError as exc:
        raise GateInputError(f"invalid JSON profile {path}: {exc}") from exc

    if not isinstance(raw, dict) or raw.get("version") != 1:
        raise GateInputError("profile version must be 1")
    if raw.get("profile") != "full-tags":
        raise GateInputError("profile must describe the full-tags test profile")

    packages = raw.get("packages")
    if not isinstance(packages, dict) or not packages:
        raise GateInputError("profile packages must be a non-empty object")

    rules: list[PackageRule] = []
    for import_path in sorted(packages):
        data = packages[import_path]
        if not isinstance(import_path, str) or not import_path:
            raise GateInputError("package import paths must be non-empty strings")
        if not isinstance(data, dict):
            raise GateInputError(f"package {import_path} must be an object")

        label = data.get("label")
        if not isinstance(label, str) or not label:
            raise GateInputError(f"package {import_path} label must be a non-empty string")

        baseline = _number(data.get("baseline"), f"{import_path}.baseline")
        floor = _number(data.get("floor"), f"{import_path}.floor")
        if floor > baseline:
            raise GateInputError(
                f"package {import_path} floor {floor:.1f}% exceeds recorded "
                f"baseline {baseline:.1f}%"
            )
        rules.append(PackageRule(import_path, label, baseline, floor))

    return rules


def parse_coverprofile(path: Path, rules: list[PackageRule]) -> dict[str, PackageCoverage]:
    try:
        lines = path.read_text(encoding="utf-8").splitlines()
    except OSError as exc:
        raise GateInputError(f"cannot read cover profile {path}: {exc}") from exc

    if not lines or not lines[0].startswith("mode: "):
        raise GateInputError("cover profile must start with a Go 'mode:' line")

    coverage = {rule.import_path: PackageCoverage() for rule in rules}
    known_paths = set(coverage)

    for line_number, line in enumerate(lines[1:], start=2):
        if not line:
            continue
        match = PROFILE_LINE_RE.match(line)
        if match is None:
            raise GateInputError(f"malformed cover profile line {line_number}: {line!r}")

        source_path = match.group("path")
        package_path = source_path.rsplit("/", 1)[0]
        if package_path not in known_paths:
            continue

        statements = int(match.group("statements"))
        count = int(match.group("count"))
        package = coverage[package_path]
        package.total_statements += statements
        if count > 0:
            package.covered_statements += statements

    return coverage


def enforce(rules: list[PackageRule], coverage: dict[str, PackageCoverage]) -> int:
    failed = False
    missing = False

    for rule in rules:
        result = coverage[rule.import_path]
        if result.total_statements == 0:
            print(
                f"ERROR {rule.label}: package {rule.import_path} is absent from "
                "the coverage profile",
                file=sys.stderr,
            )
            missing = True
            continue

        percent = result.percent
        message = (
            f"{rule.label}: {percent:.1f}% "
            f"({result.covered_statements}/{result.total_statements} statements), "
            f"floor {rule.floor:.1f}%, recorded baseline {rule.baseline:.1f}%"
        )
        if percent + 1e-9 < rule.floor:
            print(f"FAIL {message}", file=sys.stderr)
            failed = True
        else:
            print(f"PASS {message}")

    if missing:
        return 2
    if failed:
        return 1
    return 0


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--profile", required=True, type=Path)
    parser.add_argument("--coverprofile", required=True, type=Path)
    return parser


def main(argv: list[str] | None = None) -> int:
    args = build_parser().parse_args(argv)
    try:
        rules = load_rules(args.profile)
        coverage = parse_coverprofile(args.coverprofile, rules)
    except GateInputError as exc:
        print(f"ERROR: {exc}", file=sys.stderr)
        return 2
    return enforce(rules, coverage)


if __name__ == "__main__":
    raise SystemExit(main())
