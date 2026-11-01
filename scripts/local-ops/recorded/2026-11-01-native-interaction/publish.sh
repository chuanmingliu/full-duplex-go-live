#timeout 60
#push native-interaction-retro-20261101.md /tmp/native-interaction-retro-20261101.md
#push finalize-native-interaction-20261101.py /tmp/finalize-native-interaction-20261101.py
set -eu
python3 - <<'PYJOB'
from pathlib import Path
import hashlib,json,subprocess,sys,urllib.request
raw=Path('/tmp/native-interaction-retro-20261101.md').read_bytes()
assert hashlib.sha256(raw).hexdigest()=='13b11aa0c7513a468222829bc68ad5e45cf111bce7f4c7c75b1c908427f06984'
finalizer=Path('/tmp/finalize-native-interaction-20261101.py')
assert hashlib.sha256(finalizer.read_bytes()).hexdigest()=='c106ad20b76a0a9d925e5a6bee402c4e8f85b6722f9c4a1a62b66b0070a530ca'
cid='280fdab1-4078-4627-bf79-4eef8f627719'
roots=[Path('/home/chuanming/golive-telephony/journal'),Path('/home/chuanming/golive-sip-ab/native-real-fb11e2c/state/notes')]
for root in roots:
 assert (root/'retros').is_dir()
 assert not (root/'retros'/(cid+'.md')).exists(),'retrospective exists; do not replay'
for root in roots:
 with (root/'retros'/(cid+'.md')).open('xb') as out:out.write(raw)
subprocess.run([sys.executable,str(finalizer)],check=True)
token=Path('/home/chuanming/golive-desk/codex.token').read_text().strip()
body='Completed original GoLive real call '+cid+'. Normal substantive replies p95 1043 ms n=14; 1093 ms estimated with assumed 50 ms overhead, not acoustic handset proof. First opener 380 ms n=18. Qualification needs 20 normal samples. Seven playback interruptions: detection-to-cancel p50 182 ms, p95 623 ms; all eight generation cancellations reach adapter clear within approximately 2 ms. Slowest focus confirmation adds 600 ms; do not confuse detection or queue clear with acoustic stop. Four quote readouts p95 1084 ms reported separately; prepared task completion 0-2 ms does not measure uncached calculation. Original 2500-4500 ms calculation setting unchanged. Seven archive files and both audio channels verified, assistant SIP BYE successful, exit 0. Original configs and Active Call preserved; no redial.'
req=urllib.request.Request('http://127.0.0.1:8792/api/logs/telephony-latency',data=body.encode(),headers={'Authorization':'Bearer '+token,'Content-Type':'text/plain; charset=utf-8'},method='POST')
with urllib.request.urlopen(req,timeout=15) as response:assert response.status==200
print(json.dumps({'retrospective':cid,'desk_updated':True}))
PYJOB
