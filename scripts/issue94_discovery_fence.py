#!/usr/bin/env python3
from pathlib import Path

def replace(path, old, new, count=1):
    p = Path(path)
    s = p.read_text()
    actual = s.count(old)
    if actual != count:
        raise SystemExit(f"{path}: expected {count}, found {actual}: {old[:100]!r}")
    p.write_text(s.replace(old, new, count))

replace("internal/upstream/pool.go",
'''func (p *Pool) discoveryGenerationCurrent(epoch uint64) bool {
\treturn p != nil && p.discoveryEpoch.Load() == epoch
}
''',
'''func (p *Pool) discoveryGenerationCurrent(epoch uint64) bool {
\treturn p != nil && p.discoveryEpoch.Load() == epoch
}

// applyDiscoveryTargets linearizes a successful discovery result against a
// policy-generation Publish. Holding discoveryMu across the epoch check and
// UpdateTargets removes the check/update race: either the A result commits
// before StopDiscovery acquires the lock, or StopDiscovery advances the epoch
// first and A is discarded. It can never pass the check under A and write after
// B has fenced the worker.
func (p *Pool) applyDiscoveryTargets(epoch uint64, targets []Target) bool {
\tif p == nil {
\t\treturn false
\t}
\tp.discoveryMu.Lock()
\tdefer p.discoveryMu.Unlock()
\tif p.discoveryEpoch.Load() != epoch {
\t\treturn false
\t}
\tp.UpdateTargets(targets)
\treturn true
}
''')

replace("internal/upstream/discovery.go",
'''\tp.UpdateTargets(targets)
\tif hooks.OnBackends != nil {
''',
'''\tif !p.applyDiscoveryTargets(epoch, targets) {
\t\treturn
\t}
\tif hooks.OnBackends != nil {
''')

print("linearized discovery result fencing applied")
