import os,json,re,time,subprocess,urllib.request,hashlib,math,datetime
from pathlib import Path
os.umask(0o077)
root=Path('/home/chuanming/golive-sip-ab/native-ab-20261031')
sim=Path('/home/chuanming/golive-sim')
def envfile(p):
 d={}
 for l in p.read_text().splitlines():
  if l.strip() and not l.startswith('#') and '=' in l:
   k,v=l.split('=',1);d[k.strip()]=v.strip().strip('\"\'')
 return d
token=envfile(sim/'tokens.env')['SIM_CONTROL_TOKEN']
def request(url,body=None,token=None):
 h={'Authorization':'Bearer '+token} if token else {}
 if body is not None:h['Content-Type']='application/json'
 if body is not None and url.endswith('/sim/v1/runs'):h['Idempotency-Key']=f'native-ab-20261031-r6-{idx}'
 q=urllib.request.Request(url,data=json.dumps(body).encode() if body is not None else None,headers=h)
 with urllib.request.urlopen(q,timeout=50) as r:return json.load(r)
def simreq(path,body=None):return request('http://127.0.0.1:18865'+path,body,token)
def save(p,d):p.write_text(json.dumps(d,indent=2,ensure_ascii=False)+'\n')
def say(d):print(json.dumps(d),flush=True)
def idle():
 for port in [8765,9875,9885,9895]:
  assert request(f'http://127.0.0.1:{port}/v1/pool')['in_use']==0,'engine busy'
