#!/usr/bin/env python3
from pathlib import Path


def replace(path: str, old: str, new: str) -> None:
    p = Path(path)
    text = p.read_text()
    if old not in text:
        raise SystemExit(f"anchor not found in {path}: {old[:120]!r}")
    p.write_text(text.replace(old, new, 1))

p = "internal/config/parser.go"
replace(
    p,
    """\tvar cfg Config\n\t// Seed syntax-level defaults before decoding so TOML presence remains\n\t// meaningful: omission keeps full sampling, while an explicit\n\t// sample_ratio = 0 overwrites this value and remains zero.\n\tcfg.Observability.Tracing.SampleRatio = 1.0\n\tdecoder := toml.NewDecoder(bytes.NewReader(normalized)).DisallowUnknownFields()\n""",
    """\tvar cfg Config\n\tdecoder := toml.NewDecoder(bytes.NewReader(normalized)).DisallowUnknownFields()\n""",
)
replace(
    p,
    """\tcfg.applyDefaults()\n\treturn &cfg, nil\n}\n""",
    """\tsampleRatioPresent, err := tracingSampleRatioPresent(normalized)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\tcfg.applyDefaultsWithPresence(sampleRatioPresent)\n\treturn &cfg, nil\n}\n\n// tracingSampleRatioPresent records syntax presence without changing the public\n// Config shape. This is the one place where omission must be distinguished from\n// an explicit numeric zero: omitted+enabled defaults to 1.0, while explicit\n// zero is a real request to stop sampling new root traces.\nfunc tracingSampleRatioPresent(data []byte) (bool, error) {\n\tvar presence struct {\n\t\tObservability struct {\n\t\t\tTracing struct {\n\t\t\t\tSampleRatio *float64 `toml:\"sample_ratio\"`\n\t\t\t} `toml:\"tracing\"`\n\t\t} `toml:\"observability\"`\n\t}\n\tif err := toml.Unmarshal(data, &presence); err != nil {\n\t\treturn false, fmt.Errorf(\"decode tracing field presence: %w\", err)\n\t}\n\treturn presence.Observability.Tracing.SampleRatio != nil, nil\n}\n""",
)
replace(
    p,
    """// applyDefaults fills in conservative defaults for unset fields.\nfunc (c *Config) applyDefaults() {\n""",
    """// applyDefaults fills in conservative defaults for programmatically-built\n// configs where TOML field presence is unavailable.\nfunc (c *Config) applyDefaults() {\n\tc.applyDefaultsWithPresence(false)\n}\n\n// applyDefaultsWithPresence applies defaults while preserving the one numeric\n// field where explicit zero is semantically distinct from omission.\nfunc (c *Config) applyDefaultsWithPresence(tracingSampleRatioPresent bool) {\n""",
)
replace(
    p,
    """\t\t// SampleRatio is syntax-defaulted before TOML decode. Do not default it\n\t\t// here: zero is a real value that disables sampling for new root spans.\n""",
    """\t\tif t.SampleRatio == 0 && !tracingSampleRatioPresent {\n\t\t\tt.SampleRatio = 1.0\n\t\t}\n""",
)
