#!/usr/bin/env python3
from pathlib import Path
import json


def replace(path: str, old: str, new: str) -> None:
    p = Path(path)
    text = p.read_text()
    if old not in text:
        raise SystemExit(f"anchor not found in {path}: {old[:120]!r}")
    p.write_text(text.replace(old, new, 1))


# Presence-aware TOML default: seed the decode target, then let an explicit zero
# overwrite it. Never reinterpret zero after decoding.
replace(
    "internal/config/parser.go",
    """\tvar cfg Config\n\tdecoder := toml.NewDecoder(bytes.NewReader(normalized)).DisallowUnknownFields()\n""",
    """\tvar cfg Config\n\t// Seed syntax-level defaults before decoding so TOML presence remains\n\t// meaningful: omission keeps full sampling, while an explicit\n\t// sample_ratio = 0 overwrites this value and remains zero.\n\tcfg.Observability.Tracing.SampleRatio = 1.0\n\tdecoder := toml.NewDecoder(bytes.NewReader(normalized)).DisallowUnknownFields()\n""",
)
replace(
    "internal/config/parser.go",
    """\t\t// A zero ratio means \"unset\" here and defaults to full sampling; users\n\t\t// who want less set an explicit fraction in (0,1].\n\t\tif t.SampleRatio == 0 {\n\t\t\tt.SampleRatio = 1.0\n\t\t}\n""",
    """\t\t// SampleRatio is syntax-defaulted before TOML decode. Do not default it\n\t\t// here: zero is a real value that disables sampling for new root spans.\n""",
)

replace(
    "internal/config/schema.go",
    """\t// SampleRatio is the head-based sampling probability for root spans, in the\n\t// range [0,1]. It defaults to 1.0 (sample everything) when enabled; set a\n\t// fraction such as 0.1 to sample less.\n\tSampleRatio float64 `toml:\"sample_ratio\"`\n""",
    """\t// SampleRatio is the head-based sampling probability for root spans, in the\n\t// range [0,1]. An omitted TOML value defaults to 1.0 (sample everything); an\n\t// explicit 0 disables sampling of new root traces.\n\tSampleRatio float64 `toml:\"sample_ratio\"`\n""",
)

# Canonical finite/range validation shared by config validation and tracing
# preflight, so the no-fail Publish seam has exactly one numeric invariant.
replace(
    "internal/config/validate.go",
    '"fmt"\n\t"net"',
    '"fmt"\n\t"math"\n\t"net"',
)
replace(
    "internal/config/validate.go",
    """// validateTracing checks the [observability.tracing] block. It validates the\n// configuration only; whether the `otel` build tag is compiled in is reported\n// when the tracer is constructed at startup, since that depends on build tags.\nfunc validateTracing(c TracingConfig) []error {\n\tif !c.Enabled {\n\t\treturn nil\n\t}\n\tvar errs []error\n""",
    """// ValidateTracingSampleRatio validates the numeric invariant required by the\n// no-fail tracing Publish seam. TOML accepts NaN and infinities, so ordinary\n// range comparisons alone are insufficient.\nfunc ValidateTracingSampleRatio(ratio float64) error {\n\tif math.IsNaN(ratio) || math.IsInf(ratio, 0) || ratio < 0 || ratio > 1 {\n\t\treturn fmt.Errorf(\"[observability.tracing] sample_ratio must be finite and in [0, 1], got %g\", ratio)\n\t}\n\treturn nil\n}\n\n// validateTracing checks the [observability.tracing] block. It validates the\n// configuration only; whether the `otel` build tag is compiled in is reported\n// when the tracer is constructed at startup, since that depends on build tags.\nfunc validateTracing(c TracingConfig) []error {\n\tvar errs []error\n\tif err := ValidateTracingSampleRatio(c.SampleRatio); err != nil {\n\t\terrs = append(errs, err)\n\t}\n\tif !c.Enabled {\n\t\treturn errs\n\t}\n""",
)
replace(
    "internal/config/validate.go",
    """\tif c.SampleRatio < 0 || c.SampleRatio > 1 {\n\t\terrs = append(errs, fmt.Errorf(\"[observability.tracing] sample_ratio %g out of range (want 0..1)\", c.SampleRatio))\n\t}\n\treturn errs\n}\n""",
    """\treturn errs\n}\n""",
)

