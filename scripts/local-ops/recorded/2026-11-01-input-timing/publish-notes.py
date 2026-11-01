"""Publish reviewed simulator retrospective and ISS-093 without replacing other notes."""
from pathlib import Path
import hashlib,json,re,urllib.request
payload=json.loads(Path('/tmp/input-timing-notes-20261101.json').read_text())
assert payload['issue']['id']=='ISS-093' and len(payload['retros'])==1
roots={
 '/home/chuanming/golive-telephony/journal':'83d796f5b595a98516bf7dd0515b737da9ab9bbd54b556cb2895f6f97004dd77',
 '/home/chuanming/golive-sip-ab/tool-ack-focused-20261101/state/notes':'358221d3d7060f85592f213cdeded2b48c193d57473935c80d2492bb19dc248e',
}
pending=[]
for root,expected in roots.items():
 root=Path(root);file=root/'issues.json';raw=file.read_bytes()
 assert hashlib.sha256(raw).hexdigest()==expected,'notes changed since review'
 issues=json.loads(raw);matches=[n for n,i in enumerate(issues['issues']) if i['id']=='ISS-093']
 assert len(matches)<=1
 if matches:issues['issues'][matches[0]]=payload['issue']
 else:issues['issues'].append(payload['issue'])
 for name,text in payload['retros'].items():
  assert re.fullmatch(r'sim-inputtiming-[a-f0-9-]+\.md',name)
  assert not (root/'retros'/name).exists(),'retrospective already exists'
 pending.append((root,file,issues))
for root,file,issues in pending:
 for name,text in payload['retros'].items():(root/'retros'/name).write_text(text)
 tmp=file.with_suffix('.input-timing.tmp');tmp.write_text(json.dumps(issues,ensure_ascii=False,indent=1)+'\n');tmp.replace(file)
 assert json.loads(file.read_text())==issues
print(json.dumps({'published':list(roots),'issue':'ISS-093','retros':list(payload['retros'])}))
token=Path('/home/chuanming/golive-desk/codex.token').read_text().strip()
text='ISS-093 302267b: caller-input provenance validates four result links, including silent callers, in sim-inputtiming-96bb8a33-8c9b-4bb5-ac65-69dd84ed95af. Journal/analyzer numeric parity verified. Two chained tasks still lack caller lineage and remain unverified; two quote readouts missing. Conversation p95 1178 ms n=6; gate FAIL. Five archive files, chat and both audio channels verified; no receiver warnings. Original quote delay/configs and Active Call preserved. No handset qualification or production promotion.'
req=urllib.request.Request('http://127.0.0.1:8792/api/logs/telephony-latency',data=text.encode(),headers={'Authorization':'Bearer '+token,'Content-Type':'text/plain; charset=utf-8'},method='POST')
with urllib.request.urlopen(req,timeout=15) as r:assert r.status==200
