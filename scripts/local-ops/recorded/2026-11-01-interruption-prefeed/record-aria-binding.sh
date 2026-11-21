#timeout 60
#pull /home/chuanming/golive-telephony/codex-latency/aria-binding-20261101/receipt.json aria-binding-20261101.json
set -eu
python3 -B - <<'PY'
from pathlib import Path
import hashlib,json,os,urllib.request,subprocess,re
os.umask(0o077)
root=Path.home()/'golive-telephony'; dest=root/'codex-latency/aria-binding-20261101'
assert not dest.exists(),'Binding snapshot already exists; inspect rather than overwrite'
def get(u):
 with urllib.request.urlopen(u,timeout=8) as r:return json.load(r)
b=get('http://127.0.0.1:3000/api/realtime/brain');h=get('http://127.0.0.1:8765/healthz')
assert b['backend']=='local' and b['endpoint']=='ws://127.0.0.1:8765/v1/realtime' and b['revision']==13
assert h['version']=='45b5564' and h['status']=='ok' and h['sessions']==0
pids=set(re.findall(r'pid=(\d+)',subprocess.check_output(['ss','-lntpH','sport = :8765'],text=True)))
assert len(pids)==1;pid=pids.pop(); proc=Path('/proc')/pid
binary=(proc/'exe').read_bytes(); sha=hashlib.sha256(binary).hexdigest()
assert sha=='0af8a624053a706bc4f2f20ec2b9f847409eef2e6f502e21520cca60a8ec8a29'
profile=(root/'configs/telephony.json').read_bytes()
assert hashlib.sha256(profile).hexdigest()=='072ab8dbda962eff682fa56fcd049f70cd3717530e64a06bec97e66618878e7d'
dest.mkdir(mode=0o700)
(dest/'golive-45b5564-linux-amd64').write_bytes(binary)
(dest/'golive-45b5564-linux-amd64').chmod(0o700)
(dest/'telephony.json').write_bytes(profile)
# Exact process launch material remains private on the box for an explicit restore.
(dest/'cmdline.private').write_bytes((proc/'cmdline').read_bytes())
(dest/'environ.private').write_bytes((proc/'environ').read_bytes())
state={'stage':'verified_existing_binding','aria_backend':'local','aria_revision':13,'endpoint':'ws://127.0.0.1:8765/v1/realtime','restore_engine_version':'45b5564','restore_engine_sha256':sha,'profile_sha256':hashlib.sha256(profile).hexdigest(),'running_pid':int(pid),'cwd':str((proc/'cwd').resolve()),'binding_changed':False,'restore_only_on_owner_request':True}
(dest/'receipt.json').write_text(json.dumps(state,indent=2)+'\n')
assert get('http://127.0.0.1:3000/api/realtime/brain')['revision']==13
print(json.dumps(state))
PY
