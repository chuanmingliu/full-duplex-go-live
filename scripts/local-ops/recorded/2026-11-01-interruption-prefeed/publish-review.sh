#timeout 100
#pull /home/chuanming/golive-telephony/codex-latency/published-interruption-f1fd9a6.json published-interruption-f1fd9a6.json
set -eu
python3 -B - <<'PY'
from pathlib import Path
import hashlib,json,os,urllib.request
os.umask(0o077)
root=Path.home()/'golive-telephony';j=root/'journal'
receipt=root/'codex-latency/published-interruption-f1fd9a6.json'
assert not receipt.exists(),'Already attempted; inspect instead of replaying'
raw=(Path.home()/'golive/interruption-review-payload.json').read_bytes()
assert hashlib.sha256(raw).hexdigest()=='76062b5645f201b356124e203be9af2a22aa278878a70b50930b22260465972c'
payload=json.loads(raw)
expected={'out-mvxor222-6g7h':'45b5564','out-mvxpmcug-bpqi':'f1fd9a6','out-mvxpoc4i-ta7z':'f1fd9a6'}
assert set(payload['calls'])==set(payload['retros'])==set(expected)
def get(path):
 with urllib.request.urlopen('http://192.168.71.9:8790'+path,timeout=15) as f:return json.load(f)
def atomic(p,raw):
 temp=p.with_name(p.name+'.interruption.tmp')
 with temp.open('xb') as f:f.write(raw)
 os.replace(temp,p)
for call,build in expected.items():
 d=get('/api/calls/'+call)
 assert d['call']['id']==call and d['call']['build']==build and d['call']['ended_at'] and d['archive']['archived'] and d['archive']['audio']
 assert not (j/'retros'/f'{call}.md').exists()
ip=j/'issues.json';old=ip.read_bytes()
assert hashlib.sha256(old).hexdigest()=='197bd79418185d596125cb80f34bcbd6a5902bc3af1dbfc8ab6e194aa7e8c509','Issue inventory changed'
data=json.loads(old);matches=[x for x in data['issues'] if x['id']=='ISS-095'];assert len(matches)==1 and matches[0]['status']=='open'
assert 'out-mvxpmcug-bpqi' not in matches[0]['verified']
state={'stage':'prepared','candidate':'f1fd9a6','calls':payload['calls'],'issue_id':'ISS-095','issue_status':'open','live_improvement_proven':False}
atomic(receipt,(json.dumps(state,indent=2)+'\n').encode())
with (root/'codex-latency/issues-before-interruption-f1fd9a6.json').open('xb') as f:f.write(old)
matches[0]['verified']+=' '+payload['note'];matches[0]['fix']=payload['fix']
atomic(ip,(json.dumps(data,ensure_ascii=False,indent=1)+'\n').encode())
hashes={}
for call,text in payload['retros'].items():
 p=j/'retros'/f'{call}.md'
 with p.open('x') as f:f.write(text)
 hashes[call]=hashlib.sha256(p.read_bytes()).hexdigest()
 assert get('/api/calls/'+call).get('retrospective')
found=[x for x in get('/api/issues')['issues'] if x['id']=='ISS-095'];assert len(found)==1 and found[0]['status']=='open' and all(c in found[0]['verified'] for c in expected)
state.update(stage='verified',issues_sha256=hashlib.sha256(ip.read_bytes()).hexdigest(),retros_sha256=hashes)
atomic(receipt,(json.dumps(state,indent=2)+'\n').encode());print(json.dumps(state))
PY
