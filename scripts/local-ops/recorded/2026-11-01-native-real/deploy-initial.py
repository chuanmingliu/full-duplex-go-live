import os,json,hashlib,subprocess,socket,time,urllib.request,shutil
from pathlib import Path
os.umask(0o077)
r=Path('/home/chuanming/golive-sip-ab/native-real-fb11e2c');assert not r.exists(),'existing deployment; inspect instead'
source=Path('/home/chuanming/golive-sip-ab/native-ab-20261031');old=Path('/home/chuanming/golive-sip-ab/32bc369');base=Path('/home/chuanming/golive-telephony')
def get(port,path):return json.load(urllib.request.urlopen(f'http://127.0.0.1:{port}'+path,timeout=5))
for port in [8765,9875,9885]:assert get(port,'/v1/pool')['in_use']==0
h=get(8765,'/healthz');assert h['version']=='9547d22'
proc=Path('/proc/3314112');assert os.readlink(proc/'exe')==str(base/'bin/golive-linux-amd64')
env=dict(x.decode().split('=',1) for x in (proc/'environ').read_bytes().split(b'\0') if b'=' in x)
for port in [9895,9896]:
 with socket.socket() as s:s.bind(('0.0.0.0',port))
with socket.socket(socket.AF_INET,socket.SOCK_DGRAM) as s:s.bind(('0.0.0.0',15064))
for name in ['private','state','bin','state/adapter','state/notes/retros']:(r/name).mkdir(parents=True,exist_ok=True)
for src,name,sha in [(proc/'exe','golive','e8648a7942d1e7c6a2c726033ab86efd786ca5801d302712d95cbc1a9bc7c493'),(source/'adapter-fb11e2c','adapter','314765664c46ca7eaf70402c749f117e5209df6bb7f197607fb543628e36fdc6')]:
 b=src.read_bytes();assert hashlib.sha256(b).hexdigest()==sha;(r/'bin'/name).write_bytes(b);(r/'bin'/name).chmod(0o700)
shutil.copy2(source/'journal-f758708',r/'bin/journal');(r/'bin/journal').chmod(0o700)
profile=json.loads((base/'configs/telephony.json').read_text());profile.update(addr='127.0.0.1:9895',max_sessions=1,max_sessions_per_ip=1,hangup_url='',store_dir='/data/store',last_call_file='/data/last-call.json');profile.setdefault('realtime',{}).update(enabled=True,hold_for_opening=True,hold_timeout_seconds=180,store_calls=True)
(r/'private/agent.json').write_text(json.dumps(profile));shutil.copy2('/home/chuanming/golive-telephony.env',r/'private/providers.env')
env.update(GOLIVE_LAST_CALL_FILE='/data/last-call.json',GOLIVE_STORE_DIR='/data/store',GOLIVE_REALTIME_STORE_CALLS='1',GOLIVE_MAX_SESSIONS='1',GOLIVE_MAX_SESSIONS_PER_IP='1',GOLIVE_ADDR='127.0.0.1:9895')
assert all('\n' not in k+v and '\r' not in k+v for k,v in env.items())
(r/'private/engine.env').write_text(''.join(k+'='+v+'\n' for k,v in env.items()))
cfg=json.loads((old/'private/adapter.json').read_text());assert 'sim-' not in cfg['allowed_uri'];cfg.update(golive_url='http://127.0.0.1:9895',sip_listen='0.0.0.0:15064',rtp_min=31200,rtp_max=31231,record_dir='/data/adapter',golive_audio_format='pcm16',codec='pcmu',contact_user='golive-sip-adapter')
(r/'private/adapter.json').write_text(json.dumps(cfg));shutil.copy2(old/'private/adapter.env',r/'private/adapter.env')
saved=Path('/home/chuanming/golive-last-call.json').read_bytes();(r/'private/saved-call.json').write_bytes(saved);(r/'state/last-call.json').write_bytes(saved)
compose=json.loads((old/'compose.json').read_text());compose['name']='golive-native-sip-fb11e2c'
for name,s in compose['services'].items():
 s['image']='golive-sip-ringback:c3e049b';s['volumes']=['./private:/private:ro','./state:/data:rw','./bin:/runtime:ro'];s.pop('environment',None)
e=compose['services']['engine'];e['env_file']=[{'path':'./private/engine.env','format':'raw'}];e['working_dir']=str(base);e['volumes'] += [str(base)+':'+str(base)+':ro','/home/chuanming/golive-crm/api.token:/home/chuanming/golive-crm/api.token:ro','/home/chuanming/golive-edge/api.token:/home/chuanming/golive-edge/api.token:ro'];e['command']=['/bin/sh','-c','exec /runtime/golive -profile /private/agent.json -env /private/providers.env -addr 127.0.0.1:9895 >> /data/golive.log 2>&1']
j=compose['services']['journal'];j['command']=['/runtime/journal','-addr','192.168.71.9:9896','-log','/data/golive.log','-store','/data/store','-data','/data/journal','-archive','/data/archive','-adapter-records','/data/adapter','-notes','/data/notes','-last-call','/data/last-call.json','-retention-hours','720','-target','1200','-repo','https://github.com/chuanmingliu/full-duplex-go-live']
a=compose['services']['adapter'];a['entrypoint']=['/runtime/adapter'];a['command']=['-config','/private/adapter.json']
(r/'compose.json').write_text(json.dumps(compose,indent=2)+'\n')
x=subprocess.run(['docker','compose','-f',str(r/'compose.json'),'up','-d','engine','journal'],capture_output=True,text=True);(r/'deploy.log').write_text(x.stdout+x.stderr);assert x.returncode==0,'compose failed; inspect private deploy log'
for _ in range(60):
 try:
  p=get(9895,'/v1/pool')
  if p.get('ok') and p['in_use']==0 and p['capacity']==1:break
 except OSError:pass
 time.sleep(.5)
else:raise RuntimeError('new engine not ready')
assert get(9895,'/healthz')['version']=='9547d22'
x=subprocess.run(['docker','compose','-f',str(r/'compose.json'),'run','--rm','--no-deps','adapter'],capture_output=True,text=True);(r/'preflight.log').write_text(x.stdout+x.stderr);assert x.returncode==0,'adapter preflight failed'
with urllib.request.urlopen('http://192.168.71.9:9896/api/overview',timeout=10) as q:assert q.status==200
assert get(8765,'/healthz')['version']=='9547d22';assert subprocess.check_output(['docker','inspect','--format','{{.State.Running}}','active-call'],text=True).strip()=='true'
m=dict(adapter_commit='fb11e2c',engine_commit='9547d22',root=str(r),journal='http://192.168.71.9:9896',original_active_call_preserved=True,original_engine_preserved=True,saved_call_sha256=hashlib.sha256(saved).hexdigest(),ready=True)
(r/'deployment.json').write_text(json.dumps(m,indent=2)+'\n');print(json.dumps(m))