terminal={'completed','rejected','expired','failed','cancelled','aborted'}
assert not (root/'attempt-v6.json').exists(),'Already attempted; inspect records, do not repeat'
assert json.loads((root/'attempt-v5.json').read_text()).get('completed_at'),'previous experiment incomplete'
runs=simreq('/sim/v1/runs');runs=runs.get('runs',[]) if isinstance(runs,dict) else runs
assert all(r['phase'] in terminal for r in runs),'simulator busy'
idle()
assert hashlib.sha256((root/'adapter-fb11e2c').read_bytes()).hexdigest()=='314765664c46ca7eaf70402c749f117e5209df6bb7f197607fb543628e36fdc6','candidate binary changed'
assert request('http://127.0.0.1:9895/healthz')['version']=='9547d22'
scenario=json.loads((root/'private/scenario.json').read_text());saved=json.loads((root/'private/saved-call.json').read_text())
scenario['max_duration_ms']=60000
state={'order':['isolated-contact'],'runs':[],'started_at':datetime.datetime.now(datetime.timezone.utc).isoformat()};save(root/'attempt-v6.json',state)
for idx,mode in enumerate(state['order'],1):
 idle();name=f'r6-{idx:02d}-{mode}';out=root/name;out.mkdir()
 # Match the native originator's pre-dial cache preparation. warm=true cannot release a session.
 warm_deadline=time.monotonic()+25
 while True:
  warm=request('http://127.0.0.1:9895/v1/opening',{'greeting':saved.get('greeting',''),'warm':True})
  assert warm.get('ok') and not warm.get('kicked'),'unexpected warmup result'
  if warm.get('cached') and all(x.get('ready') for x in warm['cached']):break
  assert time.monotonic()<warm_deadline,'opening warmup timed out'
  time.sleep(.3)
 save(out/'opening-warm.json',{'ready':True,'bytes':[x.get('bytes') for x in warm['cached']]})

 # Unique reservation is retained before dialing; never retry an unknown outcome.
 result=simreq('/sim/v1/runs',scenario);run=result['run_id'];assert re.fullmatch(r'sim-[A-Za-z0-9_-]+',run)
 current={'mode':mode,'run':run,'stage':'reserved'};state['runs'].append(current);save(root/'attempt-v6.json',state);save(out/'reservation.json',result)
 assert simreq('/sim/v1/runs/'+run)['phase'] in {'prepared','waiting'}
 proc=None
 if mode=='activecall':
  # Existing original GoLive/Active Call workflow; destination is only the reservation.
  current['stage']='dial_requested';save(root/'attempt-v6.json',state)
  x=subprocess.run(['bash','/home/chuanming/golive-telephony/scripts/phone-brain.sh','call','sim',run],env=dict(os.environ,LAST_CALL=str(root/'private/saved-call.json'),SIM_DIR=str(sim)),capture_output=True,text=True,timeout=60)
  (out/'dial.log').write_text(x.stdout+x.stderr)
  m=re.search(r'paired: call (out-[A-Za-z0-9-]+) <-> run '+re.escape(run),x.stdout)
  assert x.returncode==0 and m,'baseline dial failed or unknown; inspect private log'
  call=m.group(1)
 else:
  call='ab-'+run
  cfg=json.loads((root/'private/adapter-template.json').read_text());cfg['allowed_uri']='sip:'+run+'@192.168.71.9';cfg['golive_audio_format']='pcm16';cfg['codec']='pcmu';cfg['contact_user']='golive-sip-adapter'
  assert re.fullmatch(r'sip:sim-[A-Za-z0-9_-]+@192\.168\.71\.9',cfg['allowed_uri'])
  save(out/'adapter.json',cfg);save(out/'call.json',{'call_id':call,**{k:saved.get(k,'') for k in ['instructions','greeting','voice']}})
  current['stage']='dial_requested';save(root/'attempt-v6.json',state)
  env=dict(os.environ,**envfile(root/'private/adapter.env'))
  log=(out/'dial.log').open('ab')
  proc=subprocess.Popen([str(root/'adapter-fb11e2c'),'-config',str(out/'adapter.json'),'-call',str(out/'call.json'),'-dial'],env=env,stdout=log,stderr=subprocess.STDOUT)
  current['adapter_pid']=proc.pid
 current.update(call=call,stage='dialed');save(root/'attempt-v6.json',state);say(current)
 deadline=time.monotonic()+155;last=None
 while time.monotonic()<deadline:
  r=simreq('/sim/v1/runs/'+run);phase=r['phase']
  if phase!=last:say({'run':run,'phase':phase});last=phase
  if phase in terminal:break
  if proc is not None and proc.poll() is not None and phase in {'prepared','waiting'}:
   save(out/'receiver-run.json',r);raise RuntimeError('adapter exited before connection; inspect private log')
  time.sleep(3)
 else:raise RuntimeError('simulation did not terminate within bound; inspect before more calls')
 current.update(stage='ended',phase=phase);save(root/'attempt-v6.json',state)
 if proc:
  try:proc.wait(timeout=4)
  except subprocess.TimeoutExpired:
   current['stale_after_receiver_end']=True;proc.terminate()
   try:proc.wait(timeout=8)
   except subprocess.TimeoutExpired:raise RuntimeError('adapter cleanup did not complete')
  current['adapter_exit']=proc.returncode
 report=simreq('/sim/v1/runs/'+run+'/report');save(out/'receiver-report.json',report)
 gaps=report.get('assistant_response_gaps',[])
 vals=sorted(g['value_ms'] for g in gaps if g.get('valid') is True and g.get('evidence')=='application_audio_boundary' and isinstance(g.get('value_ms'),(int,float)) and g['value_ms']>=0)
 current.update(valid_gaps=len(vals),gaps_ms=vals,median_ms=(vals[(len(vals)-1)//2] if vals else None),p95_ms=(vals[math.ceil(.95*len(vals))-1] if vals else None),termination_reason=report.get('termination_reason'))
 save(root/'attempt-v6.json',state);say(current)
 time.sleep(3)
 # Require all engines idle, including production, before the next simulation.
 idle()
state['completed_at']=datetime.datetime.now(datetime.timezone.utc).isoformat();save(root/'attempt-v6.json',state)
say({'complete':True,'root':str(root)})
