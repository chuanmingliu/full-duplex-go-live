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
attempt=root/'releases/engine-2891b52-20261101'
assert not attempt.exists(), 'attempt exists: inspect; do not replay'
old_hash='7d0d1e61713e85b6f903bfaa2aff46eebd3e834c8c5660b7d4a6bc93396ac65d'
new_hash='82152bb45d6ef50a57370e9dd8c6d26caf60db6c4be566015586dd8dd8fa7472'
source=Path('/tmp/golive-receipt-2891b52-linux')
receipt=json.loads(Path('/tmp/native-2891b52-ci.json').read_text())
assert receipt['headSha']=='2891b52af96194eb8e9dfca5d4d56f599c390f0a'
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
 tmp=root/'bin/golive.rollback-2891b52'
 assert not tmp.exists()
 shutil.copy2(attempt/'golive-7fc4237',tmp)
 assert digest(tmp)==old_hash
 tmp.replace(root/'bin/golive');restart();ready('7fc4237')

lock=(root/'.codex-engine-deploy.lock').open('a')
fcntl.flock(lock,fcntl.LOCK_EX|fcntl.LOCK_NB)
idle()
trial=Path('/home/chuanming/golive-sip-ab/tool-ack-focused-20261101/receipt-ab-2891b52-v4')
assert json.loads((trial/'attempt.json').read_text()).get('completed_at')
validations=json.loads((trial/'verification.json').read_text())
assert [v['version'] for v in validations]==['2891b52']
validation=validations[0]
assert validation['archive_files_verified']==5 and validation['audio_channels_with_signal']==2
assert validation['conversation_complete'] and validation['receiver_failure_count']==0
assert validation['metrics']['counts']['missing_output_records']==0
# This promotes a verified accounting fix, not a latency-qualified result.
# Short simulator samples and handset evidence still cannot qualify the goal.
assert get(9895,'/healthz')['version']=='7fc4237'
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
shutil.copy2(root/'bin/golive',attempt/'golive-7fc4237')
assert digest(attempt/'golive-7fc4237')==old_hash
state={'stage':'prepared','version':'2891b52','old_version':'7fc4237','ci':receipt['url'],'binary_sha256':new_hash,'backup':str(attempt/'golive-7fc4237'),'started_at':datetime.datetime.now(datetime.timezone.utc).isoformat(),'no_dial':True}
save(state)
tmp=root/'bin/golive.2891b52.tmp'
assert not tmp.exists()
shutil.copy2(source,tmp);tmp.chmod(0o700)
assert digest(tmp)==new_hash
idle()  # check again immediately before touching the executable/service
try:
 tmp.replace(root/'bin/golive');state['stage']='installed';save(state)
 restart();ready('2891b52')
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
