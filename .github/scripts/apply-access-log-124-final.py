#!/usr/bin/env python3
"""Apply the bounded corrections found while validating issue #124.

The checksum-pinned implementation patch is applied first by the workflow. This
script then updates only the regressions exposed by exact-toolchain validation.
Every replacement is anchored and fails closed if the expected source is absent.
"""

from pathlib import Path


def replace_once(path: str, old: str, new: str, label: str) -> None:
    file_path = Path(path)
    text = file_path.read_text(encoding="utf-8")
    if old not in text:
        raise SystemExit(f"{label}: expected anchor not found in {path}")
    file_path.write_text(text.replace(old, new, 1), encoding="utf-8")


# Programmatic Config values with nil sinks must round-trip as the documented
# omitted/default stdout sink, while explicit [] remains distinguishable and
# invalid when enabled=true.
replace_once(
    "internal/config/config_test.go",
    'import (\n\t"os"\n',
    'import (\n\t"bytes"\n\t"os"\n',
    "config test bytes import",
)
replace_once(
    "internal/config/config_test.go",
    'func TestAccessLogExplicitDisableRoundTrip(t *testing.T) {\n',
    '''func TestAccessLogProgrammaticMarshalUsesDefaultSink(t *testing.T) {
\tcfg := &Config{Servers: []ServerConfig{{Listen: "127.0.0.1:80"}}}
\traw, err := Marshal(cfg)
\tif err != nil {
\t\tt.Fatalf("marshal: %v", err)
\t}
\tif bytes.Contains(raw, []byte("sinks = []")) {
\t\tt.Fatalf("marshal encoded omitted sinks as an explicit empty list:\\n%s", raw)
\t}
\tround, err := Parse(raw)
\tif err != nil {
\t\tt.Fatalf("round-trip parse: %v", err)
\t}
\tif err := Validate(round); err != nil {
\t\tt.Fatalf("round-trip config rejected: %v\\n%s", err, raw)
\t}
\tif got := round.Observability.AccessLog.Sinks; len(got) != 1 || got[0] != "stdout" {
\t\tt.Fatalf("round-trip default sinks = %v, want [stdout]", got)
\t}
}

func TestAccessLogExplicitDisableRoundTrip(t *testing.T) {
''',
    "programmatic access-log round-trip test",
)
replace_once(
    "internal/config/parser.go",
    '''func Marshal(c *Config) ([]byte, error) {
\tdata, err := toml.Marshal(c)
\tif err != nil {
\t\treturn nil, fmt.Errorf("encode config: %w", err)
\t}
\treturn data, nil
}
''',
    '''func Marshal(c *Config) ([]byte, error) {
\t// Direct struct callers can leave access_log.sinks nil to express the same
\t// omitted/default state as TOML without a sinks key. go-toml encodes a nil
\t// slice as `sinks = []`, which would turn that omission into an explicit
\t// empty list and fail validation when the canonical output is parsed again.
\t// Canonicalize only the shallow copy so Marshal and Clone preserve the
\t// documented default stdout sink without mutating the caller's config.
\tcanonical := *c
\tcanonical.Observability = c.Observability
\tif canonical.Observability.AccessLog.Sinks == nil {
\t\tcanonical.Observability.AccessLog.Sinks = []string{"stdout"}
\t}

\tdata, err := toml.Marshal(&canonical)
\tif err != nil {
\t\treturn nil, fmt.Errorf("encode config: %w", err)
\t}
\treturn data, nil
}
''',
    "access-log marshal canonicalization",
)

# The lifecycle regression must exercise the active access-log contract, not
# deprecated global.access_log, which is intentionally ignored.
replace_once(
    "internal/app/pending_restart_test.go",
    '''\tstartupRaw := &config.Config{
\t\tGlobal: config.GlobalConfig{AccessLog: "/tmp/startup-access.log"},
\t\tServers: []config.ServerConfig{{
\t\t\tListen:    addr,
\t\t\tLocations: []config.LocationConfig{{Match: config.MatchConfig{Type: "prefix", Path: "/"}, Return: 200}},
\t\t}},
\t}
''',
    '''\tstartupRaw := &config.Config{
\t\tObservability: config.ObservabilityConfig{AccessLog: config.AccessLogConfig{
\t\t\tEnabled:     config.Bool(true),
\t\t\tSinks:       []string{"stdout"},
\t\t\tFormat:      "text",
\t\t\tRotateMaxMB: 100,
\t\t\tRotateKeep:  7,
\t\t}},
\t\tServers: []config.ServerConfig{{
\t\t\tListen:    addr,
\t\t\tLocations: []config.LocationConfig{{Match: config.MatchConfig{Type: "prefix", Path: "/"}, Return: 200}},
\t\t}},
\t}
''',
    "pending-restart active access-log fixture",
)
replace_once(
    "internal/app/pending_restart_test.go",
    '\t// Change a restart-required global field and a hot-reloadable location field.\n',
    '\t// Change an active restart-required access-log field and a hot-reloadable\n\t// location field. Deprecated global.access_log is deliberately ignored.\n',
    "pending-restart fixture comment",
)
replace_once(
    "internal/app/pending_restart_test.go",
    '\tnextRaw.Global.AccessLog = "/tmp/changed-access.log"\n',
    '\tnextRaw.Observability.AccessLog.Format = "json"\n',
    "pending-restart active field mutation",
)

# DOM events belong to Testing Library, and the fetch mock already satisfies
# the accepted receiver type without the double assertion rejected by lint.
replace_once(
    "internal/admin/ui/src/test/access-log-editor.test.tsx",
    'import { afterEach, beforeEach, describe, expect, fireEvent, it, vi } from "vitest";\nimport { render, screen, waitFor } from "@testing-library/react";',
    'import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";\nimport { fireEvent, render, screen, waitFor } from "@testing-library/react";',
    "access-log editor test imports",
)
replace_once(
    "internal/admin/ui/src/test/access-log-editor.test.tsx",
    '  ) as unknown as typeof fetch;\n',
    '  );\n',
    "access-log editor fetch assertion",
)

# The raw-TOML handoff validates and diffs before rendering Pending changes.
# The browser smoke fixture must model those two API contracts rather than let
# the catch-all 404 turn a working handoff into a false failure.
replace_once(
    "internal/admin/ui/e2e/smoke.spec.ts",
    '''  await page.route("/api/config", (route) => json(route, RAW_CONFIG));
  await page.route("/api/traffic-controls", (route) => json(route, TRAFFIC_CONTROLS));
''',
    '''  await page.route("/api/config", (route) => json(route, RAW_CONFIG));
  await page.route("/api/config/validate", (route) =>
    json(route, { ok: true, message: "Configuration is valid." }),
  );
  await page.route("/api/config/diff", (route) =>
    json(route, {
      summary: "1 modification",
      modifications: [
        {
          kind: "access_log",
          name: "observability.access_log",
          before: "enabled = true",
          after: "enabled = false",
        },
      ],
    }),
  );
  await page.route("/api/traffic-controls", (route) => json(route, TRAFFIC_CONTROLS));
''',
    "access-log browser validation and diff mocks",
)
