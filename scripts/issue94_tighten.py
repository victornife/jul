#!/usr/bin/env python3
from pathlib import Path

def replace(path, old, new, count=1):
    p = Path(path)
    s = p.read_text()
    actual = s.count(old)
    if actual != count:
        raise SystemExit(f"{path}: expected {count}, found {actual} for {old[:80]!r}")
    p.write_text(s.replace(old, new, count))

replace("internal/upstream/discovery.go",
'''func discoveryEnabled(d *config.DiscoveryConfig) bool {
	if d == nil {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(d.Type)) {
	case "", "static":
		return false
	default:
		return true
	}
}
''',
'''func discoveryEnabled(d *config.DiscoveryConfig) bool {
	if d == nil {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(d.Type)) {
	case "", "static":
		return false
	default:
		return true
	}
}

// discoveryUsesEgress reports whether the provider owns an HTTP client guarded
// by Boundary C. DNS and DNS-SRV use the system resolver and must not churn
// merely because the auxiliary HTTP egress policy changed.
func discoveryUsesEgress(d *config.DiscoveryConfig) bool {
	if d == nil {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(d.Type)) {
	case "consul", "kubernetes":
		return true
	default:
		return false
	}
}
''')

replace("internal/upstream/registry.go",
'''\tmeta := metaOf(up, scheme)
\tdisco := discoveryEnabled(up.Discovery)
''',
'''\tmeta := metaOf(up, scheme)
\tdisco := discoveryEnabled(up.Discovery)
\tegressSensitiveDiscovery := discoveryUsesEgress(up.Discovery)
''')
replace("internal/upstream/registry.go",
'''\tif e, ok := r.live[key]; ok && e.meta.equal(meta) && (!disco || e.egressGen == r.stagedEgressID) {''',
'''\tif e, ok := r.live[key]; ok && e.meta.equal(meta) && (!egressSensitiveDiscovery || e.egressGen == r.stagedEgressID) {''')
replace("internal/upstream/registry.go",
'''\t\tegressGen:   r.stagedEgressID,
''',
'''\t\tegressGen:   func() uint64 {
\t\t\tif egressSensitiveDiscovery {
\t\t\t\treturn r.stagedEgressID
\t\t\t}
\t\t\treturn 0
\t\t}(),
''')
