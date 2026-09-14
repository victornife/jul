#!/usr/bin/env python3
from pathlib import Path

path = Path("internal/server/reload_plan.go")
text = path.read_text()

old_import = '''\t"jul/internal/config"\n\t"jul/internal/lifecycle"\n\t"jul/internal/upstream"\n'''
new_import = '''\t"jul/internal/config"\n\t"jul/internal/lifecycle"\n\t"jul/internal/observability"\n\t"jul/internal/upstream"\n'''
if old_import not in text:
    raise SystemExit("reload_plan import anchor not found")
text = text.replace(old_import, new_import, 1)

old_publish = '''\tp.Runtime.Commit()\n\t// Alt-Svc max-age hot reload (#161): unlike certificate rotation, building\n'''
new_publish = '''\tp.Runtime.Commit()\n\n\t// #99: publish the candidate root sampling ratio inside the same no-fail\n\t// Publish transaction, before candidate handlers/config become reachable.\n\t// Canonical config validation has already accepted the ratio, and the\n\t// observability seam performs one atomic immutable-state swap only: no OTel\n\t// provider/exporter/global-state rebuild, network I/O, teardown or failure.\n\tobservability.UpdateTracingSampleRatio(p.Candidate.Effective.Observability.Tracing.SampleRatio)\n\n\t// Alt-Svc max-age hot reload (#161): unlike certificate rotation, building\n'''
if old_publish not in text:
    raise SystemExit("reload_plan publish anchor not found")
text = text.replace(old_publish, new_publish, 1)
path.write_text(text)
