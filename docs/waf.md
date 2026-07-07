# Web application firewall (WAF)

Jul.IA can inspect requests (and optionally responses) against
[ModSecurity](https://github.com/SpiderLabs/ModSecurity)-compatible rules and
block or merely record attacks at the edge — no separate WAF appliance. The
engine is [Coraza](https://github.com/corazawaf/coraza) (pure Go, SecLang), and
the [OWASP Core Rule Set](https://coreruleset.org/) (CRS) is **embedded in the
binary**, so a working SQLi/XSS/RCE ruleset is one flag away. Rules can also be
supplied as your own SecLang files or an inline snippet, enabled globally or per
location, and run in **block** or **detect** mode.

The WAF lives behind the `waf` build tag, so lean builds carry none of the
engine or rule-set weight.

> **Maturity:** Beta (see [ADR 0003](adr/0003-maturity-and-ga.md)). It is off by
> default; turn it on per scope. The recommended rollout is **detect → block**.

## Contents

- [Build tag](#build-tag)
- [How it works](#how-it-works)
- [Configuration](#configuration)
- [Modes](#modes)
- [The OWASP Core Rule Set](#the-owasp-core-rule-set)
- [Custom rules](#custom-rules)
- [Per-location policy](#per-location-policy)
- [Rule ordering](#rule-ordering)
- [Request and response bodies](#request-and-response-bodies)
- [Metrics](#metrics)
- [Middleware ordering](#middleware-ordering)
- [Operational notes](#operational-notes)
- [Limits](#limits)
- [GA status](#ga-status)

## Build tag

The WAF compiles only into builds that include the `waf` tag:

```sh
go build -tags waf ./cmd/jul        # WAF available
go build ./cmd/jul                  # lean build — no WAF
```

A lean binary still *parses* a `[waf]` config block, but it refuses to start
when the WAF is enabled rather than silently serving without protection:

```
waf: configuration enables the WAF but this binary was built without the "waf" tag
```

Build with `-tags waf` (combine with others, e.g. `-tags "console waf stream"`)
to run it. The check is performed once at startup by `waf.Check`.

## How it works

1. At startup (and on each reload) Jul.IA assembles a SecLang directive program
   from your policy — the embedded CRS, your directive files, your inline rules,
   and finally the enforcement-mode line — and compiles it into a Coraza engine.
   A rule that fails to compile fails the reload, so a typo never leaves a
   location unprotected.
2. Each request to a location with the WAF enabled is run through the engine
   (phases 1–4: request line, request headers, request body, and — when
   `response_body_check` is set — the response). Coraza buffers the request body
   up to `request_body_limit` for inspection.
3. If a rule **interrupts** the transaction in **block** mode, the request is
   short-circuited with the configured status (default **403**) and never
   reaches the upstream. In **detect** mode the same match is recorded but the
   request proceeds.
4. Every matched rule that logs is reported to the metrics and structured-log
   hooks: it increments `jul_waf_events_total{action,rule}` and emits a
   `waf rule matched` warning with the rule ID, URI, and message.

## Configuration

The WAF is configured under `[waf]` for a process-wide default, and can be
overridden per location with `[servers.locations.waf]`. The smallest useful
configuration turns on the embedded CRS:

```toml
[waf]
enabled = true
crs_enabled = true        # load the embedded OWASP Core Rule Set
mode = "block"            # block (default) | detect
```

The full set of keys (identical for the global block and a per-location
override):

| Key | Type | Default | Description |
| --- | ---- | ------- | ----------- |
| `enabled` | bool | `false` | Turn the firewall on for the scope it appears in |
| `mode` | string | `block` | `block` (a rule interruption returns `block_status`) or `detect` (record + log only, request proceeds) |
| `block_status` | int | `403` | HTTP status returned when a request is blocked in `block` mode. A rule may still set its own status |
| `crs_enabled` | bool | `false` | Load the embedded OWASP Core Rule Set (no external files needed) |
| `paranoia` | int | `1` | CRS paranoia level `1`–`4` (only with `crs_enabled`). Higher catches more, with more false positives |
| `directives_files` | string list | — | SecLang rule files to `Include`, in order, before the inline rules |
| `inline_rules` | string | — | A SecLang snippet appended last — handy for small tuning/allow-list rules |
| `request_body_limit` | size | `128kb` | How many request-body bytes to buffer for inspection (e.g. `1mb`) |
| `response_body_check` | bool | `false` | Also inspect response bodies (CRS phase 4). Adds latency + memory |

At least one rule source — `crs_enabled`, a `directives_files` entry, or
`inline_rules` — must be present when `enabled` is set; otherwise the config is
rejected at validation.

## Modes

- **`block`** (default) — when a rule interrupts the transaction, Jul.IA returns
  `block_status` (403 unless a rule sets its own status) and the request never
  reaches the upstream. Compiles to `SecRuleEngine On`.
- **`detect`** — rules still evaluate and matches are still counted and logged,
  but the request is allowed through. Compiles to `SecRuleEngine DetectionOnly`.
  Use it to measure the false-positive rate against real traffic before
  switching a location to `block`.

The mode line is always written **last** in the assembled program, so it wins
over any `SecRuleEngine` directive an included rule file might set.

## The OWASP Core Rule Set

With `crs_enabled = true` the CRS ships **inside the binary** (via
`coraza-coreruleset`) — there is nothing to download or mount. Jul.IA includes,
in order, the recommended Coraza base config, the CRS setup, an optional
paranoia override, and then the rules:

```
Include @coraza.conf-recommended
Include @crs-setup.conf.example
SecAction "id:900000,phase:1,nolog,pass,t:none,setvar:tx.blocking_paranoia_level=N,setvar:tx.detection_paranoia_level=N"
Include @owasp_crs/*.conf
```

`paranoia` (1–4) sets both the blocking and detection paranoia levels. Level 1
is the default and the most production-safe; raising it catches more subtle
attacks at the cost of more false positives, which is why the recommended path
is to raise paranoia in `detect` mode first.

### `block_status` and the CRS

Custom `block_status` (for example `block_status = 451`) is injected via a
leading `SecDefaultAction` so that rules without an explicit `status:` action
inherit it.  When the CRS is loaded (`crs_enabled = true`) the embedded
`@crs-setup.conf.example` also defines `SecDefaultAction` for each phase.
Because Coraza does **not** allow the same phase to be declared twice, Jul.IA
does **not** emit its own `SecDefaultAction` when `crs_enabled = true`.  As a
result CRS-triggered anomaly blocks use the status configured by the CRS setup
(403 by default) rather than `block_status`.

If you need a custom block status with the CRS, either:
1. Apply a post-files override such as `SecDefaultAction` in `inline_rules`, or
2. Set up a per-rule `status:` action inside a custom `directives_files` entry.

## Custom rules

Bring your own SecLang rules with `directives_files` (paths on disk) and/or
`inline_rules` (a snippet in the config). Either can be used **with or without**
the CRS:

```toml
[waf]
enabled = true
mode = "block"
directives_files = ["/etc/jul/waf/site-rules.conf"]
inline_rules = """
SecRule REQUEST_HEADERS:User-Agent "@contains sqlmap" \
  "id:100001,phase:1,deny,status:403,log,msg:'blocked scanner'"
"""
```

> **Tip:** add the `log` action to inline rules you want surfaced in
> `jul_waf_events_total` and the logs — only rules that log fire the event hook
> (CRS rules log by default).

## Per-location policy

A `[servers.locations.waf]` block overrides the global policy for that location;
locations without one inherit the global `[waf]` (when it is enabled). This lets
you run the CRS site-wide but, say, switch an upload endpoint to a higher body
limit or relax a noisy API to detect mode:

```toml
[waf]
enabled = true
crs_enabled = true
mode = "block"

[[servers]]
listen = "0.0.0.0:443"

  # Inherits the global block-mode CRS.
  [[servers.locations]]
  match = { type = "prefix", path = "/" }
  proxy_pass = "http://127.0.0.1:9000"

  # Larger bodies allowed and only observed, not blocked.
  [[servers.locations]]
  match = { type = "prefix", path = "/api/upload" }
  proxy_pass = "http://127.0.0.1:9001"

    [servers.locations.waf]
    enabled = true
    crs_enabled = true
    mode = "detect"
    request_body_limit = "8mb"
```

A location override replaces the global policy for that location wholesale (it is
not merged field-by-field), so repeat the rule sources you want it to keep.

### In the console

The Security panel discloses per-location overrides explicitly: it lists each
location that defines its own `[servers.locations.waf]` (with its mode and CRS
state). The WAF edit button is labelled **Edit global** because the guided
*global* editor seeds from and writes only the global `[waf]`.

Each listed override has its own **Edit** action that opens a guided per-location
editor. It controls the three knobs the panel discloses — enabled, mode
(block/detect) and the embedded CRS — and applies the change through structured
patch operations (`location_waf_set` / `location_waf_clear`) rather than splicing
nested TOML, so the edit lands on exactly one location and is reviewed as a diff
before it is applied. Advanced fields an override may carry (`block_status`,
`paranoia`, `directives_files`, `inline_rules`, body inspection) are **preserved**
— the editor never clears rules it does not display. **Clear override** removes
the per-location block so the location inherits the global policy again.

A route that still inherits the global policy can gain an override from the
**route detail** drawer (Routes → a route): its quick edits offer **Add WAF
override** (seeded detect-first), and a route that already overrides offers **Edit
WAF override**. Use the raw TOML editor for the advanced fields above. See
[console.md](console.md#web-application-firewall-waf).

## Rule ordering

The SecLang program is assembled deterministically:

1. the embedded CRS (recommended base → setup → optional paranoia → rules), when
   `crs_enabled`;
2. each `directives_files` entry, in order;
3. the `inline_rules` snippet;
4. the enforcement-mode line (`SecRuleEngine On`/`DetectionOnly`), last.

Because your files and inline rules come **after** the CRS, they can tune or
disable specific CRS rules (e.g. `SecRuleRemoveById`), and because the mode line
comes last, `mode` always wins.

## Request and response bodies

- **Request bodies** are buffered up to `request_body_limit` (128 KiB by
  default) so body-based rules can inspect them. Size your limit to the largest
  payload you need inspected, balancing memory use.
- **Response bodies** are inspected only when `response_body_check = true`. This
  enables CRS phase-4 outbound rules (e.g. data-leakage detection) but buffers
  responses and adds latency and memory — leave it off unless you need it.

These limits interact with the global request body limit (Y1-03) and with
compression: the WAF runs **before** response compression so it inspects the
uncompressed body.

## Metrics

| Metric | Type | Labels | Meaning |
| ------ | ---- | ------ | ------- |
| `jul_waf_events_total` | counter | `action`, `rule` | A rule that logs matched. `action` is the policy mode (`block` or `detect`); `rule` is the matched rule ID |

The counter reflects **logged rule matches** (standard ModSecurity behaviour) —
CRS rules log by default; raw inline rules need an explicit `,log` action to be
counted. In `block` mode a counted match generally corresponds to a blocked
request; in `detect` mode it is an observation only.

## Middleware ordering

When several per-location features are active, the WAF runs **after**
authentication and rate limiting and **before** the upstream/global compression.
The request flow, outer → inner, is:

```
plugins → client-cert → auth → rate-limit → WAF → action (proxy/static/…)
```

Running after auth means unauthenticated traffic is rejected before it reaches
the (more expensive) rule engine; running before compression means the WAF sees
the original, uncompressed request and response bodies.

## Operational notes

- **Reload-safe.** The WAF policy is compiled on startup and on every reload. A
  rule that fails to compile fails the reload with an error, so a bad rule never
  silently disables protection — the previous good configuration keeps serving.
- **Embedded CRS, no external assets.** With `crs_enabled` the rule set is
  compiled into the binary; there is nothing to ship or mount alongside it. The
  CRS version is whatever the build pinned (see `go.mod`).
- **Detect first.** Roll a new ruleset (or a paranoia bump) out in `detect` mode,
  watch `jul_waf_events_total` and the `waf rule matched` logs for false
  positives, tune with `SecRuleRemoveById`/allow-list rules, then switch to
  `block`.
- **Console.** The Console **Status** and **Security** panels report *Web
  application firewall (WAF)* with the active mode and how many locations it
  covers, so you can confirm enforcement at a glance.

## Behaviour matrix

The matrix below enumerates every supported rule source and mode combination
so operators can reason about what is available and what would change when
switching modes or rule sources.

| Capability | `block` mode | `detect` mode | Notes |
| --- | :-: | :-: | --- |
| **Rule sources** ||||
| Embedded OWASP CRS (`crs_enabled`) | ✅ | ✅ | Paranoia 1–4 configurable |
| Custom directive files (`directives_files`) | ✅ | ✅ | Include order respected |
| Inline rules (`inline_rules`) | ✅ | ✅ | Useful for allow-lists / tuning |
| Empty / no rules | ✅ | ✅ | Firewall compiles but never matches (awaiting rules) |
| **Match actions** ||||
| Block on rule match | ✅ | ☐ | `detect` records but allows |
| Record in metrics + logs | ✅ | ✅ | Both modes fire `jul_waf_events_total` and log hooks |
| Rule-set specific status codes | ✅ | ☐ | Rule-level `status:` or CRS default; detect mode returns 200 |
| **Scope control** ||||
| Global default (`[waf]`) | ✅ | ✅ | Applies to all locations without override |
| Per-location override (`[servers.locations.waf]`) | ✅ | ✅ | Wholesale replacement; not merged |
| Console global editor | ✅ | ✅ | Seeds from `[waf]`, writes global |
| Console per-location editor | ✅ | ✅ | Three knobs (enabled, mode, CRS) + Clear override |
| **Request / response handling** ||||
| Request line + headers (phases 1–2) | ✅ | ✅ | Always inspected |
| Request body (phase 2 cont.) | ✅ | ✅ | Up to `request_body_limit` |
| Response body (phase 4) | ✅ | ✅ | Opt-in via `response_body_check` |
| Uncompressed body inspection | ✅ | ✅ | WAF runs before compression middleware |
| **Integration** ||||
| Prometheus `jul_waf_events_total` | ✅ | ✅ | Labeled by `action` and `rule` |
| Structured log `waf rule matched` | ✅ | ✅ | Both modes |
| Flapping / health history | ☐ | ☐ | Not yet integrated (future) |
| Access-log / audit sink integration | ☐ | ☐ | Future — events are metric+log only today |
| **Build-time** ||||
| Opt-in via `waf` build tag | ✅ | ✅ | Lean builds reject WAF config |
| Reload-safe compilation | ✅ | ✅ | Bad rule fails reload; old config keeps serving |
| Stub (no-op lean build) | ✅ | ✅ | `Check` rejects enabled config; `Middleware` no-op |

## Threat note

The WAF is a **defense-in-depth** layer, not a single point of protection.
Operators should understand the following risks and limitations:

### False positives

The OWASP CRS is tuned for broad coverage and can flag legitimate traffic,
especially at higher paranoia levels (`paranoia ≥ 2`). Recommended mitigation:

1. **Deploy in `detect` mode first** for every new rule source or paranoia bump.
2. **Monitor `jul_waf_events_total`** and the `waf rule matched` logs for 24–48
   hours of representative traffic.
3. **Tune with `SecRuleRemoveById`** or allow-list `inline_rules` before
   switching to `block`. The Jul.IA rule ordering (user files/snippets come
   after the CRS) makes this straightforward.

### Bypass scenarios

- **Request-size evasion:** A body larger than `request_body_limit` is not
  inspected for body-based rules. Attackers might pad payloads to exceed the
  limit. Size the limit to the largest payload you need inspected, or front the
  server with a CDN that enforces its own size cap.
- **Response-body evasion:** When `response_body_check = false`, outbound data
  leakage evades phase-4 inspection. Enable only when your threat model requires
  it (adds latency/memory).
- **Coraza engine bugs:** The rule engine is Coraza, not Jul.IA code. Follow
  Coraza security advisories and rebuild when the upstream publishes fixes. The
  CRS version is pinned at build time (`go.mod`); there is no live update
  channel.
- **TLS termination order:** The WAF sees decrypted traffic, so a misconfigured
  TLS termination before the proxy could expose payloads to intermediate
  inspection. This is a deployment-architecture concern, not a Jul.IA code bug.

### Replay and detection gaps

- **No dedicated audit sink yet.** Matched rules are metric + log only. If you
  need tamper-evident WAF audit trails, forward the structured logs to a SIEM or
  wait for the Y1-10 access-log / audit integration (future roadmap item).
- **Detect mode does not block.** A request in `detect` mode reaches the upstream
  even when rules match. Use this mode only for observation, never as a
  production control.

### Configuration trust

Config is trusted per [SECURITY.md](../SECURITY.md). A compromised or mistaken
operator with config-write access could:

- Disable the WAF (`enabled = false`).
- Switch to `detect` mode without intending to.
- Remove rules with `SecRuleRemoveById` in `inline_rules`.

These are operator-error / insider-threat vectors, not request-driven exploits.
Use the Console’s diff-and-apply flow and the config-history rollback feature
(GA) to catch accidental changes.

| # | GA criterion | Status |
| --- | --- | --- |
| 1 | Behaviour matrix published | ✅ [Matrix above](#behaviour-matrix) |
| 2 | Published benchmark numbers | ✅ `BenchmarkWAF_*` (internal/waf/bench_test.go) |
| 3 | Documented known-limitations | ✅ [Limits](#limits) |
| 4 | Stable config/API contract (semver-guarded) | ✅ Covered by v1 freeze ([compatibility.md](compatibility.md)) |
| 5 | Long-running soak test passed | ✅ soaked 1h windows 2026-07-04 (1.67M req, 0% err) — [evidence](soak-evidence.md#2026-07-04--waf-soak-local-windows-1-hour-50-workers) |
| 6 | Runnable example + docs | ✅ [testdata/waf.toml](../testdata/waf.toml) + this doc |
| 7 | Security / threat note | ✅ [Threat note](#threat-note) above |
| 8 | Fuzzing where parsing is involved | n/a — SecLang parsing is owned by Coraza (no custom parser) |
| 9 | Self-explanatory Console surface | ✅ Console **Status**/**Security** report *Web application firewall (WAF)* with mode + location count |

The WAF is now **GA**.
