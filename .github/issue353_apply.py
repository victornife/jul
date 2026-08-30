#!/usr/bin/env python3
from __future__ import annotations

import re
from pathlib import Path

import yaml

ROOT = Path(__file__).resolve().parents[1]
TODAY = "2026-08-30"
VERSION = "2.7"


def read(path: str) -> str:
    return (ROOT / path).read_text(encoding="utf-8")


def write(path: str, text: str) -> None:
    if not text.endswith("\n"):
        text += "\n"
    (ROOT / path).write_text(text, encoding="utf-8", newline="\n")


def replace_once(text: str, old: str, new: str, path: str) -> str:
    count = text.count(old)
    if count != 1:
        raise RuntimeError(f"{path}: expected one occurrence, found {count}: {old[:80]!r}")
    return text.replace(old, new, 1)


def replace_regex(text: str, pattern: str, replacement: str, path: str, flags: int = 0) -> str:
    new_text, count = re.subn(pattern, replacement, text, count=1, flags=flags)
    if count != 1:
        raise RuntimeError(f"{path}: expected one regex match, found {count}: {pattern!r}")
    return new_text


def replace_between(text: str, start: str, end: str, replacement: str, path: str) -> str:
    start_at = text.find(start)
    if start_at < 0:
        raise RuntimeError(f"{path}: start marker missing: {start!r}")
    end_at = text.find(end, start_at + len(start))
    if end_at < 0:
        raise RuntimeError(f"{path}: end marker missing: {end!r}")
    return text[:start_at] + replacement + text[end_at:]


def feature_rows() -> list[dict]:
    path = ROOT / "docs" / "feature-status.yaml"
    data = yaml.safe_load(path.read_text(encoding="utf-8"))
    features = data["features"]

    for feature in features:
        feature["delivery"] = "soaked" if feature.get("maturity") == "GA" else "merged"

    existing = {feature["id"] for feature in features}
    additions = [
        {
            "id": "SEC-EGRESS",
            "name": "Auxiliary egress allow-list",
            "tags": ["core"],
            "maturity": "Beta",
            "delivery": "candidate",
            "doc": "egress.md",
            "criteria": {1: True, 2: None, 3: True, 4: False, 5: False, 6: True, 7: True, 8: None, 9: True},
            "notes": "Published in v1.32.1-rc.1 as a prerelease capability; stable release and long-running soak promotion remain explicit decisions.",
        },
        {
            "id": "CGC-ROUTE",
            "name": "Request predicates, response headers, and CORS",
            "tags": ["core"],
            "maturity": "Beta",
            "delivery": "merged",
            "doc": "core-http.md",
            "criteria": {1: True, 2: True, 3: True, 4: False, 5: False, 6: True, 7: True, 8: None, 9: True},
            "notes": "Merged on main through #145-#147. It is not contained in v1.32.1-rc.1 and is not promoted through the older Core HTTP GA row.",
        },
        {
            "id": "CGC-RES",
            "name": "Upstream resilience (admission, retry, circuit)",
            "tags": ["core", "grpc", "stream"],
            "maturity": "Beta",
            "delivery": "merged",
            "doc": "upstreams.md",
            "criteria": {1: True, 2: True, 3: True, 4: False, 5: False, 6: True, 7: True, 8: True, 9: False},
            "notes": "Admission, retry and circuit slices are merged; #287 and #144 retain the integrated race/fuzz/soak and complete external-contract closure at this baseline.",
        },
        {
            "id": "AUTO-AUTH",
            "name": "Configuration authority and managed drift",
            "tags": ["core", "console"],
            "maturity": "Beta",
            "delivery": "merged",
            "doc": "reload-semantics.md",
            "criteria": {1: True, 2: None, 3: True, 4: False, 5: False, 6: True, 7: True, 8: None, 9: True},
            "notes": "Managed/file-owned authority, drift and adoption are merged through #148; the stable external automation API remains #150.",
        },
        {
            "id": "AUTO-CONTRACT",
            "name": "Generated configuration contracts and route identity",
            "tags": ["core"],
            "maturity": "Beta",
            "delivery": "merged",
            "doc": "generated/config-reference.md",
            "criteria": {1: True, 2: None, 3: True, 4: False, 5: False, 6: True, 7: True, 8: None, 9: None},
            "notes": "JSON Schema, machine metadata, generated factual reference and durable route identity are merged through #149; external OpenAPI is separate work under #150.",
        },
        {
            "id": "MIG-ASSESS",
            "name": "NGINX migration assessment, provenance, and includes",
            "tags": ["importer"],
            "maturity": "Beta",
            "delivery": "merged",
            "doc": "nginx-assessment.md",
            "criteria": {1: True, 2: False, 3: True, 4: False, 5: False, 6: True, 7: True, 8: True, 9: True},
            "notes": "Schema-v2 assessment, source provenance and bounded include traversal are merged through #152/#153. The released Y1-09 importer GA record remains separate.",
        },
    ]
    for feature in additions:
        if feature["id"] not in existing:
            features.append(feature)

    return features


def update_manifest(features: list[dict]) -> None:
    header = """# docs/feature-status.yaml
#
# Canonical feature maturity and delivery manifest for Jul.IA.
# Runtime code and generated contracts own behaviour and field-level facts.
# This file owns the product-level maturity and delivery classification rendered
# in docs/status.md.
#
# maturity: GA | GA-soak-pending | Beta | Alpha | Deprecated
# delivery: implemented | merged | candidate | released | soaked
#
# Maturity and delivery are independent axes. Additive work merged after a
# released GA feature does not inherit that feature's maturity implicitly; give
# it its own entry or an explicit relationship. A GA row must be released and
# soaked under ADR 0003/0005. Volatile issue sequencing belongs in #62, not here.
#
# Updated 2026-08-30 for issue #353.

"""
    data = {"version": 2, "updated": TODAY, "features": features}
    body = yaml.safe_dump(data, sort_keys=False, allow_unicode=True, width=120)
    write("docs/feature-status.yaml", header + body)


def criterion_cell(value) -> str:
    if value is True:
        return "✅"
    if value is False:
        return "☐"
    return "n/a"


def tag_cell(tags: list[str]) -> str:
    return " · ".join("core" if tag == "core" else f"`{tag}`" for tag in tags)


def table_for(features: list[dict], maturity: str) -> str:
    selected = [feature for feature in features if feature["maturity"] == maturity]
    lines = [
        "| Feature | ID | Tag | Delivery | 1 | 2 | 3 | 4 | 5 | 6 | 7 | 8 | 9 | Doc |",
        "| --- | --- | --- | --- | :-: | :-: | :-: | :-: | :-: | :-: | :-: | :-: | :-: | --- |",
    ]
    if not selected:
        lines.append("| *(none)* | — | — | — | — | — | — | — | — | — | — | — | — | — |")
    for feature in selected:
        criteria = feature.get("criteria", {})
        cells = [criterion_cell(criteria.get(i)) for i in range(1, 10)]
        lines.append(
            "| "
            + " | ".join(
                [
                    feature["name"],
                    feature["id"],
                    tag_cell(feature.get("tags", [])),
                    f"`{feature['delivery']}`",
                    *cells,
                    f"[{feature['doc']}]({feature['doc']})",
                ]
            )
            + " |"
        )
    return "\n".join(lines)


