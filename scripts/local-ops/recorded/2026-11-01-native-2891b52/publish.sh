#timeout 60
#push receipt-followup-issues-20261101.json /tmp/receipt-followup-issues-20261101.json
#push receipt-followup-retro-20261101.md /tmp/receipt-followup-retro-20261101.md
set -eu
python3 - <<'PYJOB'
from pathlib import Path
import hashlib,json,urllib.request
assert hashlib.sha256(Path('/tmp/receipt-followup-issues-20261101.json').read_bytes()).hexdigest()=='f049f64a42b4b11ec7c4fc6cb7fc3521882b5d64cd4b1bb1785b8156932bad56'
assert hashlib.sha256(Path('/tmp/receipt-followup-retro-20261101.md').read_bytes()).hexdigest()=='fac23045965d563fd7de4f7b339d7a1e16e38caa0fe614008ac3afbe899df566'
expected_retro='21efb02b883636daa2530aab7c6eb059e68856b6149e5f287173b326d0e9aef0'
cid='3af753f8-d623-43ff-a619-6c4a6557cccb'
raw=Path('/tmp/receipt-followup-retro-20261101.md').read_bytes()
updates=json.loads(Path('/tmp/receipt-followup-issues-20261101.json').read_text())
pending=[]
for name in ['/home/chuanming/golive-telephony/journal','/home/chuanming/golive-sip-ab/native-real-fb11e2c/state/notes']:
 root=Path(name);p=root/'issues.json';old=p.read_bytes();d=json.loads(old)
 by={x['id']:x for x in updates};assert set(by) <= {x['id'] for x in d['issues']}
 d['issues']=[by.get(x['id'],x) for x in d['issues']]
 retrospective=root/'retros'/(cid+'.md');assert hashlib.sha256(retrospective.read_bytes()).hexdigest()==expected_retro
 pending.append((p,old,(json.dumps(d,ensure_ascii=False,indent=1)+'\n').encode(),retrospective))
for p,old,new,retrospective in pending:
 assert p.read_bytes()==old,'concurrent issue update'
 tmp=p.with_suffix('.receipt-followup.tmp');tmp.write_bytes(new);tmp.replace(p)
 tmp=retrospective.with_suffix('.receipt-followup.tmp');tmp.write_bytes(raw);tmp.replace(retrospective)
token=Path('/home/chuanming/golive-desk/codex.token').read_text().strip()
body='2891b52 full CI 41286136566 passed; native SIP test engine 9895 deployed at 15:58:40, ready/idle, adapter no-dial preflight passed. ISS-097 playback receipt loss fixed; original config hashes, other engines, Journal and Active Call unchanged, 7fc4237 backup retained. Fresh candidate simulator: 10 normal outputs p95 1021 ms vs baseline 12 outputs p95 1092 ms, no missing timing records or receiver warnings; both scripted interruptions completed. Small variable-dialogue runs do not prove latency gain or handset qualification; first candidate TTS-failed trial retained. Real call remains normal p95 1260 ms, focus delay 2087 ms, acoustic capture invalid due clock discontinuity. ISS-096 UUID export fixed; ISS-098 diagnostic detail added but capture root cause still open. No redial.'
r=urllib.request.Request('http://127.0.0.1:8792/api/logs/telephony-latency',data=body.encode(),headers={'Authorization':'Bearer '+token,'Content-Type':'text/plain; charset=utf-8'},method='POST')
with urllib.request.urlopen(r,timeout=15) as response:assert response.status==200
print(json.dumps({'call_id':cid,'retrospective_updated':True,'issues_updated':[x['id'] for x in updates],'desk_updated':True}))
PYJOB
