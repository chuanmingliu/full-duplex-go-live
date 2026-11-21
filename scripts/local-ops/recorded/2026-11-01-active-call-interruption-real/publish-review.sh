#timeout 100
#pull /home/chuanming/golive-telephony/codex-latency/published-real-out-mvxr0ein-wwqh.json published-real-out-mvxr0ein-wwqh.json
set -eu
python3 -B - <<'PY'
from pathlib import Path
import hashlib,json,os,urllib.request
os.umask(0o077)
root=Path.home()/'golive-telephony';journal=root/'journal'
raw=(Path.home()/'golive/activecall-real-f1fd9a6-review-payload.json').read_bytes()
assert hashlib.sha256(raw).hexdigest()=='92cf083c0ded1bbd84ce7ea2cf86ec41eb67423f89a4ca69d4e2b3b3b992eaa8'
p=json.loads(raw);call=p['call'];assert call=='out-mvxr0ein-wwqh'
receipt=root/('codex-latency/published-real-'+call+'.json')
assert not receipt.exists(),'Review already attempted; inspect instead of replaying'
def get(path):
 with urllib.request.urlopen('http://192.168.71.9:8790'+path,timeout=15) as r:return json.load(r)
def atomic(path,raw):
 temp=path.with_name(path.name+'.real-review.tmp')
 with temp.open('xb') as f:f.write(raw)
 os.replace(temp,path)
d=get('/api/calls/'+call)
assert d['call']['id']==call and d['call']['build']=='f1fd9a6' and d['call']['ended_at'] and d['archive']['archived'] and d['archive']['audio']
ip=journal/'issues.json';old=ip.read_bytes()
assert hashlib.sha256(old).hexdigest()=='236e33344d07c5a8968d719b6e1fba7739b0edee70d57409e884f392a0bb4948','Issue file changed'
data=json.loads(old);matches=[x for x in data['issues'] if x['id']=='ISS-095'];assert len(matches)==1
assert matches[0]['status']=='open' and call not in matches[0]['verified']
retro=journal/'retros'/f'{call}.md';assert not retro.exists()
state={'call':call,'stage':'prepared','build':'f1fd9a6','previous_call':'out-mvxo348d-w5pb','comparable_interruptions':15,'interruption_p50_ms':207,'interruption_p95_ms':694,'ordinary_n':28,'ordinary_p95_ms':1135,'canonical_conversational_n':29,'canonical_conversational_p95_ms':1260,'canonical_gate':'fail','acoustic_handset_verified':False}
atomic(receipt,(json.dumps(state,indent=2)+'\n').encode())
with (root/('codex-latency/issues-before-'+call+'.json')).open('xb') as f:f.write(old)
matches[0]['verified']+=' '+p['note'];atomic(ip,(json.dumps(data,ensure_ascii=False,indent=1)+'\n').encode())
with retro.open('x') as f:f.write(p['retro'])
assert get('/api/calls/'+call).get('retrospective')
rows=[x for x in get('/api/issues')['issues'] if x['id']=='ISS-095'];assert len(rows)==1 and rows[0]['status']=='open' and call in rows[0]['verified']
state.update(stage='verified',issue_id='ISS-095',issues_sha256=hashlib.sha256(ip.read_bytes()).hexdigest(),retro_sha256=hashlib.sha256(retro.read_bytes()).hexdigest())
atomic(receipt,(json.dumps(state,indent=2)+'\n').encode());print(json.dumps(state))
PY
