#timeout 60
#push native-7fc4237-issue.json /tmp/native-7fc4237-issue.json
set -eu
python3 - <<'PYJOB'
from pathlib import Path
import json,hashlib,urllib.request
payload=Path('/tmp/native-7fc4237-issue.json').read_bytes()
assert hashlib.sha256(payload).hexdigest()=='7a74fcc35c2bcb119c583a32f3cf2088382b5799441f1259bb46d4c2684ac0d9'
issue=json.loads(payload);assert issue['id']=='ISS-094'
roots={'/home/chuanming/golive-telephony/journal': '31feb56b72c760f6e20dbe6e88517f2783042fb1a63fe702939ce393927f7183', '/home/chuanming/golive-sip-ab/tool-ack-focused-20261101/state/notes': '2fddac26cf3b4358f88d923789912cf666cfa4cc3b46ee237d844f6d4a0bcb5b'}
pending=[]
for name,sha in roots.items():
 p=Path(name)/'issues.json';raw=p.read_bytes();assert hashlib.sha256(raw).hexdigest()==sha
 x=json.loads(raw);matches=[n for n,i in enumerate(x['issues']) if i['id']=='ISS-094'];assert len(matches)==1
 x['issues'][matches[0]]=issue;pending.append((p,x))
for p,x in pending:
 tmp=p.with_suffix('.native-7fc.tmp');tmp.write_text(json.dumps(x,ensure_ascii=False,indent=1)+'\n');tmp.replace(p)
token=Path('/home/chuanming/golive-desk/codex.token').read_text().strip()
body='Deployed exact-source CI-passed 7fc4237 to idle native SIP engine 9895 at 14:55:38. Full CI 41281964710, runtime readiness and no-dial adapter preflight passed. Earlier quote-heavy simulator exercises new caller protection five times; conversational p95 988 ms n=6, quotation readout p95 6341 ms n=5. Count and one unverified chained result/readout still fail qualification. Previous f12b6f5 binary retained; original configs/GoLive 8765/native Journal/Active Call unchanged. No real call or physical handset qualification.'
r=urllib.request.Request('http://127.0.0.1:8792/api/logs/telephony-latency',data=body.encode(),headers={'Authorization':'Bearer '+token,'Content-Type':'text/plain; charset=utf-8'},method='POST')
with urllib.request.urlopen(r,timeout=15) as q:assert q.status==200
print(json.dumps({'published_issue':'ISS-094','deployment':'7fc4237','desk_updated':True}))
PYJOB
