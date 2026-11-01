"""Idle-only upgrade of the native SIP test engine; preserve original services."""
from pathlib import Path
import datetime
import fcntl
import hashlib
import json
import os
import shutil
import subprocess
import time
import urllib.request

os.umask(0o077)
root=Path('/home/chuanming/golive-sip-ab/native-real-fb11e2c')
attempt=root/'releases/engine-f12b6f5-20261101'
assert not attempt.exists(), 'attempt exists: inspect; do not replay'
old_hash='e8648a7942d1e7c6a2c726033ab86efd786ca5801d302712d95cbc1a9bc7c493'
new_hash='7d367f327be47ee6abfd29e0bca5bc9cecc940ebacf0130c3bda3cab2bc166a4'
source=Path('/tmp/golive-covered-f12b6f5-linux')
receipt=json.loads(Path('/tmp/native-f12b6f5-ci.json').read_text())
assert receipt['headSha']=='f12b6f5f1e0c949fad49cbc2321ff353f2046dd8'
assert receipt['status']=='completed' and receipt['conclusion']=='success'
assert {j['name'] for j in receipt['jobs']}=={'verify','simulate'}
assert all(j['status']=='completed' and j['conclusion']=='success' for j in receipt['jobs'])
expected={
 'compose.json':'18e1084e7897d96eb99fd789a668fc390923072431f08313fe1b2c63949eb480',
 'private/adapter.env':'15b75b42aea4a69efb2de6d7ed277617d16dffe1a30bda0296bf0e38099e3272',
 'private/adapter.json':'6564e4268bf1a92cd8b669730887ce4d44bc9ca869c67a77e169af56f8e21d41',
 'private/agent.json':'2762a9bf9d1b0a208b91534454fed287a9e01621149edce85599b9be83880fe1',
 'private/engine.env':'33c4797f1bf76ee48bec61f64caf1b376e709e5592c4ba350713789f007fcf3b',
 'private/providers.env':'142ee6a2fd67af61666c96d73f40229e7cc66c34e8595af0ea2f8acb8a14209c',
 'private/saved-call.json':'d43d88e3ed8d84163ebfe78a5d74c32463b6827c48af747570e7a9830d13ddf7',
}
def digest(p):return hashlib.sha256(p.read_bytes()).hexdigest()
def get(port,path):
 with urllib.request.urlopen(f'http://127.0.0.1:{port}'+path,timeout=8) as response:return json.load(response)
def idle():
 for port in [8765,9875,9885,9895]:assert get(port,'/v1/pool')['in_use']==0,'call active; no restart'
def identity(container):
 x=json.loads(subprocess.check_output(['docker','inspect',container],text=True))[0]
 return (x['Id'],x['State']['StartedAt'],x['State']['Running'])
def restart():
 result=subprocess.run(['docker','compose','-f',str(root/'compose.json'),'restart','-t','45','engine'],capture_output=True,text=True)
 with (attempt/'compose.log').open('a') as f:f.write(result.stdout+result.stderr)
 assert result.returncode==0,'restart failed: inspect private log'
def ready(version):
 for _ in range(100):
  try:
   h=get(9895,'/healthz');p=get(9895,'/v1/pool')
   if h.get('version')==version and p.get('ok') and p.get('in_use')==0 and p.get('capacity')==1:return
  except OSError:pass
  time.sleep(.5)
 raise RuntimeError('native engine readiness timeout')
def save(state):
 (attempt/'receipt.json').write_text(json.dumps(state,indent=2)+'\n')
def restore():
 try:assert get(9895,'/v1/pool')['in_use']==0,'new call active: rollback suppressed'
 except OSError:pass
 tmp=root/'bin/golive.rollback-f12b6f5'
 assert not tmp.exists()
 shutil.copy2(attempt/'golive-9547d22',tmp)
 assert digest(tmp)==old_hash
 tmp.replace(root/'bin/golive');restart();ready('9547d22')

lock=(root/'.codex-engine-deploy.lock').open('a')
fcntl.flock(lock,fcntl.LOCK_EX|fcntl.LOCK_NB)
idle()
assert get(9895,'/healthz')['version']=='9547d22'
assert digest(root/'bin/golive')==old_hash and digest(source)==new_hash
assert all(digest(root/name)==sha for name,sha in expected.items())
active=identity('active-call');journal=identity('golive-native-sip-fb11e2c-journal-1')
engine=identity('golive-native-sip-fb11e2c-engine-1')
assert engine[0]=='3c64195ad0bac081aa9f0577b0e662ff4b8f3ce898b1fcd81367bb58abe3281b' and engine[2]
assert active[0]=='de78b525beea3702b68ca8d310fbf6a5a0347fe2d8ec32f38e5bc1756b9dff2e' and active[2] and journal[2]
original_versions={p:get(p,'/healthz')['version'] for p in [8765,9875,9885]}
protected=json.loads(Path('/home/chuanming/golive-sip-ab/tool-ack-focused-20261101/original-hashes.json').read_text())
assert all(digest(Path(p))==sha for p,sha in protected.items())
attempt.mkdir(parents=True)
shutil.copy2(root/'bin/golive',attempt/'golive-9547d22')
assert digest(attempt/'golive-9547d22')==old_hash
state={'stage':'prepared','version':'f12b6f5','old_version':'9547d22','ci':receipt['url'],'binary_sha256':new_hash,'backup':str(attempt/'golive-9547d22'),'started_at':datetime.datetime.now(datetime.timezone.utc).isoformat(),'no_dial':True}
save(state)
tmp=root/'bin/golive.f12b6f5.tmp'
assert not tmp.exists()
shutil.copy2(source,tmp);tmp.chmod(0o700)
assert digest(tmp)==new_hash
idle()  # check again immediately before touching the executable/service
try:
 tmp.replace(root/'bin/golive');state['stage']='installed';save(state)
 restart();ready('f12b6f5')
 result=subprocess.run(['docker','compose','-f',str(root/'compose.json'),'run','--rm','--no-deps','adapter'],capture_output=True,text=True)
 (attempt/'adapter-preflight.log').write_text(result.stdout+result.stderr)
 assert result.returncode==0,'adapter preflight failed'
 assert identity('active-call')==active and identity('golive-native-sip-fb11e2c-journal-1')==journal
 assert all(digest(root/name)==sha for name,sha in expected.items())
 assert all(digest(Path(p))==sha for p,sha in protected.items())
 assert all(get(p,'/healthz')['version']==v for p,v in original_versions.items())
 assert digest(root/'bin/golive')==new_hash
 state.update(stage='verified',completed_at=datetime.datetime.now(datetime.timezone.utc).isoformat(),original_configs_unchanged=True,active_call_unchanged=True,journal_unchanged=True)
 save(state)
except Exception:
 state['stage']='failed';save(state)
 restore();state['stage']='rolled_back';save(state)
 raise
print(json.dumps(state),flush=True)
