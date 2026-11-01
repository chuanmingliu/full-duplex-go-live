#timeout 60
#push native-handset-retro-20261101.md /tmp/native-handset-retro-20261101.md
#push native-handset-issues-20261101.json /tmp/native-handset-issues-20261101.json
#push finalize-native-handset-20261101.py /tmp/finalize-native-handset-20261101.py
set -eu
python3 - <<'PYJOB'
from pathlib import Path
import hashlib,json,subprocess,sys,urllib.request
assert hashlib.sha256(Path('/tmp/native-handset-retro-20261101.md').read_bytes()).hexdigest()=='21efb02b883636daa2530aab7c6eb059e68856b6149e5f287173b326d0e9aef0'
assert hashlib.sha256(Path('/tmp/native-handset-issues-20261101.json').read_bytes()).hexdigest()=='a8ecabda906138468c786c9a4198059a9174dc85b47b0ee6d39961030afada9b'
assert hashlib.sha256(Path('/tmp/finalize-native-handset-20261101.py').read_bytes()).hexdigest()=='64c809bc03ccb7fc795164af29cf4947739c39f27961f2a5195da65d4b13e849'
cid='3af753f8-d623-43ff-a619-6c4a6557cccb'
raw=Path('/tmp/native-handset-retro-20261101.md').read_bytes()
updates=json.loads(Path('/tmp/native-handset-issues-20261101.json').read_text())
pending=[]
for name in ['/home/chuanming/golive-telephony/journal','/home/chuanming/golive-sip-ab/native-real-fb11e2c/state/notes']:
 root=Path(name);p=root/'issues.json';old=p.read_bytes();d=json.loads(old)
 assert not (root/'retros'/(cid+'.md')).exists(),'retrospective exists; inspect, do not replay'
 assert not any(x['id'] in ('ISS-097','ISS-098') for x in d['issues']),'issue collision'
 by={x['id']:x for x in updates};d['issues']=[by.pop(x['id'],x) for x in d['issues']];d['issues'].extend(by.values())
 pending.append((root,p,old,(json.dumps(d,ensure_ascii=False,indent=1)+'\n').encode()))
for root,p,old,new in pending:
 assert p.read_bytes()==old,'concurrent issue update'
 (root/'retros').mkdir(exist_ok=True)
 with (root/'retros'/(cid+'.md')).open('xb') as out:out.write(raw)
 tmp=p.with_suffix('.native-handset.tmp');tmp.write_bytes(new);tmp.replace(p)
subprocess.run([sys.executable,'/tmp/finalize-native-handset-20261101.py'],check=True)
token=Path('/home/chuanming/golive-desk/codex.token').read_text().strip()
body='Completed original GoLive UUID '+cid+'. Normal-content p95 1260 ms n=30, estimated handset 1310 ms with assumed 50 ms overhead; first audio p95 672 ms n=34. Strict gate fails latency and one started-output completion record. Thirteen playback interruptions: detection-to-cancel median 232 ms, p95 2087 ms; focus hold 2040 ms in slowest case, all sixteen adapter clears within about 2 ms. Four prepared quote readouts p95 1089 ms separate; calculation config 2500-4500 ms unchanged. Seven archive files verified, assistant SIP BYE succeeded, exit 0. Handset WAV/hash saved but sample_clock_discontinuity invalidates acoustic qualification. ISS-097 reproduction and receipt fix pass race tests and thirty repetitions; ISS-098 adds bounded future clock diagnostics, handset suite 46 tests passes. Full CI pending before native engine promotion. Original settings and Active Call preserved; no redial.'
req=urllib.request.Request('http://127.0.0.1:8792/api/logs/telephony-latency',data=body.encode(),headers={'Authorization':'Bearer '+token,'Content-Type':'text/plain; charset=utf-8'},method='POST')
with urllib.request.urlopen(req,timeout=15) as response:assert response.status==200
print(json.dumps({'retrospective':cid,'desk_updated':True,'issues_updated':[x['id'] for x in updates]}))
PYJOB
