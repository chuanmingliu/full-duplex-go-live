#timeout 60
#push focus-followup-retro-20261101.md /tmp/focus-followup-retro-20261101.md
#push focus-followup-issue-20261101.json /tmp/focus-followup-issue-20261101.json
set -eu
python3 - <<'PY'
from pathlib import Path
import json,hashlib,urllib.request
cid='280fdab1-4078-4627-bf79-4eef8f627719'
raw=Path('/tmp/focus-followup-retro-20261101.md').read_bytes()
assert hashlib.sha256(raw).hexdigest()=='da75f877f0327c31879a1e354a565411ec3f668158fb73529f9de1ce2b25d38f'
b=Path('/tmp/focus-followup-issue-20261101.json').read_bytes()
assert hashlib.sha256(b).hexdigest()=='73859d7a394ebf1b9aaa43cd785e9d34c00fc7d931b76ae2787f6a2bce9f120e'
issue=json.loads(b);assert issue['id']=='ISS-095'
roots={'/home/chuanming/golive-telephony/journal':'8aa94e51dc3f86655b95cc1b5737e2950a694f15b08a6757c54b896f7e1aa09e','/home/chuanming/golive-sip-ab/native-real-fb11e2c/state/notes':'2ceb6550f6c63fbad71e504b90a075ab7fba1763fbc3b49dec79594f0a86f2b0'}
pending=[]
for name,sha in roots.items():
 root=Path(name);p=root/'issues.json';old=p.read_bytes();assert hashlib.sha256(old).hexdigest()==sha
 d=json.loads(old);assert not any(x['id']=='ISS-095' for x in d['issues']);d['issues'].append(issue)
 note=root/'retros'/(cid+'.md');assert hashlib.sha256(note.read_bytes()).hexdigest()=='13b11aa0c7513a468222829bc68ad5e45cf111bce7f4c7c75b1c908427f06984'
 pending.extend([(p,(json.dumps(d,ensure_ascii=False,indent=1)+'\n').encode()),(note,raw)])
for p,data in pending:
 tmp=p.with_suffix(p.suffix+'.focus-followup.tmp');tmp.write_bytes(data);tmp.replace(p)
with urllib.request.urlopen('http://192.168.71.9:9896/api/calls/'+cid,timeout=15) as response:assert 'ISS-095' in json.load(response)['retrospective']['text']
token=Path('/home/chuanming/golive-desk/codex.token').read_text().strip()
body='ISS-095: completed offline speaker-window replay, 72 conditions on last authorized call. Shortening 800 ms to 600 ms advances the slowest simplified acceptance by 0/200/0 ms across alignment checks; 400 ms provides no gain there. Approximate input alignment and absent negative-control truth prevent a safe configuration change. Four focused duplex regressions pass in 31.499 s. Original speaker guards, original GoLive, quote delay and Active Call preserved. No real call or runtime deployment during probe. Actual handset capture and >=20 normal responses remain outstanding.'
r=urllib.request.Request('http://127.0.0.1:8792/api/logs/telephony-latency',data=body.encode(),headers={'Authorization':'Bearer '+token,'Content-Type':'text/plain; charset=utf-8'},method='POST')
with urllib.request.urlopen(r,timeout=15) as response:assert response.status==200
print(json.dumps({'issue':'ISS-095','retrospective_updated':cid,'desk_updated':True}))
PY
