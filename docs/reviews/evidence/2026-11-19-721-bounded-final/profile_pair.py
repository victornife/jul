import pathlib,subprocess,time,threading,urllib.request,tomllib,json
B=pathlib.Path('/workspace/scratch/fecc8ca1efe5/721-bounded-final')
out=B/'profiled-p1'; profiles=B/'cpu-profiles';profiles.mkdir()
cmd=['taskset','-c','4-8',str(B/'ia-diagnostic'),'run','-out',str(out),'-profile','bounded-local','-envelope','none','-scenario','p1-large-body-plugin-h1','-variants','wasm','-reps','1','-target','candidate:current:'+str(B/'jul-candidate'),'-target','main:current:'+str(B/'jul-main'),'-origin-cpus','2,3','-guest','/workspace/scratch/fecc8ca1efe5/721-diagnostic/inspect-load.wasm','-guest-source','examples/plugins/inspect-load']
(B/'profiled-matrix.txt').write_text('Supplement declared after the initial p1 functional pair showed a 15.7% candidate slowdown in a single CPU-saturated sample. One reverse-order pair, identical configuration, with a 15-second CPU profile per target. All samples retained; profiling results are diagnostic, not throughput acceptance.\n')
threads=[];seen=set();results={}
def capture(p):
 label=p.parent.parent.name
 try:
  d=tomllib.loads(p.read_text());port=d['admin']['listen'].split(':')[-1]
  time.sleep(8)
  req=urllib.request.Request('http://127.0.0.1:'+port+'/debug/pprof/profile?seconds=15',headers={'Authorization':'Bearer assessment-695-token-not-a-secret'})
  with urllib.request.urlopen(req,timeout=25) as r:data=r.read()
  (profiles/(label+'.cpu.pprof')).write_bytes(data)
  results[label]={'cpu_bytes':len(data)}
 except Exception as e:results[label]={'error':str(e)}
with (B/'profiled-p1-run.txt').open('w') as log:
 child=subprocess.Popen(cmd,stdout=log,stderr=subprocess.STDOUT)
 while child.poll() is None:
  for p in out.glob('cells/p1-large-body-plugin-h1/wasm/*/rep1/config.toml'):
   if p not in seen:
    seen.add(p);t=threading.Thread(target=capture,args=(p,));t.start();threads.append(t)
  time.sleep(.1)
 for t in threads:t.join()
 (profiles/'capture.json').write_text(json.dumps(results,indent=2)+'\n')
 if child.returncode:raise SystemExit(child.returncode)
 print(json.dumps(results))
