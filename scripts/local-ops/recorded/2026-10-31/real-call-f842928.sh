#timeout 100
#pull /home/chuanming/golive-telephony/codex-latency/real-call-f842928-20261031-next1.json codex-real-call-f842928-20261031-next1.json
set -eu
cd "$HOME/golive-telephony"
python3 -B - <<'PY'
from pathlib import Path
import datetime,hashlib,json,os,re,subprocess,urllib.request
os.umask(0o077)
root=Path.home()/'golive-telephony'
receipt=root/'codex-latency/real-call-f842928-20261031-next1.json'
assert not receipt.exists(),'This attempt already exists; inspect it rather than redialling'
def get(url):
 with urllib.request.urlopen(url,timeout=8) as r:return json.load(r)
def sha(raw):return hashlib.sha256(raw).hexdigest()
h=get('http://127.0.0.1:8765/healthz')
assert h.get('status')=='ok' and h.get('sessions')==0 and h.get('version')=='f842928','Service is busy, unhealthy or a different build'
assert get('http://127.0.0.1:8791/healthz').get('status')=='ok','CRM is not healthy'
saved=json.loads((Path.home()/'golive-last-call.json').read_text())
assert saved.get('call')=='out-mvvy09vm-1d57','Saved call changed; inspect before retrying'
last=get('http://192.168.71.9:8790/api/calls/'+saved['call'])['call']
assert last.get('ended_at') and not last['ended_at'].startswith('0001-'),'Previous call has not ended'
expected={'instructions':'639dcecf24dddc73274a7412c017e36258b51679c2fd3ad4e72a595453aa8d25','voice':'65d9d6c5c7c2d5c29e38b777edf8da1dbd764f67deeeaa230724967cafe4e684','greeting':'fa7c33a6b9c732413d932a7453c5c85d4936fcdd8515822bf92e3fc07e2f4df8'}
assert all(sha(saved.get(k,'').encode())==v for k,v in expected.items()),'Saved prompt, voice or greeting changed'
assert sha((root/'configs/telephony.json').read_bytes())=='072ab8dbda962eff682fa56fcd049f70cd3717530e64a06bec97e66618878e7d','Phone profile changed'
listeners=subprocess.check_output(['ss','-lntpH','sport = :8765'],text=True)
pids=set(re.findall(r'pid=(\d+)',listeners))
assert len(pids)==1,'Cannot identify phone process'
proc=Path('/proc')/pids.pop()
assert sha((proc/'exe').read_bytes())=='083caa6ad1cc8e07f5aac46c9105cc68a8e398e198e46062efb58c361b0a9837','Running binary differs'
env=dict(v.split(b'=',1) for v in (proc/'environ').read_bytes().split(b'\0') if b'=' in v)
assert env.get(b'GOLIVE_REALTIME_STORE_CALLS')==b'1' and env.get(b'GOLIVE_STORE_DIR') and env.get(b'GOLIVE_STORE_TTL_HOURS')==b'720','Live recording is not enabled as expected'
overview=get('http://192.168.71.9:8790/api/overview')
assert overview.get('recording') is True and overview.get('retention_hours')==720,'Journal recording state differs'
callee=''
for line in (Path.home()/'aria/.env').read_text().splitlines():
 if line.startswith('ALLOWED_CALLEE='):callee=line.split('=',1)[1].strip().strip('"\'')
assert callee and not callee.startswith('sip:') and 'simulator.invalid' not in callee,'Recipient is not configured as a real telephone number'
state={'stage':'dial_requested','kind':'real','build':'f842928','requested_at_utc':datetime.datetime.now(datetime.timezone.utc).isoformat(),'server_recording_verified':True,'physical_handset_capture_verified':False,'previous_call':saved['call']}
def save(exclusive=False):
 with receipt.open('x' if exclusive else 'w') as f:
  json.dump(state,f,indent=2);f.write('\n');f.flush();os.fsync(f.fileno())
save(True)
print(json.dumps(state),flush=True)
try:
 r=subprocess.run(['bash','scripts/phone-brain.sh','call'],capture_output=True,text=True,timeout=60)
 tail=r.stdout.split('originator answered',1)[-1] if 'originator answered' in r.stdout else ''
 match=re.search(r'"call_id"\s*:\s*"(out-[A-Za-z0-9-]+)"',tail)
 if r.returncode!=0 or not match:
  state.update(stage='dial_outcome_unconfirmed',exit_code=r.returncode);save()
  print(json.dumps(state),flush=True);raise SystemExit('No automatic redial: inspect originator state')
 state.update(stage='dial_accepted',call_id=match.group(1),accepted_at_utc=datetime.datetime.now(datetime.timezone.utc).isoformat());save()
 print(json.dumps(state),flush=True)
except subprocess.TimeoutExpired:
 state['stage']='dial_outcome_unconfirmed';save()
 raise SystemExit('Dial timed out; do not automatically redial')
PY