def update_status(features: list[dict]) -> None:
    path = "docs/status.md"
    text = read(path)
    text = re.sub(r"> Version \d+\.\d+ · Updated \d{4}-\d{2}-\d{2}", f"> Version {VERSION} · Updated {TODAY}", text, count=1)

    delivery = """### Delivery state vs. maturity

Maturity answers *how stable and evidence-complete is this capability?* Delivery
answers *where is this implementation in the publication pipeline?* The axes are
kept separately in [`feature-status.yaml`](feature-status.yaml):

| Delivery | Meaning |
| --- | --- |
| `implemented` | Code exists on a working branch; not part of `main`. |
| `merged` | On `main` under `[Unreleased]`; not in a published tag. |
| `candidate` | Frozen in a published or draft prerelease tag. |
| `released` | Published in a stable immutable tag. |
| `soaked` | Released and through the feature's long-running post-GA soak gate. |

A GA entry must be compatible with `soaked`. A newer additive capability does
not inherit an older GA row merely because it lives in the same package or guide.

### Current product snapshot

- **Published checkpoint:** `v1.32.1-rc.1` is an independently verified
  prerelease candidate at `9a936d0cc1bc3f7086f38ca87741d9d09f950e25`.
  It is not a stable release.
- **Current `main`:** contains substantial later work, including cache
  recertification, closed-world lifecycle authority, structured configuration,
  trusted client identity, backend trust, routing/response policy,
  configuration authority/generated contracts, resilience slices, and NGINX
  assessment/provenance/include traversal. Those additions retain their own
  delivery and maturity rows below.
- **Volatile execution state:** lives in
  [#62](https://github.com/victornife/jul/issues/62). The
  [roadmap](roadmap/README.md) intentionally keeps only durable portfolio state.
- **Dated audit disposition:** lives in the
  [audit register](audit-register.md). Historical audits remain evidence rather
  than a second current-status source.

### Current notices

- **Response cache:** #134 completed integrated recertification; the released
  cache record retains GA.
- **Trusted client address and backend TLS:** merged Beta capabilities; stable
  publication and soak are still explicit promotion gates.
- **Resilience:** admission, retry and circuit implementations are merged; the
  integrated cross-protocol/soak and full external-contract closure remain in
  #287/#144 at this baseline.
- **Routing, configuration authority, generated contracts and NGINX assessment:**
  merged after the current RC and therefore represented separately from older
  GA rows.

"""
    text = replace_between(text, "### Delivery state vs. maturity\n", "## GA criteria legend\n", delivery + "## GA criteria legend\n", path)

    tables = f"""## GA

Released and soaked capabilities that satisfy all applicable GA criteria.

{table_for(features, 'GA')}

## GA — soak pending

Released capabilities that satisfy the other GA criteria but still require the
long-running post-GA soak gate.

{table_for(features, 'GA-soak-pending')}

## Beta

Usable capabilities whose contract, release, soak, or integrated evidence is
not yet at the GA bar. `merged` and `candidate` are not synonyms for released.

{table_for(features, 'Beta')}

## Alpha

{table_for(features, 'Alpha')}

## Deprecated

{table_for(features, 'Deprecated')}

"""
    text = replace_between(text, "## GA\n", "## Soak tracking (post-GA gate)\n", tables + "## Soak tracking (post-GA gate)\n", path)

    changelog_header = "| Date | Ver | What changed | Source |\n| --- | --- | --- | --- |\n"
    new_row = (
        f"| {TODAY} | {VERSION} | Reconciled maturity and delivery as separate axes; added explicit post-RC rows for egress, routing/response policy, resilience, configuration authority/generated contracts, and NGINX assessment/provenance/includes; removed stale programme language. | Issue #353; [feature-status.yaml](feature-status.yaml) |\n"
    )
    if new_row not in text:
        text = replace_once(text, changelog_header, changelog_header + new_row, path)
    text = re.sub(r"\n### Issue #81 delivery note \(2026-08-09\).*\Z", "\n", text, flags=re.S)
    write(path, text)


def update_readme() -> None:
    path = "README.md"
    text = read(path)
    text = replace_once(text, "- **Language:** Go 1.26.5", "- **Language:** Go 1.26", path)
    text = replace_once(text, "[roadmap v2.0](docs/roadmap/README.md)", "[roadmap](docs/roadmap/README.md)", path)

    old_direction = "> Current direction and sequencing are governed by the\n> [combined repository audit](docs/audit/combined-audit-2026-08-03.md) and the\n> [master programme](https://github.com/victornife/jul/issues/62). The durable\n> product direction remains in the [vision](docs/vision/), [roadmap](docs/roadmap/),\n> [engineering specs](docs/specs/), and [ADRs](docs/adr/). The permanent\n> OSS/open-core boundary is defined in\n> [ADR 0012](docs/adr/0012-oss-open-core-boundary.md)."
    new_direction = "> Current product maturity and delivery are governed by\n> [`docs/feature-status.yaml`](docs/feature-status.yaml) and rendered in\n> [`docs/status.md`](docs/status.md). Volatile execution state lives in the\n> [master programme](https://github.com/victornife/jul/issues/62); the\n> [roadmap](docs/roadmap/) keeps the durable portfolio sequence. Dated audit\n> disposition lives in the [audit register](docs/audit-register.md), while the\n> underlying audits remain historical evidence. The permanent OSS/open-core\n> boundary is defined in [ADR 0012](docs/adr/0012-oss-open-core-boundary.md)."
    text = replace_once(text, old_direction, new_direction, path)

    maturity = """## Feature maturity

Jul.IA tracks **maturity** and **delivery** separately. The canonical machine
record is [`docs/feature-status.yaml`](docs/feature-status.yaml); the checked
human view and evidence matrix are in [`docs/status.md`](docs/status.md).

| Classification | Current examples |
| --- | --- |
| **GA · soaked** | Core HTTP, released TLS/ACME and mTLS, authentication, cache, compression, rate limiting, health checks, service discovery, released gRPC/L4/WASM/WAF/observability/importer capabilities, Console, secrets, and reload transaction |
| **Beta · merged/candidate** | Trusted client address, backend TLS trust, auxiliary egress policy, method/header/query routing, response-header policy and CORS, upstream admission/retry/circuit controls, configuration authority/generated contracts, and NGINX assessment/provenance/include traversal |
| **GA — soak pending** | None at this snapshot |

A capability merged on `main` is not automatically released or GA. In
particular, the published `v1.32.1-rc.1` checkpoint is an independently verified
**prerelease**, while current `main` contains substantial later work. See the
[release-candidate evidence](docs/release-candidates/v1.32.1-rc.1.md) and the
[current status matrix](docs/status.md) rather than inferring publication from a
feature guide.

The response-cache correction programme is complete: #134 recertified the
feature and the existing released cache record retains GA. Newer additions keep
their own rows so they do not inherit that maturity implicitly.

Many features require an opt-in **build tag** (for example `grpc`, `acme`,
`wasmplugins`, `stream`, `http3`, `waf`, `consul`, or `kubernetes`). The default
`lean` binary ships the core surface plus gzip. Build with `-tags "..."` or use
a `full` release artifact to enable all optional capabilities.

---

"""
    text = replace_between(text, "## Feature maturity\n", "## Features\n", maturity + "## Features\n", path)

    replacements = {
        "| **Health & failover** | Passive health checking (`max_fails` / `fail_timeout`) plus optional active HTTP/TCP probes (`[upstreams.health_check]`), with automatic retry of idempotent requests against healthy backends |":
        "| **Health & failover** | Released passive/active health checking plus merged Beta resilience controls: bounded admission and pending work, per-backend capacity, retry attempts/deadline/backoff/budget, and an explicit closed/open/half-open circuit model. See [upstreams.md](docs/upstreams.md). |",
        "| **App gateways** | `fastcgi_pass` (e.g. PHP-FPM) and `uwsgi_pass` (Python/WSGI) with full CGI parameter mapping |":
        "| **App gateways** | `fastcgi_pass` and `uwsgi_pass` accept literal targets or named upstream pools and share load balancing, health, failure accounting and admission; TCP and Unix-socket backends are supported. |",
        "| **Response cache** | Two-tier (in-memory + optional disk overflow) cache with TTL, `stale-while-revalidate`, and admin purge; currently under correctness recertification — see [docs/cache.md](docs/cache.md) |":
        "| **Response cache** | Recertified two-tier memory/disk cache with shared-cache validation, conservative authenticated reuse, invalidation, TTL/stale controls, conditional requests, and exact/all purge. The released cache record retains GA — see [docs/cache.md](docs/cache.md). |",
        "| **Routing** | `exact`, `prefix`, and `regex` location matching; regex rewrites with `last`/`break`/`redirect`/`permanent` flags |":
        "| **Routing & response policy** | Deterministic exact/prefix/regex precedence, method/header/query predicates, rewrites, ordered response-header add/set/remove operations, and bounded CORS/preflight handling. The predicate/response-policy additions are merged Beta capabilities. |",
        "| **Hot reload** | Zero-downtime config reload via SIGHUP, file-watch, or the admin API — invalid configs are rejected and the old config keeps serving |":
        "| **Configuration lifecycle** | Transactional zero-downtime reload, strict preflight, planned-restart staging, history/rollback, and explicit `managed` versus `file_owned` authority with drift/adoption semantics. Field-level lifecycle truth is generated from the Go registry. |",
        "| **Migration** | `jul import nginx` translates an existing NGINX config to Jul.IA TOML, reporting every directive it could not map — opt-in `importer` build tag |":
        "| **Migration** | `jul import nginx` can convert supported NGINX constructs and emit deterministic human/JSON assessment with blocking/approximate findings, source provenance, guidance, and opt-in bounded root-confined include traversal — opt-in `importer` build tag. |",
    }
    for old, new in replacements.items():
        text = replace_once(text, old, new, path)

    insert_after_service = "| **Service discovery** | Resolve an upstream's backends dynamically and refresh the pool live without a reload (`[upstreams.discovery]`): **DNS** A/AAAA and **DNS SRV** in every build, plus **Consul** and **Kubernetes** EndpointSlices behind the `consul`/`kubernetes` build tags — failed or empty resolves keep the last-good backends |"
    backend_rows = """| **Backend trust** | One normalized `backend_tls` policy for private/system roots, backend client certificates, SNI/verified names, minimum TLS and peer identities across HTTP, native gRPC, transcoding/reflection and active health probes. Merged Beta. |
| **Upstream resilience** | Pool-scoped admission and retry budget state, location-overridable stateless controls, bounded queueing, protocol-aware lifetime accounting, and one per-backend circuit state machine reused by HTTP, gRPC, FastCGI/uWSGI and L4 TCP. Integrated closure remains tracked separately. |"""
    if backend_rows not in text:
        text = replace_once(text, insert_after_service, insert_after_service + "\n" + backend_rows, path)

    insert_after_rate = "| **Rate limiting** | Token-bucket request limiting keyed by client IP, a request header, or a JWT claim, with burst, global or per-location policy, and `429` + `Retry-After`; plus a per-listener concurrent-connection cap |"
    identity_row = "| **Trusted client identity** | Per-listener `trusted_proxies` and bounded Forwarded/X-Forwarded-For derivation produce one canonical client address used by CIDR auth, rate limiting, WAF, logs, forwarding and FastCGI. Untrusted assertions are ignored. Merged Beta. |"
    if identity_row not in text:
        text = replace_once(text, insert_after_rate, insert_after_rate + "\n" + identity_row, path)

    insert_after_dx = "| **Developer experience** | Zero-config `jul run --serve`/`--proxy` (no file needed), `jul lint` best-practice checks with CI-friendly exit codes, and `jul fmt` canonical formatting |"
    contracts_row = "| **Generated contracts** | Deterministic JSON Schema, machine metadata, generated field reference and lifecycle reference derive from code-defined authorities; durable `route_id` supports stable route addressability. Merged Beta. |"
    if contracts_row not in text:
        text = replace_once(text, insert_after_dx, insert_after_dx + "\n" + contracts_row, path)

    write(path, text)