replace(
    "internal/observability/preflight.go",
    """func ValidateTracerConfig(cfg config.TracingConfig) error {\n\tif !cfg.Enabled {\n\t\treturn nil\n\t}\n""",
    """func ValidateTracerConfig(cfg config.TracingConfig) error {\n\tif err := config.ValidateTracingSampleRatio(cfg.SampleRatio); err != nil {\n\t\treturn err\n\t}\n\tif !cfg.Enabled {\n\t\treturn nil\n\t}\n""",
)
replace(
    "internal/observability/preflight.go",
    """\tif cfg.SampleRatio < 0 || cfg.SampleRatio > 1 {\n\t\treturn fmt.Errorf(\"[observability.tracing] sample_ratio must be in [0, 1], got %g\", cfg.SampleRatio)\n\t}\n\treturn nil\n}\n""",
    """\treturn nil\n}\n""",
)

# Console must preserve an explicit zero and stop warning that it aliases the
# default. Full sampling may still be omitted because it is the TOML default.
replace(
    "internal/admin/ui/src/lib/tracingToml.ts",
    """// formatRatio renders a sampling probability as a valid TOML float. Values are\n// only emitted in [0,1); the parser treats a zero as \"unset\" and defaults to\n// full sampling, so a fraction such as 0.1 is the meaningful case.\n""",
    """// formatRatio renders a sampling probability as a valid TOML float. Values in\n// [0,1) are emitted explicitly; 0 disables sampling of new root traces, while\n// 1 may be omitted because it is the server default.\n""",
)
replace(
    "internal/admin/ui/src/lib/tracingToml.ts",
    """\n  if (d.sampleRatio <= 0) {\n    w.push(\n      \"A sample ratio of 0 is treated as unset and falls back to full sampling; set a fraction such as 0.1 to sample less.\",\n    );\n  }\n""",
    "\n",
)
replace(
    "internal/admin/ui/src/lib/tracingToml.ts",
    """  // Only emit a fraction; full sampling (1.0) is the server default, so omit it\n  // to keep the config minimal and round-trip a default-sampled block.\n  if (d.sampleRatio > 0 && d.sampleRatio < 1) {\n""",
    """  // Emit zero and fractions explicitly. Full sampling (1.0) is the server\n  // default, so omit only that value to keep the config minimal.\n  if (Number.isFinite(d.sampleRatio) && d.sampleRatio >= 0 && d.sampleRatio < 1) {\n""",
)
replace(
    "internal/admin/ui/src/test/tracing-toml.test.ts",
    'it("emits a fractional sample ratio but omits 0 and 1", () => {',
    'it("emits zero and fractional sample ratios but omits default 1", () => {',
)
replace(
    "internal/admin/ui/src/test/tracing-toml.test.ts",
    """    expect(generateTracingToml(draft({ enabled: true, endpoint: \"h:1\", sampleRatio: 0 }))).not.toContain(\n      \"sample_ratio\",\n    );\n""",
    """    expect(generateTracingToml(draft({ enabled: true, endpoint: \"h:1\", sampleRatio: 0 }))).toContain(\n      \"sample_ratio = 0.0\",\n    );\n""",
)
replace(
    "internal/admin/ui/src/features/traffic-controls/TracingEditor.tsx",
    'hint="Head-based sampling probability 0–1. Blank or 1 samples everything; 0.1 samples 10%."',
    'hint="Head-based sampling probability 0–1. Blank or 1 samples everything; 0 samples no new root traces."',
)

