#!/usr/bin/env python3
from pathlib import Path

p = Path("internal/app/factory.go")
s = p.read_text()
old = "\tEgress        *egress.Manager\n\tPoolReg       *upstream.Registry"
new = """\tEgress        *egress.Manager
\t// EgressDial is the legacy injection seam used by focused factory tests.
\t// Production sets Egress and never reads this field; when Egress is nil the
\t// factory preserves the old caller-supplied dial behavior.
\tEgressDial    func(context.Context, string, string) (net.Conn, error)
\tPoolReg       *upstream.Registry"""
if old not in s:
    raise SystemExit("factory Egress field anchor not found")
s = s.replace(old, new, 1)
old = "\tvar authDial auth.DialFunc\n\tif egressGeneration != nil && egressGeneration.Enabled() {"
new = """\tvar authDial auth.DialFunc
\tif f.Egress == nil {
\t\tauthDial = f.EgressDial
\t}
\tif egressGeneration != nil && egressGeneration.Enabled() {"""
if old not in s:
    raise SystemExit("auth dial anchor not found")
p.write_text(s.replace(old, new, 1))

p = Path("internal/app/serve.go")
s = p.read_text()
if '\t"net"\n' not in s:
    raise SystemExit("serve net import anchor not found")
p.write_text(s.replace('\t"net"\n', "", 1))
