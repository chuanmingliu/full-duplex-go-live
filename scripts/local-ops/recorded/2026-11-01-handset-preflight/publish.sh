#timeout 60
#push handset-preflight-issue-20261101.json /tmp/handset-preflight-issue-20261101.json
set -eu
python3 - <<'PY'
from pathlib import Path
import json,hashlib,urllib.request
raw=Path('/tmp/handset-preflight-issue-20261101.json').read_bytes()
assert hashlib.sha256(raw).hexdigest()=='8cf961921fa0b3b73941128ad65db9ac50a351f98acef05dbfb77a850a3c6872'
issue=json.loads(raw);assert issue['id']=='ISS-096'
pending=[]
for name in ['/home/chuanming/golive-telephony/journal','/home/chuanming/golive-sip-ab/native-real-fb11e2c/state/notes']:
 p=Path(name)/'issues.json';old=p.read_bytes();d=json.loads(old)
 assert not any(x['id']=='ISS-096' for x in d['issues']),'issue exists; inspect, do not replay'
 d['issues'].append(issue);pending.append((p,old,(json.dumps(d,ensure_ascii=False,indent=1)+'\n').encode()))
for p,old,new in pending:
 assert p.read_bytes()==old,'concurrent issue update'
 tmp=p.with_suffix('.handset-preflight.tmp');tmp.write_bytes(new);tmp.replace(p)
token=Path('/home/chuanming/golive-desk/codex.token').read_text().strip()
body='ISS-096 fixed in pushed 1ec5d8c: recorder and acoustic verifier now accept canonical UUID IDs; explicit separate quotation SLA still requires >=20 normal content samples and mandatory quote boundary evidence. All 45 handset tests pass, UUID regressions reproduce prior failures. SIP full CI 41285082027 running. Corrected recorder served locally on Mac; native engine remains CI-passed 7fc4237. Prepared next UUID call only; not queued/dialed, waiting for current recording-ready confirmation. Original settings, archives, quotation delay and Active Call preserved.'
r=urllib.request.Request('http://127.0.0.1:8792/api/logs/telephony-latency',data=body.encode(),headers={'Authorization':'Bearer '+token,'Content-Type':'text/plain; charset=utf-8'},method='POST')
with urllib.request.urlopen(r,timeout=15) as response:assert response.status==200
print(json.dumps({'issue':'ISS-096','desk_updated':True,'runtime_unchanged':True}))
PY