def update_roadmap() -> None:
    content = f"""# Jul.IA — Roadmap

> Version {VERSION} · Updated {TODAY}
>
> This roadmap owns the **durable portfolio sequence**. It deliberately does
> not duplicate volatile READY/NEXT/blocked issue state. The current issue-level
> execution tracker is [#62](https://github.com/victornife/jul/issues/62), while
> feature maturity and delivery live in [status.md](../status.md).

Jul.IA's active objective is a coherent, production-quality standalone
single-node edge and protocol gateway. Correctness and security may interrupt
any later investment. Distributed control planes and category expansion remain
separate decisions rather than implicit completion requirements.

## Sources of truth

| Question | Authority |
| --- | --- |
| What does the binary do? | Runtime code, tests, and generated configuration/lifecycle contracts |
| What is GA, Beta, merged, candidate, released, or soaked? | [Feature status](../status.md) and [`feature-status.yaml`](../feature-status.yaml) |
| What is being worked on now? | [Programme tracker #62](https://github.com/victornife/jul/issues/62) |
| What is the durable order of investment? | This roadmap |
| Which dated audit is current or superseded? | [Audit register](../audit-register.md) |

## Portfolio lanes

| Lane | Objective | Decision rule |
| --- | --- | --- |
| **Correctness and security** | Correct unsafe, misleading, protocol-invalid, or lifecycle-invalid behavior | May pre-empt every other lane |
| **Core Gateway Completeness** | Close material gaps inside the standalone gateway boundary | Architecture and product integrity, not feature-count parity |
| **Operational enhancement** | Improve long-running operation and recovery | Value and leverage must justify permanent complexity |
| **Migration and diagnostics** | Make adoption, evidence and support safer | No compatibility percentage, silent approximation, phone-home, or unsafe replay |
| **Technical experiment** | Test one bounded category hypothesis | Explicit entry gate, time box, and promote/freeze/extract/remove/defer decision |
| **Vision horizon** | Preserve possible distributed or category-expansion futures | Requires a separate activation decision |

## Active operating roadmap

| Stage | Durable focus | Current snapshot |
| --- | --- | --- |
| **0 — Programme and product truth** | One tracker, audit disposition, operating model and product boundary | Complete; this issue reconciles later documentation drift |
| **1 — Correctness foundation** | Strict config, protocol/security corrections, cache recertification and quality gates | Complete for the selected tranche; new defects still interrupt later stages |
| **2 — Lifecycle and structured configuration** | Closed-world lifecycle authority, transactional apply/stage/rollback, typed workflows | Complete |
| **3 — Trust boundaries** | Canonical client identity and consistent backend TLS/mTLS identity | Implemented on `main`; represented as merged Beta capabilities |
| **4 — Routing and response policy** | Method/header/query predicates, response headers, CORS and typed operation surfaces | Implemented on `main`; represented separately from the older Core HTTP GA row |
| **5 — Generic resilience** | Admission, queue/connection bounds, retry budget/deadline/backoff, circuit state and bounded operations evidence | Core implementations are merged; integrated cross-protocol/soak and complete external-contract closure remain under #287/#144 at this baseline |
| **6 — Configuration authority and automation** | Managed/file-owned authority, generated contracts, supported external API, thin remote CLI | Authority and generated contracts are merged; external OpenAPI #150 and CLI #151 remain separate gates |
| **7 — Selected runtime dynamics** | High-value certificate, credential, logging, sink, cache-policy and Alt-Svc transitions | Planned and value-ranked; universal hot reload is not a requirement |
| **8 — Migration and diagnostics** | NGINX assessment/provenance/includes, compatibility corpus, support bundle and `jul doctor` | Assessment/provenance/includes are merged; corpus work has started; support bundle and doctor remain later work |
| **9 — One bounded experiment** | AI Gateway or another explicitly approved category | Gated; not an automatic continuation of core work |
| **10 — Integrated closure** | Fresh exact-SHA audit, protocol/failure matrix, lean/full gates, E2E, soak and release evidence | Planned after the selected programme |

For exact issue state, child decomposition, active pull requests and sequencing,
read #62. This table changes only when the durable portfolio boundary or stage
outcome changes.

## Current programme boundary

### Complete foundations

- The selected cache correction and recertification programme is complete; the
  response cache retains GA.
- Closed-world lifecycle classification and generated lifecycle mirrors are
  complete.
- Structured configuration Phase 5 is complete.
- ADRs 0016–0019 define trust, resilience, routing/response policy, authority,
  generated contracts and resource identity.
- Canonical inbound identity, backend trust, routing/response policy,
  configuration authority and generated configuration contracts are implemented
  on `main`.

### Active or incomplete closure

- New post-RC capabilities are not promoted through older GA rows; their
  maturity and delivery remain explicit in [status.md](../status.md).
- Resilience still requires the remaining integrated evidence/external-contract
  closure tracked by #287/#144.
- The supported versioned external Admin API and remote CLI remain #150/#151;
  current Console routes are not automatically the stable external contract.
- NGINX compatibility corpus and selected-dimension E2E continue after the
  assessment/provenance/include foundation.

## Release-candidate checkpoint

`v1.32.1-rc.1` is an immutable published prerelease at
`9a936d0cc1bc3f7086f38ca87741d9d09f950e25`. Its release-path checks, platform
matrix, checksums, embedded SBOMs and attestations are recorded in the
[candidate evidence](../release-candidates/v1.32.1-rc.1.md).

Current `main` is intentionally ahead of that checkpoint. A later stable tag is
a separate publication decision and must reconcile the changelog, status,
security posture, limitations and exact artifacts for that SHA.

## Core Gateway Completeness boundary

The standalone product includes:

- HTTP/1.1, HTTP/2/h2c, HTTP/3, TLS/mTLS, gRPC and optional L4 proxying;
- deterministic request routing and bounded response policy;
- trusted client identity and backend peer identity;
- balancing, health, discovery and generic resilience;
- security policy, secrets and auxiliary egress controls;
- strict configuration, lifecycle, apply, stage, rollback and history;
- generated configuration contracts and supported automation surfaces;
- observability, diagnostics and operational recovery;
- explicit NGINX migration assessment;
- bounded WASM extensibility and supported release profiles.

The following remain outside the core boundary unless a later ADR changes it:
production fleet control plane, Kubernetes Gateway API controller, distributed
cache/rate limiting, hosted cloud, service mesh, GSLB/CDN, GraphQL composition,
AI Gateway, and full parity with NGINX/Envoy/Kong/Caddy/Traefik.

## Selected runtime dynamics

The product may finish with many fields deliberately restart-required. Selected
runtime changes must reuse the existing transactional preparation/publication
and resource-lifetime models rather than introducing a universal callback
framework. Current candidates include certificate material, admin credentials,
access-log sinks, selected cache scalars and Alt-Svc advertisement state.

A complete and truthful `stage_restart` path is an acceptable final design for
unselected or structural transitions.

## Migration and diagnostics

The migration lane is evidence-oriented:

- deterministic per-directive assessment rather than a compatibility score;
- source provenance and bounded root-confined include traversal;
- a sanitized, licensed corpus with selected-dimension comparison;
- no automatic production cutover or unsafe traffic replay;
- support bundles and diagnostics that are explicit, bounded and secret-safe;
- no phone-home or automatic upload.

## Experiment governance

At most one major category-expansion experiment is active. It must declare its
hypothesis, prerequisites, dependency/binary budget, test strategy, time box and
exit decision. Generic trust, resilience, streaming ownership, secrets and
observability must be reused rather than duplicated inside the experiment.

## Completion evidence

The selected programme closes only with:

- a fresh exact-SHA source and documentation audit;
- consistent feature maturity/delivery records;
- lean/full/build-tag and cross-platform verification;
- real H1/H2/h2c/H3/TLS/mTLS/gRPC/L4 protocol suites;
- failure-boundary, race/leak, browser E2E and long-running soak evidence;
- bounded-label, secret/privacy and compatibility review;
- release notes, residual limitations and an explicit publication decision.

## Historical relationship

Earlier phase-by-phase roadmaps, audit findings and delivery notes remain in Git
history, issue comments, the changelog and dated audit records. They are
historical evidence, not a second active roadmap. When current issue-level state
changes, update #62; update this document only when the durable portfolio or a
stage outcome changes.
"""
    write("docs/roadmap/README.md", content)


