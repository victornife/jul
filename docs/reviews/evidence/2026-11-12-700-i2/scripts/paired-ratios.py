#!/usr/bin/env python3
"""Descriptive drift check for the I2 normal comparison (not an ADR verdict rule).

Targets of one repetition run back to back (about 40 s apart), so host drift that
moves every target in the same window cancels in the within-block ratio
cand/main. The ADR verdict remains the frozen summarize rule; this only explains
why that rule reports noise.

usage: paired-ratios.py RUN_DIR... > paired-ratios.md
"""
import json, os, statistics, sys

blocks = {}
for run in sys.argv[1:]:
    tag = os.path.basename(os.path.normpath(run))
    cells = os.path.join(run, "cells")
    for scen in sorted(os.listdir(cells)):
        for var in sorted(os.listdir(os.path.join(cells, scen))):
            for tgt in os.listdir(os.path.join(cells, scen, var)):
                for rep in os.listdir(os.path.join(cells, scen, var, tgt)):
                    p = os.path.join(cells, scen, var, tgt, rep, "result.json")
                    if not os.path.exists(p):
                        continue
                    r = json.load(open(p))
                    phases = [ph for ph in r.get("phases", []) if ph.get("name") == "load"]
                    if not (r.get("valid") and r.get("correct")) or len(phases) != 1 or len(phases[0]["classes"]) != 1:
                        continue
                    v = phases[0]["classes"][0]["goodput_rps"]
                    blocks.setdefault((scen, var), {}).setdefault((tag, rep), {})[tgt] = v

print("| Scenario | Variant | Pair | Blocks | Within-block ratios (sorted) | Median ratio | Median regression |")
print("| --- | --- | --- | ---: | --- | ---: | ---: |")
for (scen, var), bl in sorted(blocks.items()):
    for base, cand in (("main", "cand"), ("base4", "cand"), ("base4", "main"), ("base", "base4")):
        rs = sorted(b[cand] / b[base] for b in bl.values() if base in b and cand in b and b[base] > 0)
        if not rs:
            continue
        m = statistics.median(rs)
        print(f"| {scen} | {var} | {base}:{cand} | {len(rs)} | {' '.join(f'{x:.3f}' for x in rs)} | {m:.3f} | {1 - m:+.3f} |")
