import json,sys,re,statistics as st
rs=[json.loads(l) for l in open(sys.argv[1])]
# Kernel OOM kills come from the systemd journal because memory.events is gone once the scope dies.
J=open(sys.argv[2]).read() if len(sys.argv)>2 else ''
def koom(r): return bool(re.search(rf"a695-{re.escape(r['scenario'])}-{r['config']}-r{r['rep']}-\d+\.scope: Failed with result 'oom-kill'",J))
M=1<<20
def fm(x): return '%d'%round(x/M) if isinstance(x,(int,float)) and x>=0 else 'n/a'
print('| scenario | variant | rep | phase | verified/s | verified | non-200 statuses | errors | client-cancelled | p50/p99 ms | Jul CPU s | phase peak MiB | inst |')
print('|---|---|---|---|---:|---:|---|---|---:|---|---:|---:|---:|')
for r in rs:
    for ph in r['phases']:
        for i,s in enumerate(ph['stats']):
            non={k:v for k,v in s['statuses'].items() if k!='200'}
            print(f"| {r['scenario']} | {r['config']} | {r['rep']} | {ph['name']}[{i}] | {s['verified_rps']:.1f} | {s['completed_2xx_verified']} | {non or ''} | {s['errors'] or ''} | {s['cancelled_by_client']} | {s['p50_ms']:.0f}/{s['p99_ms']:.0f} | {ph['jul_cpu_seconds']:.1f} | {fm(ph['peak_cg_mem'])} | {ph['peak_plugin_instances']} |")
print()
print('| scenario | variant | rep | cg peak MiB | OOM (events) | kernel OOM (journal) | exited | peak shmem MiB | peak spill files/MiB | peak goroutines | peak fds | base→final goroutines | base→final fds | base→final in-flight | post-GC heap MiB (base) | canary fails | origin calls |')
print('|---|---|---|---:|---|---|---|---:|---|---:|---:|---|---|---|---|---:|---:|')
for r in rs:
    b,f,p=r['baseline'],r.get('final_after_recovery') or {},r['peak']
    cf=sum(1 for c in r['canaries'] if not c['ok'])
    print(f"| {r['scenario']} | {r['config']} | {r['rep']} | {fm(r['cgroup_memory_peak'])} | {r['oom_killed']} | {koom(r)} | {r.get('jul_exited','')} | {fm(p['CgShmem'])} | {p['SpillFiles']}/{fm(p['SpillBytes'])} | {p['Goroutines']} | {p['FDs']} | {b.get('goroutines')}→{f.get('goroutines')} | {b.get('fds')}→{f.get('fds')} | {b.get('in_flight')}→{f.get('in_flight')} | {fm(r['post_gc_heap_inuse'])} ({fm(b.get('heap_inuse',0))}) | {cf} | {r['origin_calls']} |")