def update_index(features: list[dict]) -> None:
    feature_links = []
    seen = set()
    for feature in features:
        doc = feature["doc"]
        key = (feature["name"], doc)
        if key in seen:
            continue
        seen.add(key)
        feature_links.append(f"| {feature['name']} | [{doc}]({doc}) | `{feature['maturity']}` / `{feature['delivery']}` |")

    content = f"""# Jul.IA documentation

Welcome to the Jul.IA documentation. Jul.IA is a standalone edge and protocol
gateway written in Go, configured through TOML, and shipped as a static binary.

## Start with product truth

- **[Feature status and evidence](status.md)** — canonical human view of maturity
  and delivery; generated from/checkable against
  [`feature-status.yaml`](feature-status.yaml).
- **[Programme tracker #62](https://github.com/victornife/jul/issues/62)** —
  current issue-level execution state.
- **[Roadmap](roadmap/README.md)** — durable portfolio order, deliberately not a
  second issue tracker.
- **[Audit register](audit-register.md)** — current disposition of dated audits
  and historical evidence.
- **[Known limitations](known-limitations.md)** — active defects, deliberate
  limits, merged/unreleased constraints and restart-bound/deferred behavior.

## New to Jul.IA?

- **[README](../README.md)** — product scope, installation and capability
  overview.
- **[Getting started](getting-started.md)** — zero-config, static, proxy and TLS
  examples.
- **[Configuration concepts](configuration.md)** — human-authored structure,
  examples, trade-offs and workflows.
- **[Generated configuration reference](generated/config-reference.md)** —
  exhaustive field-level types, defaults, lifecycle, capabilities and values.
- **[JSON Schema](generated/config.schema.json)** and
  **[machine metadata](generated/config-metadata.json)** — generated machine
  contracts; runtime validation remains authoritative.
- **[Troubleshooting](troubleshooting.md)** — common first-run and operational
  problems.
- **[Concepts appendix](vision/appendix.md)** — HTTP, proxy, TLS, caching and
  observability foundations.

## Operating and evaluating

- **[Deployment](deployment.md)** — systemd, Windows service, Docker and log
  rotation.
- **[Reload, staging, authority and rollback](reload-semantics.md)** —
  transactional reload, planned restart, managed/file-owned authority and
  generation lifetimes.
- **[Generated lifecycle reference](generated/config-lifecycle.md)** and
  **[machine lifecycle metadata](generated/config-lifecycle.json)** — exhaustive
  field-level lifecycle truth from the Go registry.
- **[Observability](observability.md)** — logs, metrics, tracing, probes and
  runtime surfaces.
- **[Prometheus contract](metrics-contract.json)** — metric names, types, labels
  and release state.
- **[Security model](../SECURITY.md)** and **[security posture](security-posture.md)**
  — threat boundaries and production hardening.
- **[Compatibility policy](compatibility.md)** — SemVer, deprecation and stable
  contract boundaries.
- **[Release process](release.md)** and **[soak evidence](soak-evidence.md)**.

## Feature guides

| Capability | Canonical guide | Status |
| --- | --- | --- |
{chr(10).join(feature_links)}

Some capabilities share a canonical guide because they compose one subsystem.
The status manifest still gives each additive capability its own maturity and
delivery row where inheriting an older GA label would be misleading.

## Migration and automation

- **[NGINX importer](nginx-importer.md)** — translation boundary and supported
  constructs.
- **[NGINX migration assessment](nginx-assessment.md)** — schema-v2 findings,
  provenance, guidance and bounded include traversal.
- **[Configuration authority](reload-semantics.md#configuration-authority-managed-vs-file-owned)**
  — one writer at a time.
- **Generated external API:** not yet the stable contract. Existing Console
  routes remain internal unless explicitly classified by #150.

## Architecture and contribution

- **[Project layout and architecture](architecture.md)**.
- **[ADRs](adr/README.md)** — durable design decisions.
- **[Core Gateway Completeness](specs/core-gateway-completeness.md)** — approved
  standalone-product boundary.
- **[Operating model](operating-model.md)** — portfolio, evidence, WIP and
  experiment rules.
- **[Engineering specs](specs/)** and **[vision](vision/README.md)**.
- **[Contributing](../CONTRIBUTING.md)** and **[changelog](../CHANGELOG.md)**.

## Build tags

| Tag | Capability |
| --- | --- |
| `acme` | Automatic HTTPS |
| `brotli` / `zstd` | Optional compression encoders |
| `console` | Embedded operations Console |
| `consul` / `kubernetes` | Optional discovery providers |
| `grpc` | Native gRPC and transcoding |
| `http3` | HTTP/3 over QUIC |
| `importer` | NGINX migration tooling |
| `otel` | OpenTelemetry tracing |
| `stream` | L4 TCP/UDP proxy |
| `waf` | Coraza WAF |
| `wasmplugins` | WASM runtime |

The default binary is lean. The full profile uses the tag list maintained in CI,
release workflows and the Makefile.

## Documentation contract

Runtime behavior and generated contracts outrank prose. `feature-status.yaml`
owns maturity/delivery, #62 owns volatile execution, the roadmap owns durable
sequence, feature guides own operation and limitations, and the audit register
owns current audit disposition. Historical audits retain their original claims
and dates.

Open an issue or pull request when those sources disagree; do not create another
parallel status registry.
"""
    write("docs/index.md", content)


