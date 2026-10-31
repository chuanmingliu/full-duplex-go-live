import os,json,subprocess,time,urllib.request,hashlib,signal,socket
from pathlib import Path
os.umask(0o077)
r=Path('/home/chuanming/golive-sip-ab/native-ab-20261031')
assert json.loads((r/'attempt-v7.json').read_text()).get('completed_at')
for port in [8765,9875,9885,9895]:
 assert json.load(urllib.request.urlopen(f'http://127.0.0.1:{port}/v1/pool'))['in_use']==0
assert hashlib.sha256((r/'adapter-fb11e2c').read_bytes()).hexdigest()=='314765664c46ca7eaf70402c749f117e5209df6bb7f197607fb543628e36fdc6'
env=dict(os.environ)
for line in (r/'private/adapter.env').read_text().splitlines():
 if line.strip() and not line.startswith('#') and '=' in line:
  k,v=line.split('=',1);env[k.strip()]=v.strip().strip('\"\'')
x=subprocess.run([str(r/'adapter-fb11e2c'),'-config',str(r/'private/adapter-native-candidate.json')],env=env,capture_output=True,text=True,timeout=15)
assert x.returncode==0,'candidate readiness check failed'
x=subprocess.run(['python3',str(r/'archive-native-ab.py')],capture_output=True,text=True,timeout=60)
assert x.returncode==0,'archive verification failed; preserve running services and inspect'
verified=json.loads(x.stdout)
assert len(verified)==10 and all(x['files_verified']>=5 and x['channels_with_signal']==2 for x in verified)
pid=int((r/'engine.pid').read_text());proc=Path('/proc')/str(pid)
assert os.readlink(proc/'exe')==str(r/'golive')
assert str(r/'private/agent.json').encode() in (proc/'cmdline').read_bytes().split(b'\0')
os.kill(pid,signal.SIGTERM)
for _ in range(50):
 if not proc.exists() or '\nState:\tZ' in (proc/'status').read_text():break
 time.sleep(.2)
else:raise RuntimeError('test engine did not stop; do not force kill')
with socket.socket() as s:
 assert s.connect_ex(('127.0.0.1',9895))!=0,'test port still open'
for name in ['native-ab-sip-capture','native-ab-pcm-sip-capture','native-ab-bye-capture','native-ab-contact-capture','native-ab-qualification-capture']:
 exists=subprocess.run(['docker','inspect',name],capture_output=True).returncode==0
 if exists:
  subprocess.run(['docker','stop','--time','5',name],capture_output=True,check=True)
  subprocess.run(['docker','rm',name],capture_output=True,check=True)
h=json.load(urllib.request.urlopen('http://127.0.0.1:8765/healthz'))
assert h['version']=='9547d22' and h['sessions']==0
m=json.loads((r/'manifest.json').read_text())
result=dict(candidate_ready_before_stop=True,archived_adapter_calls=len(verified),verified_adapter_files=sum(x['files_verified'] for x in verified),test_engine_stopped=True,capture_containers_removed=True,original_version=h['version'],original_idle=True,original_profile_unchanged=hashlib.sha256(Path('/home/chuanming/golive-telephony/configs/telephony.json').read_bytes()).hexdigest()==m['original_profile_sha256'],original_saved_call_unchanged=hashlib.sha256(Path('/home/chuanming/golive-last-call.json').read_bytes()).hexdigest()==m['saved_call_sha256'])
(r/'finalization.json').write_text(json.dumps(result,indent=2)+'\n');print(json.dumps(result))
