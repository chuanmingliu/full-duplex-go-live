#timeout 60
#push native-f12b6f5-issue.json /tmp/native-f12b6f5-issue.json
set -eu
python3 - <<'PYJOB'
from pathlib import Path
import json,hashlib,urllib.request
payload=Path('/tmp/native-f12b6f5-issue.json').read_bytes()
assert hashlib.sha256(payload).hexdigest()=='90ce7d8fdbef1a19f14b46b1e0776425c2d15b2585735bfe62be565f305853d3'
issue=json.loads(payload);assert issue['id']=='ISS-092'
roots={
 '/home/chuanming/golive-telephony/journal':'688326c098c3b0c4d368e164d902b714a96fde7f89fac083bab30268d3f540f5',
 '/home/chuanming/golive-sip-ab/tool-ack-focused-20261101/state/notes':'8a289f24573bc1fdeccfccbc9515afc2c79e6b469cb727d7e13dc8a80a9eac97',
}
pending=[]
for name,sha in roots.items():
 p=Path(name)/'issues.json';raw=p.read_bytes();assert hashlib.sha256(raw).hexdigest()==sha
 x=json.loads(raw);matches=[n for n,i in enumerate(x['issues']) if i['id']=='ISS-092'];assert len(matches)==1
 x['issues'][matches[0]]=issue;pending.append((p,x))
for p,x in pending:
 tmp=p.with_suffix('.native-f12.tmp');tmp.write_text(json.dumps(x,ensure_ascii=False,indent=1)+'\n');tmp.replace(p)
token=Path('/home/chuanming/golive-desk/codex.token').read_text().strip()
text='Deployed CI-passed f12b6f5 to idle native SIP engine 9895 at 14:35:13. Exact-source CI run 41280473376 passed both verify and simulate jobs. Runtime readiness and no-dial adapter preflight passed. Prior 9547d22 binary retained for rollback. Original configs, engine 8765, native Journal and Active Call unchanged. New input-timing repair 302267b remains simulator-only pending full CI. No real call or handset qualification.'
r=urllib.request.Request('http://127.0.0.1:8792/api/logs/telephony-latency',data=text.encode(),headers={'Authorization':'Bearer '+token,'Content-Type':'text/plain; charset=utf-8'},method='POST')
with urllib.request.urlopen(r,timeout=15) as q:assert q.status==200
print(json.dumps({'published_issue':'ISS-092','deployment':'f12b6f5','desk_updated':True}))
PYJOB