def update_audit_register() -> None:
    path = "docs/audit-register.md"
    text = read(path)
    split = text.find("\n---\n")
    if split < 0:
        raise RuntimeError(f"{path}: historical separator missing")
    remainder = text[split + len("\n---\n"):]
    top = f"""# Audit Register

This page identifies the current audit disposition and preserves earlier audit
evidence. Issue closure alone is not audit evidence, and a dated audit is not a
live issue tracker.

## Current authoritative records

| Record | Source baseline | Current role | Disposition |
| --- | --- | --- | --- |
| [2026-08-07 response-cache recertification](audit/2026-08-07-cache-recertification.md) | Post-#131/#132/#133 cache tree | Current cache conformance and retained-GA evidence | Complete; #107/#134 closed |
| [2026-08-03 combined repository re-audit](audit/combined-audit-2026-08-03.md) | `66c71b2c...` | Dated programme-opening audit and historical finding source | Superseded for current issue state by #62 and later implementation evidence; not rewritten retrospectively |
| [Stage 0/1 programme closure](audit/2026-08-05-stage-0-1-programme-closure.md) | `0de8541e...` | Exact-SHA programme-foundation disposition | Complete, with residuals transferred explicitly |
| [2026-07-31 full repository audit](audit/2026-07-31-full-repository-audit.md) | `e8865615` plus recorded remediations | Historical audit and remediation evidence | Maintainer-certified and superseded under #130; no independent two-human certification claimed |

## Current programme disposition

- Cache correctness/recertification, closed-world lifecycle authority and
  structured configuration Phase 5 are complete.
- ADRs 0016–0019 are accepted. Canonical client identity, backend trust,
  routing/response policy, configuration authority and generated contracts are
  implemented on `main`.
- Those post-RC capabilities keep their own maturity and delivery rows in
  [status.md](status.md); implementation does not imply stable publication or GA.
- Generic resilience implementations are substantially merged, while #287/#144
  retain integrated evidence/external-contract closure at the issue #353
  baseline.
- NGINX assessment, provenance and bounded include traversal are merged;
  compatibility-corpus and later diagnostics work continue separately.
- The versioned supported external Admin API and remote CLI remain #150/#151.
- Selected runtime dynamics, support bundle, `jul doctor` and the bounded AI
  experiment remain later portfolio decisions.

Volatile issue-level sequencing is owned by
[#62](https://github.com/victornife/jul/issues/62). Feature maturity/delivery is
owned by [`feature-status.yaml`](feature-status.yaml). The
[roadmap](roadmap/README.md) records only durable portfolio order.

A release closure entry must identify the exact SHA, commands actually run,
workflow evidence, unavailable lanes and residual risk.

---
"""
    write(path, top + remainder)


def update_known_limitations() -> None:
    path = "docs/known-limitations.md"
    text = read(path)
    split = text.find("\n---\n")
    if split < 0:
        raise RuntimeError(f"{path}: first separator missing")
    remainder = text[split + len("\n---\n"):]
    top = """# Jul.IA — Known limitations

This page separates active defects from deliberate product boundaries,
merged-but-unreleased constraints, restart-bound/deferred behavior and historical
corrections. A limitation is not a place to hide a correctness defect, and a
closed issue must not remain phrased as future work.

## Active correctness or security defects

No repository-wide P0/P1 defect is being declared by this document at the issue
#353 baseline. Newly discovered correctness/security findings still pre-empt the
roadmap and must be tracked in focused issues with tests and disposition.

## Implemented on `main`, not yet stable GA publication

- **Trusted client address (`client_address`):** merged Beta; stable tag and
  long-running soak promotion remain open.
- **Backend TLS trust (`backend_tls`):** merged Beta across HTTP, native gRPC,
  transcoding/reflection and active health probes; stable tag/soak promotion
  remains open.
- **Routing and response policy:** method/header/query predicates,
  response-header operations and CORS are merged after the current RC.
- **Generic resilience:** admission, retry and circuit implementations are
  merged; #287/#144 retain the integrated race/fuzz/soak and complete
  external-contract closure at this baseline.
- **Configuration authority/generated contracts:** managed/file-owned authority,
  drift/adoption, route identity, JSON Schema, metadata and generated reference
  are merged; the supported external API and remote CLI remain #150/#151.
- **NGINX assessment/provenance/includes:** schema-v2 assessment and bounded
  source traversal are merged separately from the released base importer GA row.
- **Auxiliary egress allow-list:** present in `v1.32.1-rc.1`; the prerelease is
  not a stable publication.

## Deliberate product boundaries

- Single-node operation; no production fleet control plane.
- No Kubernetes Gateway API controller or service-mesh/xDS control plane.
- No distributed cache, rate limit, circuit state or global quota.
- No full NGINX/Envoy/Kong/Caddy/Traefik parity.
- No automatic migration cutover, one-dimensional compatibility percentage,
  unsafe traffic replay, phone-home or automatic support upload.
- No arbitrary expression language or embedded general-purpose scripting in
  core routing policy.

## Restart-bound or deferred behavior

Field-level lifecycle authority is the Go registry rendered in
[`generated/config-lifecycle.md`](generated/config-lifecycle.md). Structural or
unselected transitions may remain restart-required; the complete
`stage_restart` workflow is an acceptable final product design. Selected future
candidates include certificate material, admin credentials, access-log sinks,
selected cache scalars and Alt-Svc advertisement state.

## Historical corrections

The response-cache defects found by the combined audit were corrected by
#131/#132/#133 and recertified by #134; the cache retains GA. Closed-world
lifecycle authority (#89), structured configuration (#77–#82), trust, routing,
authority/generated-contract and NGINX assessment foundations are also complete
on `main`. Their dated audit records remain evidence, not current defect lists.

---
"""
    text = top + remainder
    stale_probe = re.compile(
        r"- \*\*Health probes verify against the system roots only\.\*\* An `https` pool behind\n"
        r"  a private CA is reported unhealthy until probes consume the resolved policy;\n"
        r"  `jul lint` warns about the combination\.\n"
    )
    text, count = stale_probe.subn(
        "- **Health probes use the pool-level resolved policy.** A route-level override cannot govern a shared pool probe; put the roots and client identity required by the probe on the upstream.\n",
        text,
        count=1,
    )
    if count != 1:
        raise RuntimeError(f"{path}: stale backend-health limitation not found")
    write(path, text)