replace(
    "internal/app/runtime.go",
    """\t// Tracing is initialised once at startup (like ACME): the OTLP pipeline and\n\t// global TracerProvider are fixed for the process, so changing\n\t// [observability.tracing] takes effect only after a restart. It is a no-op\n\t// unless enabled and built with the \"otel\" tag; an enabled block in a binary\n""",
    """\t// Tracing is initialised once at startup (like ACME): the OTLP pipeline and\n\t// global TracerProvider identity are fixed for the process. The pipeline\n\t// identity fields remain restart-bound; sample_ratio is the sole runtime-\n\t// tunable tracing field. It is a no-op unless enabled and built with the\n\t// \"otel\" tag; an enabled block in a binary\n""",
)

# The value-contract JSON is authoritative input for generated config metadata.
p = Path("docs/config-value-contract.json")
doc = json.loads(p.read_text())
matched = 0
for field in doc.get("fields", []):
    if field.get("path") == "observability.tracing.sample_ratio":
        field["zero_semantics"] = "omitted defaults to 1.0; explicit zero disables sampling of new root traces"
        matched += 1
if matched != 1:
    raise SystemExit(f"expected one sample_ratio value-contract entry, got {matched}")
p.write_text(json.dumps(doc, indent=2) + "\n")

# Parser/defaulting and canonical TOML validation coverage.
Path("internal/config/issue99_sample_ratio_test.go").write_text(r'''// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package config

import (
    "math"
    "strings"
    "testing"
)

func TestTracingSampleRatioTOMLPresenceSemantics(t *testing.T) {
    cases := []struct {
        name  string
        field string
        want  float64
    }{
        {"omitted defaults to one", "", 1},
        {"explicit zero stays zero", "sample_ratio = 0\n", 0},
        {"fraction stays fractional", "sample_ratio = 0.25\n", 0.25},
        {"explicit one stays one", "sample_ratio = 1\n", 1},
    }
    for _, tc := range cases {
        t.Run(tc.name, func(t *testing.T) {
            raw := "[observability.tracing]\nenabled = true\nendpoint = \"collector:4317\"\n" + tc.field
            cfg, err := Parse([]byte(raw))
            if err != nil {
                t.Fatalf("Parse: %v", err)
            }
            if got := cfg.Observability.Tracing.SampleRatio; got != tc.want {
                t.Fatalf("sample_ratio=%v, want %v", got, tc.want)
            }
        })
    }
}

func TestTracingSampleRatioTOMLRejectsNonFiniteValues(t *testing.T) {
    for _, token := range []string{"nan", "inf", "-inf"} {
        t.Run(token, func(t *testing.T) {
            raw := "[observability.tracing]\nenabled = true\nendpoint = \"collector:4317\"\nsample_ratio = " + token + "\n"
            cfg, err := Parse([]byte(raw))
            if err != nil {
                t.Fatalf("TOML parser should accept %s so semantic validation can reject it: %v", token, err)
            }
            errs := validateTracing(cfg.Observability.Tracing)
            if len(errs) == 0 {
                t.Fatalf("validateTracing accepted sample_ratio=%s", token)
            }
            if !strings.Contains(errs[0].Error(), "finite") {
                t.Fatalf("error=%q, want finite-value diagnostic", errs[0])
            }
        })
    }
}

func TestValidateTracingSampleRatioFiniteBoundaries(t *testing.T) {
    for _, ratio := range []float64{0, 0.25, 1} {
        if err := ValidateTracingSampleRatio(ratio); err != nil {
            t.Fatalf("ratio %v rejected: %v", ratio, err)
        }
    }
    for _, ratio := range []float64{math.NaN(), math.Inf(1), math.Inf(-1), -0.01, 1.01} {
        if err := ValidateTracingSampleRatio(ratio); err == nil {
            t.Fatalf("ratio %v accepted", ratio)
        }
    }
}
''')

