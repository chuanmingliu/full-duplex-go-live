import os,json,re,time,hashlib,subprocess,urllib.request,datetime
from pathlib import Path
os.umask(0o077)
r=Path('/home/chuanming/golive-sip-ab/native-real-fb11e2c');cid='280fdab1-4078-4627-bf79-4eef8f627719'
def get(port,path):return json.load(urllib.request.urlopen(f'http://127.0.0.1:{port}'+path,timeout=5))
for port in [8765,9875,9885,9895]:assert get(port,'/v1/pool')['in_use']==0,'active session; no dial'
assert get(9895,'/healthz')['version']=='7fc4237'
assert hashlib.sha256((r/'bin/adapter').read_bytes()).hexdigest()=='314765664c46ca7eaf70402c749f117e5209df6bb7f197607fb543628e36fdc6'
cfg=json.loads((r/'private/adapter.json').read_text());assert cfg['golive_audio_format']=='pcm16' and cfg['codec']=='pcmu' and cfg['contact_user']=='golive-sip-adapter'
allowed=next(l.split('=',1)[1].strip().strip('\"\'') for l in Path('/home/chuanming/aria/.env').read_text().splitlines() if l.startswith('ALLOWED_CALLEE='))
assert re.fullmatch(r'\+?[0-9]{7,16}',allowed)
assert cfg['allowed_uri'].split('@')[0]=='sip:'+allowed,'configured recipient changed'
assert subprocess.check_output(['docker','inspect','--format','{{.State.Running}}','active-call'],text=True).strip()=='true'
saved=json.loads((r/'private/saved-call.json').read_text());current=json.loads(Path('/home/chuanming/golive-last-call.json').read_text())
assert all(saved.get(k)==current.get(k) for k in ['instructions','greeting','voice'])
assert cfg['golive_url']=='http://127.0.0.1:9895'
assert hashlib.sha256((r/'bin/golive').read_bytes()).hexdigest()=='7d0d1e61713e85b6f903bfaa2aff46eebd3e834c8c5660b7d4a6bc93396ac65d'
engine=json.loads(subprocess.check_output(['docker','inspect','golive-native-sip-fb11e2c-engine-1'],text=True))[0]
assert engine['State']['Running']
pid=engine['State']['Pid']
live=dict(x.decode().split('=',1) for x in Path(f'/proc/{pid}/environ').read_bytes().split(b'\0') if b'=' in x)
assert live['GOLIVE_REALTIME_STORE_CALLS']=='1' and live['GOLIVE_STORE_DIR']=='/data/store'
with urllib.request.urlopen('http://192.168.71.9:9896/api/overview',timeout=10) as q:overview=json.load(q)
assert overview['recording'] and overview['retention_hours']==8760
profile=json.loads((r/'private/agent.json').read_text())
assert profile['realtime']['hold_for_opening'] and profile['realtime']['store_calls']
assert not (r/'state/adapter'/cid).exists()
record=r/'state'/('receipt-'+cid+'.json');assert not record.exists(),'already attempted; inspect instead'
# Original native greeting cache, with release held until the SIP answer.
for _ in range(100):
 body=json.dumps({'greeting':saved.get('greeting',''),'warm':True}).encode()
 req=urllib.request.Request('http://127.0.0.1:9895/v1/opening',data=body,headers={'Content-Type':'application/json'})
 with urllib.request.urlopen(req,timeout=15) as q:w=json.load(q)
 assert w.get('ok') and not w.get('kicked')
 if w.get('cached') and all(x.get('ready') for x in w['cached']):break
 time.sleep(.3)
else:raise RuntimeError('opening not warmed; no dial')
(r/'state/store').mkdir(exist_ok=True);(r/'state/calls').mkdir(exist_ok=True)
call={'call_id':cid,**{k:saved.get(k,'') for k in ['instructions','greeting','voice']}}
with (r/'state/calls'/(cid+'.json')).open('x') as f:json.dump(call,f)
state={'call_id':cid,'kind':'real','adapter_commit':'fb11e2c','engine_commit':'7fc4237','journal':'http://192.168.71.9:9896','stage':'dial_requested','at':datetime.datetime.now(datetime.timezone.utc).isoformat(),'physical_handset_capture_verified':False,'assumed_handset_overhead_ms':50}
with record.open('x') as f:json.dump(state,f)
name='golive-native-sip-call-280fdab1-4078-4627-bf79-4eef8f627719'
x=subprocess.run(['docker','compose','-f',str(r/'compose.json'),'run','-d','--no-deps','--name',name,'adapter','-config','/private/adapter.json','-call','/data/calls/'+cid+'.json','-dial'],capture_output=True,text=True,timeout=25)
(r/'state'/('launch-'+cid+'.log')).write_text(x.stdout+x.stderr)
assert x.returncode==0,'dial launch outcome must be inspected; never retry'
state.update(stage='submitted',container=name);record.write_text(json.dumps(state,indent=2)+'\n');print(json.dumps(state),flush=True)
for _ in range(75):
 p=r/'state/adapter'/cid/'events.jsonl'
 if p.exists():
  events=[json.loads(l) for l in p.read_text().splitlines() if l.strip()]
  if any(e['type']=='answered' for e in events):
   state.update(stage='answered');record.write_text(json.dumps(state,indent=2)+'\n');print(json.dumps({'call_id':cid,'stage':'answered','recording_events_present':True}),flush=True);break
  if any(e['type']=='call_end' for e in events):
   state.update(stage='ended');record.write_text(json.dumps(state,indent=2)+'\n');print(json.dumps({'call_id':cid,'stage':'ended_before_answer','sip_statuses':[e.get('data',{}).get('status') for e in events if e['type']=='sip_response']}),flush=True);break
 time.sleep(.5)