def update_compatibility() -> None:
    path = "docs/compatibility.md"
    text = read(path)
    text = re.sub(r"> Version \d+\.\d+ · Updated \d{4}-\d{2}-\d{2}", f"> Version 1.7 · Updated {TODAY}", text, count=1)
    old = "3. **Admin/Console HTTP API** — documented `/api/*` request/response shapes the\n   Console depends on."
    new = "3. **Supported external Admin API** — only endpoints explicitly classified in a versioned external contract are stable. Existing unversioned Console `/api/*` routes remain internal unless separately listed; #150 owns the first supported `/api/v1` subset."
    text = replace_once(text, old, new, path)

    marker = "## What it does not cover\n"
    section = """## Delivery state and compatibility

Compatibility promises attach to a released contract, not merely to code merged
on `main`. `implemented`, `merged`, and `candidate` capabilities may be usable
and documented while remaining Beta and outside the stable GA surface. The
canonical classification is [`feature-status.yaml`](feature-status.yaml).

The published `v1.32.1-rc.1` is a prerelease, not a stable tag. Later `main`
changes — including trusted client identity, backend trust, routing/response
policy, resilience, configuration authority/generated contracts, and NGINX
assessment/provenance/includes — do not become released compatibility promises
until an explicit publication and maturity decision says so.

Existing Console routes are implementation surfaces for the embedded UI. This
policy does not accidentally freeze all of them as an external automation API.
The supported versioned subset, common errors, auth/RBAC and deprecation rules
are owned by #150.

"""
    text = replace_once(text, marker, section + marker, path)
    changelog = "| Date | Ver | What changed | What stayed | Source |\n| --- | --- | --- | --- | --- |\n"
    row = f"| {TODAY} | 1.7 | Separated released compatibility from merged/candidate delivery and clarified that unversioned Console routes are internal until #150 publishes a supported external API subset. | Existing released GA configuration, CLI, metric and wire contracts remain governed by SemVer. | Issue #353; [status.md](status.md) |\n"
    if row not in text:
        text = replace_once(text, changelog, changelog + row, path)
    write(path, text)


def update_configuration() -> None:
    path = "docs/configuration.md"
    text = read(path)
    old = "The `[global]` block controls process-wide settings: logging, worker parallelism,\nand the graceful-shutdown deadline. These values apply to the entire server\ninstance and are read once at startup."
    new = "The `[global]` block controls process-wide settings. Lifecycle is field-specific: some values are hot, some are startup-bound, and authority changes are deliberately restart-required. The generated [lifecycle reference](generated/config-lifecycle.md) is authoritative; this section explains meaning and workflow rather than duplicating every disposition."
    text = replace_once(text, old, new, path)
    old2 = "The backend/API operations are available in #80. The Global and Traffic\nControls form migration is #81; current guided TOML and raw TOML workflows\nremain supported."
    new2 = "The structured global operations and matching Global/Traffic Controls Console forms are shipped. Guided editors and raw TOML remain supported surfaces over the same server-side validation, lifecycle and apply engine."
    text = replace_once(text, old2, new2, path)
    write(path, text)


def update_feature_headers() -> None:
    edits = {
        "docs/upstreams.md": (
            "> **Maturity:** the pool itself is GA (see [status.md](status.md)); `backend_tls`\n> is new and its lifecycle is deliberately conservative — see\n> [Reload behaviour](#reload-behaviour).",
            "> **Maturity and delivery:** the released pool/balancing/health foundation is GA. `backend_tls` and the newer admission/retry/circuit surface are separate merged Beta capabilities on current `main`; #287/#144 retain integrated resilience closure. See [status.md](status.md) and [Reload behaviour](#reload-behaviour).",
        ),
        "docs/core-http.md": (
            "> **Maturity:** GA (see [ADR 0003](adr/0003-maturity-and-ga.md)). TLS termination\n> is documented in [tls-acme.md](tls-acme.md); client certificates in\n> [mtls.md](mtls.md).",
            "> **Maturity and delivery:** the released Core HTTP foundation is GA. Method/header/query predicates, response-header policy/CORS, and the newer resilience taxonomy are additive merged Beta contracts and do not inherit the base row automatically. See [status.md](status.md). TLS termination is documented in [tls-acme.md](tls-acme.md); client certificates in [mtls.md](mtls.md).",
        ),
        "docs/nginx-importer.md": (
            "> Feature ID: **Y1-09** · Build tag: `importer` · Since v1.26",
            "> Base importer: **Y1-09, GA/soaked** · Assessment/provenance/includes: **MIG-ASSESS, Beta/merged** · Build tag: `importer`",
        ),
        "docs/nginx-assessment.md": (
            "> Feature ID: **Y1-09** · Build tag: `importer` · Report schema: **2**",
            "> Feature ID: **MIG-ASSESS** · Maturity/delivery: **Beta / merged** · Build tag: `importer` · Report schema: **2**",
        ),
    }
    for path, (old, new) in edits.items():
        text = read(path)
        text = replace_once(text, old, new, path)
        write(path, text)

    path = "docs/console.md"
    text = read(path)
    intro = "# Jul.IA Console\n\n"
    note = "# Jul.IA Console\n\n> **Maturity and API boundary:** the released embedded Console foundation is GA. Newer authority, route-policy and resilience panels on current `main` retain their own maturity rows. Existing unversioned `/api/*` routes are Console-internal unless #150 explicitly publishes them in the supported external `/api/v1` contract.\n\n"
    text = replace_once(text, intro, note, path)
    write(path, text)


def update_release_doc() -> None:
    path = "docs/release.md"
    text = read(path)
    pattern = re.compile(
        r"The \[current roadmap checkpoint\]\(roadmap/README\.md#release-candidate-checkpoint\)\n"
        r"is the independently verified, published `v1\.32\.1-rc\.1` prerelease at\n"
        r"`9a936d0cc1bc3f7086f38ca87741d9d09f950e25`\..*?\n\nBefore creating any tag:",
        re.S,
    )
    replacement = """The [current roadmap checkpoint](roadmap/README.md#release-candidate-checkpoint)
is the independently verified, published `v1.32.1-rc.1` prerelease at
`9a936d0cc1bc3f7086f38ca87741d9d09f950e25`. Its exact evidence is recorded in
[`docs/release-candidates/v1.32.1-rc.1.md`](release-candidates/v1.32.1-rc.1.md).
Current `main` contains substantial later work; consult [status.md](status.md)
for maturity/delivery and do not infer that a merged capability is present in
the RC. A later stable tag requires a separate publication decision and fresh
release run; the RC tag is never renamed or reused.

Before creating any tag:"""
    text, count = pattern.subn(replacement, text, count=1)
    if count != 1:
        raise RuntimeError(f"{path}: release-candidate paragraph not found")
    write(path, text)


def update_changelog() -> None:
    path = "CHANGELOG.md"
    text = read(path)
    marker = "### Added\n"
    row = "- **Documentation product-truth reconciliation (#353):** separated feature maturity from delivery state; made `docs/feature-status.yaml` authoritative for both; reconciled README, status, roadmap, documentation index, audit disposition, limitations, compatibility and feature-guide framing with current `main`; explicitly split post-RC client identity, backend trust, routing/response policy, resilience, configuration authority/generated contracts and NGINX assessment/provenance/include work from older GA rows; added drift checks for manifest/status/README/index/version coherence.\n"
    if row not in text:
        text = replace_once(text, marker, marker + row, path)
    write(path, text)


