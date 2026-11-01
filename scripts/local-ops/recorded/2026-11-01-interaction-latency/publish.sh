#timeout 60
#push interaction-latency-retro-20261101.md /tmp/interaction-latency-retro-20261101.md
set -eu
python3 - <<'PYJOB'
from pathlib import Path
import hashlib,json,urllib.request
raw=Path('/tmp/interaction-latency-retro-20261101.md').read_bytes()
assert hashlib.sha256(raw).hexdigest()=='1d543e2e6fe57782cce698bb3e636e1a3aea74b8be79d25f74214c5e3859db3a'
cid='sim-interaction-ab988aa8-b778-4a5d-aab2-b51464cc2768'
roots=[Path('/home/chuanming/golive-telephony/journal'),Path('/home/chuanming/golive-sip-ab/tool-ack-focused-20261101/state/notes')]
for root in roots:assert not (root/'retros'/(cid+'.md')).exists(),'retrospective exists; do not replay'
for root in roots:
 with (root/'retros'/(cid+'.md')).open('xb') as out:out.write(raw)
with urllib.request.urlopen('http://192.168.71.9:9916/api/calls/'+cid,timeout=15) as q:detail=json.load(q)
assert detail.get('retrospective')
token=Path('/home/chuanming/golive-desk/codex.token').read_text().strip()
body='Owner prioritizes non-calculation latency; original quote delay preserved. Recorded 7fc4237 interaction simulation passes separate server SLA: conversation p95 1087 ms n=25, estimated 1137 ms with assumed 50 ms overhead. Receiver first-audio p95 1193 ms n=20, max 1257 ms: narrow margin, different boundary. Opening answer-to-first-RTP 0.971 ms. Scripted interruption completed; acoustic stop latency not established. Quote was prepared (761 ms), so slow-calculation overlap not established. Five archive files, chat and both audio directions verified; no receiver warnings. Original configs and Active Call preserved; no real handset measurement.'
r=urllib.request.Request('http://127.0.0.1:8792/api/logs/telephony-latency',data=body.encode(),headers={'Authorization':'Bearer '+token,'Content-Type':'text/plain; charset=utf-8'},method='POST')
with urllib.request.urlopen(r,timeout=15) as q:assert q.status==200
print(json.dumps({'retrospective':cid,'desk_updated':True}))
PYJOB
