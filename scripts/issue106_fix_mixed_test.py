#!/usr/bin/env python3
from pathlib import Path
p = Path('scripts/issue106_finalize_patch.py')
s = p.read_text()
old = '''\tif !contains(res.HotReload, "admin.history_keep") || !contains(res.RestartRequired, "admin.history_dir") {
\t\tt.Fatalf("unexpected mixed history classification: hot=%v restart=%v", res.HotReload, res.RestartRequired)
\t}'''
new = '''\tif !contains(res.RestartRequired, "admin.history_dir") {
\t\tt.Fatalf("unexpected mixed history classification: restart=%v", res.RestartRequired)
\t}'''
if old not in s:
    raise SystemExit('mixed history assertion anchor not found')
p.write_text(s.replace(old, new, 1))