def update_docs_check() -> None:
    path = "scripts/docs-check.py"
    text = read(path)
    old_function = re.search(r"def check_feature_status_manifest\(\):.*?\n\ndef _cell_to_criterion", text, re.S)
    if not old_function:
        raise RuntimeError(f"{path}: check_feature_status_manifest function not found")
    new_function = r'''def check_feature_status_manifest():
    """Validate the canonical maturity/delivery manifest and its human surfaces."""
    try:
        import yaml
    except ModuleNotFoundError:
        error(ROOT / "docs" / "feature-status.yaml", 0,
              "pyyaml is required for YAML manifest checks — install with: pip install pyyaml")
        return

    manifest = DOCS / "feature-status.yaml"
    if not manifest.exists():
        error(manifest, 0, "feature-status.yaml is missing from docs/")
        return
    try:
        data = yaml.safe_load(manifest.read_text(encoding="utf-8"))
    except yaml.YAMLError as exc:
        error(manifest, 0, f"feature-status.yaml is not valid YAML: {exc}")
        return
    ok("feature-status.yaml parses as valid YAML")

    features = data.get("features") if isinstance(data, dict) else None
    if not isinstance(features, list):
        error(manifest, 0, "feature-status.yaml missing required 'features' list")
        return

    allowed_maturity = {"GA", "GA-soak-pending", "Beta", "Alpha", "Deprecated"}
    allowed_delivery = {"implemented", "merged", "candidate", "released", "soaked"}
    seen_ids = set()
    index_path = DOCS / "index.md"
    index_text = index_path.read_text(encoding="utf-8") if index_path.exists() else ""

    for entry in features:
        feat_id = str(entry.get("id", "")).strip()
        name = str(entry.get("name", "?")).strip()
        maturity = entry.get("maturity")
        delivery = entry.get("delivery")
        doc = str(entry.get("doc", "")).strip()

        if not feat_id:
            error(manifest, 0, f"feature '{name}' has no ID")
        elif feat_id in seen_ids:
            error(manifest, 0, f"duplicate feature ID '{feat_id}'")
        else:
            seen_ids.add(feat_id)

        if maturity not in allowed_maturity:
            error(manifest, 0, f"feature {feat_id} has invalid maturity {maturity!r}")
        if delivery not in allowed_delivery:
            error(manifest, 0, f"feature {feat_id} has invalid delivery {delivery!r}")
        if maturity == "GA" and delivery != "soaked":
            error(manifest, 0, f"feature {feat_id} is GA but delivery is {delivery!r}, not 'soaked'")
        if maturity == "GA-soak-pending" and delivery != "released":
            error(manifest, 0, f"feature {feat_id} is GA-soak-pending but delivery is {delivery!r}, not 'released'")

        if doc:
            doc_path = DOCS / doc
            if not doc_path.exists():
                error(manifest, 0, f"feature '{name}' references missing doc: {doc}")
            else:
                ok(f"feature-status.yaml: doc {doc} exists")
            if f"]({doc})" not in index_text and f"]({doc}#" not in index_text:
                error(index_path, 0, f"feature {feat_id} canonical guide '{doc}' is not linked from docs/index.md")

    readme_path = ROOT / "README.md"
    readme = readme_path.read_text(encoding="utf-8") if readme_path.exists() else ""
    has_non_ga = any(entry.get("maturity") not in {"GA", "Deprecated"} for entry in features)
    if has_non_ga and re.search(r"all shipped features are GA|All shipped features meet", readme, re.I):
        error(readme_path, 0, "README claims every shipped feature is GA while the manifest contains non-GA entries")

    status_doc = DOCS / "status.md"
    if not status_doc.exists():
        error(manifest, 0, "docs/status.md is missing for cross-check")
        return
    status_rows = _parse_status_md_rows(status_doc.read_text(encoding="utf-8"))
    for entry in features:
        feat_id = entry.get("id", "")
        row = status_rows.get(feat_id)
        if row is None:
            error(manifest, 0, f"feature ID '{feat_id}' ({entry.get('name')}) has no parseable row in docs/status.md")
            continue
        _compare_feature_row(manifest, entry, row)


def _cell_to_criterion'''
    text = text[:old_function.start()] + new_function + text[old_function.end():]

    parse_match = re.search(r"def _parse_status_md_rows\(text: str\).*?\n\ndef _compare_feature_row", text, re.S)
    if not parse_match:
        raise RuntimeError(f"{path}: status row parser not found")
    parser = r'''def _parse_status_md_rows(text: str) -> dict[str, dict]:
    """Parse maturity tables in status.md, returning id -> row data."""
    rows: dict[str, dict] = {}
    maturity = None
    in_table = False
    for line in text.splitlines():
        if line == "## GA":
            maturity = "GA"
            in_table = False
            continue
        if line == "## GA — soak pending":
            maturity = "GA-soak-pending"
            in_table = False
            continue
        if line == "## Beta":
            maturity = "Beta"
            in_table = False
            continue
        if line == "## Alpha":
            maturity = "Alpha"
            in_table = False
            continue
        if line == "## Deprecated":
            maturity = "Deprecated"
            in_table = False
            continue
        if line.startswith("## ") and line not in {
            "## GA", "## GA — soak pending", "## Beta", "## Alpha", "## Deprecated"
        }:
            maturity = None
            in_table = False

        if not line.startswith("|"):
            in_table = False
            continue
        cells = [c.strip() for c in line.strip("|").split("|")]
        if cells and cells[0] == "Feature" and "Delivery" in cells:
            in_table = True
            continue
        if all(re.match(r"^[:\-]+$", c) for c in cells if c):
            continue
        if not in_table or maturity is None or len(cells) < 14:
            continue
        feat_id = cells[1]
        if not feat_id or feat_id in {"—", "-", "ID"}:
            continue
        rows[feat_id] = {
            "name": cells[0],
            "tag": cells[2],
            "delivery": cells[3].strip("`"),
            "maturity": maturity,
            "criteria": {i: _cell_to_criterion(cells[3 + i]) for i in range(1, 10)},
            "doc": cells[13],
        }
    return rows


def _compare_feature_row'''
    text = text[:parse_match.start()] + parser + text[parse_match.end():]

    compare_match = re.search(r"def _compare_feature_row\(manifest: Path, entry: dict, row: dict\):.*?\n\ndef _run_lifecycle_generator_check", text, re.S)
    if not compare_match:
        raise RuntimeError(f"{path}: compare function not found")
    compare = r'''def _compare_feature_row(manifest: Path, entry: dict, row: dict):
    """Compare a feature-status.yaml entry against its status.md table row."""
    feat_id = entry.get("id", "?")
    name = entry.get("name", "?")
    if entry.get("maturity") != row.get("maturity"):
        error(manifest, 0, f"feature {feat_id} ({name}) maturity mismatch: YAML={entry.get('maturity')}, status.md={row.get('maturity')}")
    if entry.get("delivery") != row.get("delivery"):
        error(manifest, 0, f"feature {feat_id} ({name}) delivery mismatch: YAML={entry.get('delivery')}, status.md={row.get('delivery')}")

    yaml_criteria = entry.get("criteria", {})
    row_criteria = row.get("criteria", {})
    for i in range(1, 10):
        yaml_val = yaml_criteria.get(i)
        row_val = row_criteria.get(i)
        if yaml_val != row_val:
            error(manifest, 0, f"feature {feat_id} ({name}) criterion {i} mismatch: YAML={yaml_val}, status.md={row_val}")

    yaml_doc = entry.get("doc", "")
    link_match = re.search(r"\]\(([^)]+)\)", row.get("doc", ""))
    if yaml_doc and (not link_match or link_match.group(1) != yaml_doc):
        error(manifest, 0, f"feature {feat_id} ({name}) doc mismatch: YAML={yaml_doc}, status.md={link_match.group(1) if link_match else None}")
    ok(f"feature-status.yaml: {feat_id} row data matches status.md")


def _run_lifecycle_generator_check'''
    text = text[:compare_match.start()] + compare + text[compare_match.end():]

    insertion = r'''

def check_readme_go_version():
    """README may show exact Go patch or deliberately coarser major.minor, never a stale patch."""
    go_mod = ROOT / "go.mod"
    readme = ROOT / "README.md"
    if not go_mod.exists() or not readme.exists():
        return
    mod_match = re.search(r"^go\s+(\d+\.\d+(?:\.\d+)?)\s*$", go_mod.read_text(encoding="utf-8"), re.M)
    readme_match = re.search(r"\*\*Language:\*\* Go (\d+\.\d+(?:\.\d+)?)", readme.read_text(encoding="utf-8"))
    if not mod_match or not readme_match:
        error(readme, 0, "could not find Go version in go.mod and README language metadata")
        return
    actual = mod_match.group(1).split(".")
    stated = readme_match.group(1).split(".")
    if stated[:2] != actual[:2] or (len(stated) == 3 and stated != actual):
        error(readme, 0, f"README Go version {'.'.join(stated)} disagrees with go.mod {'.'.join(actual)}")
    else:
        ok("README Go version is exact or deliberately major.minor")


def check_living_doc_headers():
    """A living document header cannot be older than its own newest changelog row."""
    version_re = re.compile(r"> Version (\d+\.\d+) · Updated (\d{4}-\d{2}-\d{2})")
    row_re = re.compile(r"^\|\s*(\d{4}-\d{2}-\d{2})\s*\|\s*(\d+\.\d+)\s*\|", re.M)
    for rel in ("status.md", "roadmap/README.md", "compatibility.md"):
        path = DOCS / rel
        if not path.exists():
            continue
        text = path.read_text(encoding="utf-8")
        header = version_re.search(text)
        rows = list(row_re.finditer(text))
        if not header or not rows:
            continue
        newest = max(rows, key=lambda m: (m.group(1), _version_key(m.group(2))))
        if newest.group(1) > header.group(2) or _version_key(newest.group(2)) > _version_key(header.group(1)):
            error(path, 0, f"header {header.group(1)} / {header.group(2)} is older than changelog {newest.group(2)} / {newest.group(1)}")
        else:
            ok(f"{rel} header is current with its changelog")
'''
    text = replace_once(text, "\ndef main():\n", insertion + "\ndef main():\n", path)
    text = replace_once(text, "    check_roadmap_active_ids()\n", "    check_roadmap_active_ids()\n    check_readme_go_version()\n    check_living_doc_headers()\n", path)
    write(path, text)


