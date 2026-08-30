#!/usr/bin/env python3
# Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
# SPDX-License-Identifier: AGPL-3.0-only

from __future__ import annotations

import json
from pathlib import Path


CLASSIFIER_OLD = '''func classifyReturn(params []string, serverLevel bool) capability {
\tif len(params) == 0 {
\t\treturn blocking("NGX_RETURN_MALFORMED", RiskRouting, "return directive has no status or target")
\t}
\tif serverLevel {
\t\treturn capabilityRegistry[capabilityKey{ContextServer, "return"}]
\t}
\tcode, err := strconv.Atoi(params[0])
\tif err == nil && len(params) > 1 && (code < 300 || code >= 400) {
\t\treturn approximated("NGX_LOCATION_RETURN_BODY", RiskRouting, "non-redirect response body is dropped")
\t}
\treturn capabilityRegistry[capabilityKey{ContextLocation, "return"}]
}
'''

CLASSIFIER_NEW = '''func classifyReturn(params []string, serverLevel bool) capability {
\tif len(params) == 0 {
\t\treturn blocking("NGX_RETURN_MALFORMED", RiskRouting, "return directive has no status or target")
\t}
\tif serverLevel {
\t\treturn capabilityRegistry[capabilityKey{ContextServer, "return"}]
\t}
\tcode, err := strconv.Atoi(params[0])
\tif err == nil {
\t\tif len(params) > 1 && (code < 300 || code >= 400) {
\t\t\treturn approximated("NGX_LOCATION_RETURN_BODY", RiskRouting, "non-redirect response body is dropped")
\t\t}
\t\tif code >= 300 && code < 400 && len(params) > 1 {
\t\t\treturn classifyRedirectTarget(params[1])
\t\t}
\t\treturn capabilityRegistry[capabilityKey{ContextLocation, "return"}]
\t}
\treturn classifyRedirectTarget(params[0])
}

func classifyRedirectTarget(target string) capability {
\ttarget = strings.TrimSpace(target)
\tlower := strings.ToLower(target)
\tif strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://") {
\t\treturn capabilityRegistry[capabilityKey{ContextLocation, "return"}]
\t}
\treturn approximated(
\t\t"NGX_LOCATION_RETURN_RELATIVE",
\t\tRiskRouting,
\t\t"NGINX expands a local redirect to an absolute URL by default while Jul preserves the relative target",
\t)
}
'''

CHANGELOG_ENTRY = (
    "- **NGINX migration corpus and selected-dimension real E2E (#154):** "
    "adds a licensed, sanitized fixture contract; exact assessment/candidate goldens; "
    "real Jul replay; and a digest-pinned, network-isolated NGINX reference lane. The "
    "first differential finding now classifies local `return` redirects as approximated "
    "because NGINX expands them to absolute URLs by default while Jul preserves the "
    "relative target.\n"
)

DOC_NOTE = '''

### Corpus-discovered relative redirect boundary

NGINX expands a local `return 30x /path` target to an absolute `Location`
by default, using the request/server authority. Jul preserves `/path`.
The importer therefore reports `NGX_LOCATION_RETURN_RELATIVE` as
`approximated`, and the corpus records the selected-dimension runtime
relationship as `expected_difference` rather than claiming equivalence.
'''


def patch_classifier() -> None:
    path = Path("internal/migrate/nginx/assessment_classify.go")
    text = path.read_text(encoding="utf-8")
    if "func classifyRedirectTarget(" in text:
        return
    if CLASSIFIER_OLD not in text:
        raise SystemExit("classifyReturn implementation did not match the expected baseline")
    path.write_text(text.replace(CLASSIFIER_OLD, CLASSIFIER_NEW, 1), encoding="utf-8")


def patch_manifest() -> None:
    path = Path("testdata/nginx-corpus/core-multifile-return/manifest.json")
    manifest = json.loads(path.read_text(encoding="utf-8"))
    results = manifest["expected_assessment"]["results"]
    if not any(result.get("code") == "NGX_LOCATION_RETURN_RELATIVE" for result in results):
        replacement: list[dict[str, object]] = []
        replaced = False
        for result in results:
            if result.get("code") == "NGX_LOCATION_RETURN" and result.get("count") == 2:
                supported = dict(result)
                supported["count"] = 1
                replacement.append(supported)
                approximate = dict(result)
                approximate.update(
                    {
                        "code": "NGX_LOCATION_RETURN_RELATIVE",
                        "class": "approximated",
                        "count": 1,
                    }
                )
                replacement.append(approximate)
                replaced = True
            else:
                replacement.append(result)
        if not replaced:
            raise SystemExit("return assessment golden did not match the expected baseline")
        manifest["expected_assessment"]["results"] = replacement

    for scenario in manifest["scenarios"]:
        if scenario["id"] != "relative-redirect":
            continue
        scenario["reference"]["headers"]["location"] = [
            "http://corpus.test:18080/health"
        ]
        scenario["jul"] = {
            "status": 302,
            "headers": {"location": ["/health"]},
        }
        scenario["expected_verdict"] = "expected_difference"
        scenario["expected_difference_code"] = "NGX_LOCATION_RETURN_RELATIVE"
        break
    else:
        raise SystemExit("relative-redirect scenario not found")

    path.write_text(json.dumps(manifest, indent=2) + "\n", encoding="utf-8")


def patch_changelog() -> None:
    path = Path("CHANGELOG.md")
    text = path.read_text(encoding="utf-8")
    if CHANGELOG_ENTRY in text:
        return
    marker = "### Added\n"
    if marker not in text:
        raise SystemExit("CHANGELOG Added section not found")
    path.write_text(text.replace(marker, marker + CHANGELOG_ENTRY, 1), encoding="utf-8")


def patch_docs() -> None:
    for raw_path in ("docs/nginx-migration-corpus.md", "docs/nginx-importer.md"):
        path = Path(raw_path)
        text = path.read_text(encoding="utf-8")
        if "NGX_LOCATION_RETURN_RELATIVE" in text:
            continue
        path.write_text(text.rstrip() + DOC_NOTE + "\n", encoding="utf-8")


def main() -> None:
    patch_classifier()
    patch_manifest()
    patch_changelog()
    patch_docs()


if __name__ == "__main__":
    main()
