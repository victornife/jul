import sys,json
for f in sys.argv[1:]:
    for line in open(f):
        r=json.loads(line)
        for ph in r["phases"]:
            for i,s in enumerate(ph["stats"]):
                w=ph["workloads"][i]
                print(f'{f.split("/")[0]:22} {r["scenario"][:20]:20} {r["config"]:5} {ph["name"]:6} wl{i} c={w["concurrency"]:3} ok={s["completed_2xx_verified"]:5} rps={s["verified_rps"]:8} st={s["statuses"]} err={s["errors"]} p50={s["p50_ms"]} p99={s["p99_ms"]} peakMiB={ph["peak_cg_mem"]>>20} cpu={ph["jul_cpu_seconds"]:.1f}')
        for k in ("oom","killed","exit","notes","Notes"):
            if k in r: print("   ",k,str(r[k])[:300])