def update_docs_check_tests() -> None:
    path = "scripts/test_docs_check.py"
    text = read(path)
    tests = r'''

# ── Product-truth drift guards (issue #353) ─────────────────────────────────


def _write_feature_truth_tree(root: Path, *, readme_claim=False, index_link=True, delivery="merged"):
    docs = root / "docs"
    docs.mkdir(parents=True)
    (docs / "feature.md").write_text("# Feature\n", encoding="utf-8")
    (docs / "feature-status.yaml").write_text(
        "version: 2\nupdated: 2026-08-30\nfeatures:\n"
        "  - id: F-1\n"
        "    name: Feature one\n"
        "    tags: [core]\n"
        "    maturity: Beta\n"
        f"    delivery: {delivery}\n"
        "    doc: feature.md\n"
        "    criteria: {1: true, 2: null, 3: true, 4: false, 5: false, 6: true, 7: true, 8: null, 9: null}\n",
        encoding="utf-8",
    )
    link = "[feature](feature.md)" if index_link else "No feature link"
    (docs / "index.md").write_text(f"# Index\n\n{link}\n", encoding="utf-8")
    (docs / "status.md").write_text(
        "# Status\n\n## Beta\n\n"
        "| Feature | ID | Tag | Delivery | 1 | 2 | 3 | 4 | 5 | 6 | 7 | 8 | 9 | Doc |\n"
        "| --- | --- | --- | --- | :-: | :-: | :-: | :-: | :-: | :-: | :-: | :-: | :-: | --- |\n"
        f"| Feature one | F-1 | core | `{delivery}` | ✅ | n/a | ✅ | ☐ | ☐ | ✅ | ✅ | n/a | n/a | [feature.md](feature.md) |\n",
        encoding="utf-8",
    )
    claim = "All shipped features are GA.\n" if readme_claim else "Maturity is in the manifest.\n"
    (root / "README.md").write_text(f"# Repo\n\n{claim}", encoding="utf-8")


def test_feature_manifest_rejects_readme_all_ga_claim():
    with tempfile.TemporaryDirectory() as tmpdir:
        root = Path(tmpdir)
        _write_feature_truth_tree(root, readme_claim=True)
        _, fail = _run_in_tmp(root, docs_check.check_feature_status_manifest)
        assert fail == 1, f"expected one failure, got {fail}"


def test_feature_manifest_requires_index_discoverability():
    with tempfile.TemporaryDirectory() as tmpdir:
        root = Path(tmpdir)
        _write_feature_truth_tree(root, index_link=False)
        _, fail = _run_in_tmp(root, docs_check.check_feature_status_manifest)
        assert fail == 1, f"expected one failure, got {fail}"


def test_feature_manifest_compares_delivery_state():
    with tempfile.TemporaryDirectory() as tmpdir:
        root = Path(tmpdir)
        _write_feature_truth_tree(root, delivery="merged")
        status = root / "docs" / "status.md"
        status.write_text(status.read_text(encoding="utf-8").replace("`merged`", "`candidate`"), encoding="utf-8")
        _, fail = _run_in_tmp(root, docs_check.check_feature_status_manifest)
        assert fail == 1, f"expected one failure, got {fail}"


def test_readme_go_version_accepts_major_minor_and_rejects_stale_patch():
    with tempfile.TemporaryDirectory() as tmpdir:
        root = Path(tmpdir)
        (root / "go.mod").write_text("module example\n\ngo 1.26.6\n", encoding="utf-8")
        (root / "README.md").write_text("- **Language:** Go 1.26\n", encoding="utf-8")
        _, fail = _run_in_tmp(root, docs_check.check_readme_go_version)
        assert fail == 0, f"expected coarse major.minor to pass, got {fail}"
        (root / "README.md").write_text("- **Language:** Go 1.26.5\n", encoding="utf-8")
        _, fail = _run_in_tmp(root, docs_check.check_readme_go_version)
        assert fail == 1, f"expected stale patch to fail, got {fail}"


def test_living_doc_header_detects_newer_changelog():
    with tempfile.TemporaryDirectory() as tmpdir:
        root = Path(tmpdir)
        docs = root / "docs"
        docs.mkdir(parents=True)
        (docs / "compatibility.md").write_text(
            "# Compatibility\n\n> Version 1.1 · Updated 2026-08-04\n\n"
            "| Date | Ver | Change |\n| --- | --- | --- |\n"
            "| 2026-08-19 | 1.6 | Newer |\n",
            encoding="utf-8",
        )
        _, fail = _run_in_tmp(root, docs_check.check_living_doc_headers)
        assert fail == 1, f"expected stale header failure, got {fail}"
'''
    if "test_feature_manifest_rejects_readme_all_ga_claim" not in text:
        text += tests
    write(path, text)


def validate_no_stale_phrases() -> None:
    checks = {
        "README.md": ["all shipped features are GA", "currently under correctness recertification", "Go 1.26.5", "roadmap v2.0"],
        "docs/roadmap/README.md": ["#115 is READY / NEXT", "#117 READY / NEXT, not started"],
        "docs/audit-register.md": ["#115 is READY / NEXT and not started", "#135-#151 remains gated"],
        "docs/known-limitations.md": ["#89 will make every public configuration leaf", "Health probes verify against the system roots only"],
    }
    for path, phrases in checks.items():
        text = read(path)
        for phrase in phrases:
            if phrase.lower() in text.lower():
                raise RuntimeError(f"{path}: stale phrase remains: {phrase}")


def main() -> None:
    features = feature_rows()
    update_manifest(features)
    update_status(features)
    update_readme()
    update_roadmap()
    update_index(features)
    update_audit_register()
    update_known_limitations()
    update_compatibility()
    update_configuration()
    update_feature_headers()
    update_release_doc()
    update_changelog()
    update_docs_check()
    update_docs_check_tests()
    validate_no_stale_phrases()
    print("issue #353 documentation reconciliation applied")


if __name__ == "__main__":
    main()