Path("internal/observability/preflight_issue99_test.go").write_text(r'''// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package observability

import (
    "math"
    "strings"
    "testing"

    "jul/internal/config"
)

func TestValidateTracerConfigRejectsNonFiniteSampleRatio(t *testing.T) {
    for _, ratio := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
        err := ValidateTracerConfig(config.TracingConfig{SampleRatio: ratio})
        if err == nil {
            t.Fatalf("sample ratio %v accepted", ratio)
        }
        if !strings.Contains(err.Error(), "finite") {
            t.Fatalf("error=%q, want finite-value diagnostic", err)
        }
    }
}
''')

# Canonical TOML -> validation -> lifecycle -> actual ReloadPlan.Publish -> OTel
# sampling behavior. This intentionally uses the process sampler installed by
# NewTracer and a real server reload, rather than calling the sampler update seam
# directly.
Path("internal/server/issue99_tracing_reload_otel_test.go").write_text(r'''// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build otel

package server

import (
    "context"
    "fmt"
    "net/http"
    "net/http/httptest"
    "strings"
    "sync"
    "sync/atomic"
    "testing"
    "time"

    "go.opentelemetry.io/otel"

    "jul/internal/config"
    "jul/internal/lifecycle"
    "jul/internal/observability"
    "jul/internal/redact"
    tracingseam "jul/internal/tracing"
)

type issue99TOMLSource struct {
    mu  sync.Mutex
    raw []byte
}

func (s *issue99TOMLSource) set(raw []byte) {
    s.mu.Lock()
    defer s.mu.Unlock()
    s.raw = append([]byte(nil), raw...)
}

func (s *issue99TOMLSource) Load() (*config.Config, error) {
    s.mu.Lock()
    raw := append([]byte(nil), s.raw...)
    s.mu.Unlock()
    return config.Parse(raw)
}

func (s *issue99TOMLSource) ReadRaw() ([]byte, error) {
    s.mu.Lock()
    defer s.mu.Unlock()
    return append([]byte(nil), s.raw...), nil
}

func (s *issue99TOMLSource) Name() string { return "issue99-toml" }

func issue99TracingTOML(addr, endpoint, ratio string) []byte {
    return []byte(fmt.Sprintf(`[global]
shutdown_timeout = "2s"

[observability.tracing]
enabled = true
exporter = "otlp-http"
endpoint = %q
sample_ratio = %s
service_name = "jul-issue99-reload"
insecure = true

[[servers]]
listen = %q

  [[servers.locations]]
  match = { type = "prefix", path = "/" }
  return = 200
`, endpoint, ratio, addr))
}

func TestIssue99TOMLReloadPublishesZeroAndRestoresOne(t *testing.T) {
    collector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        if r.URL.Path != "/v1/traces" {
            t.Errorf("collector path=%q, want /v1/traces", r.URL.Path)
        }
        w.Header().Set("Content-Type", "application/x-protobuf")
        w.WriteHeader(http.StatusOK)
    }))
    defer collector.Close()

    addr := freePort(t)
    endpoint := strings.TrimPrefix(collector.URL, "http://")
    initialRaw := issue99TracingTOML(addr, endpoint, "1.0")
    initial, err := config.Parse(initialRaw)
    if err != nil {
        t.Fatalf("parse initial TOML: %v", err)
    }
    if err := config.Validate(initial); err != nil {
        t.Fatalf("validate initial TOML: %v", err)
    }
    if err := observability.ValidateTracerConfig(initial.Observability.Tracing); err != nil {
        t.Fatalf("preflight initial tracing: %v", err)
    }

    oldProvider := otel.GetTracerProvider()
    oldPropagator := otel.GetTextMapPropagator()
    oldSeam := tracingseam.Active()
    tracerRuntime, err := observability.NewTracer(initial.Observability.Tracing)
    if err != nil {
        t.Fatalf("NewTracer: %v", err)
    }
    defer func() {
        ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
        defer cancel()
        if err := tracerRuntime.Shutdown(ctx); err != nil {
            t.Errorf("tracer shutdown: %v", err)
        }
        otel.SetTracerProvider(oldProvider)
        otel.SetTextMapPropagator(oldPropagator)
        tracingseam.Set(oldSeam)
        observability.UpdateTracingSampleRatio(1)
    }()

    otelTracer := otel.Tracer("jul/issue99-reload-test")
    parentCtx, existingParent := otelTracer.Start(context.Background(), "existing-sampled-parent")
    if !existingParent.SpanContext().IsSampled() {
        t.Fatal("initial ratio=1 root is not sampled")
    }
    defer existingParent.End()

    src := &issue99TOMLSource{}
    src.set(initialRaw)
    tag := &atomic.Pointer[string]{}
    one := "ratio-one"
    tag.Store(&one)
    validate := func(_ context.Context, cfg *config.Config) error {
        if err := config.Validate(cfg); err != nil {
            return err
        }
        return observability.ValidateTracerConfig(cfg.Observability.Tracing)
    }
    srv := New(
        initial,
        initial,
        lifecycle.ComputeFingerprint(initial),
        quietLogger(),
        bodyHandlerFactory(tag),
        src,
        validate,
    )

    ctx, cancel := context.WithCancel(context.Background())
    reload := make(chan ReloadRequest, 1)
    done := make(chan error, 1)
    go func() { done <- srv.Run(ctx, reload, redact.EmptyState()) }()
    defer func() {
        cancel()
        if err := <-done; err != nil {
            t.Errorf("Run returned error: %v", err)
        }
    }()
    waitForServe(t, "http://"+addr+"/", one)

    // 1 -> 0 through actual TOML parsing/defaulting and the full reload plan.
    zeroRaw := issue99TracingTOML(addr, endpoint, "0.0")
    parsedZero, err := config.Parse(zeroRaw)
    if err != nil {
        t.Fatalf("parse zero TOML: %v", err)
    }
    if got := parsedZero.Observability.Tracing.SampleRatio; got != 0 {
        t.Fatalf("explicit zero defaulted to %v", got)
    }
    if err := config.Validate(parsedZero); err != nil {
        t.Fatalf("validate zero TOML: %v", err)
    }
    if reason, restart := lifecycle.RestartRequired(lifecycle.ComputeFingerprint(initial), lifecycle.ComputeFingerprint(parsedZero)); restart {
        t.Fatalf("ratio-only change classified restart-required: %s", reason)
    }
    zero := "ratio-zero"
    tag.Store(&zero)
    src.set(zeroRaw)
    reload <- ReloadRequest{Source: ReloadSourceSIGHUP}
    waitForServe(t, "http://"+addr+"/", zero)

    _, rootAtZero := otelTracer.Start(context.Background(), "new-root-at-zero")
    if rootAtZero.SpanContext().IsSampled() {
        t.Fatal("new root after TOML ratio=0 Publish is sampled")
    }
    rootAtZero.End()

    _, childAfterZero := otelTracer.Start(parentCtx, "child-after-zero")
    if !childAfterZero.SpanContext().IsSampled() {
        t.Fatal("sampled existing parent lost authority after ratio=0 Publish")
    }
    childAfterZero.End()

    // 0 -> 1 through the same source/reload path.
    oneRaw := issue99TracingTOML(addr, endpoint, "1.0")
    restored := "ratio-restored"
    tag.Store(&restored)
    src.set(oneRaw)
    reload <- ReloadRequest{Source: ReloadSourceSIGHUP}
    waitForServe(t, "http://"+addr+"/", restored)

    _, rootAtOne := otelTracer.Start(context.Background(), "new-root-at-one")
    if !rootAtOne.SpanContext().IsSampled() {
        t.Fatal("new root after TOML ratio=1 Publish is not sampled")
    }
    rootAtOne.End()
}
''')
