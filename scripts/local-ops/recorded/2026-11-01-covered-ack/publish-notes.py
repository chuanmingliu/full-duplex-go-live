"""Publish reviewed simulator retrospective and ISS-092 without replacing other notes."""
from pathlib import Path
import hashlib,json,re,urllib.request
payload=json.loads(Path('/tmp/covered-ack-notes-20261101.json').read_text())
assert payload['issue']['id']=='ISS-092' and len(payload['retros'])==1
roots={
 '/home/chuanming/golive-telephony/journal':'99f917637b8f452e78f0f2e0ee39648693769b068c1d3919ad6960218e100246',
 '/home/chuanming/golive-sip-ab/tool-ack-focused-20261101/state/notes':'b2a2ceaf94dccc96652aec210adc99b1368c1d05064aaff8be596914cc0850f9',
}
pending=[]
for root,expected in roots.items():
 root=Path(root);file=root/'issues.json';raw=file.read_bytes()
 assert hashlib.sha256(raw).hexdigest()==expected,'notes changed since review'
 issues=json.loads(raw);matches=[n for n,i in enumerate(issues['issues']) if i['id']=='ISS-092']
 assert len(matches)<=1
 if matches:issues['issues'][matches[0]]=payload['issue']
 else:issues['issues'].append(payload['issue'])
 for name,text in payload['retros'].items():
  assert re.fullmatch(r'sim-coveredack-[a-f0-9-]+\.md',name)
  assert not (root/'retros'/name).exists(),'retrospective already exists'
 pending.append((root,file,issues))
for root,file,issues in pending:
 for name,text in payload['retros'].items():(root/'retros'/name).write_text(text)
 tmp=file.with_suffix('.covered-ack.tmp');tmp.write_text(json.dumps(issues,ensure_ascii=False,indent=1)+'\n');tmp.replace(file)
 assert json.loads(file.read_text())==issues
print(json.dumps({'published':list(roots),'issue':'ISS-092','retros':list(payload['retros'])}))
token=Path('/home/chuanming/golive-desk/codex.token').read_text().strip()
text='ISS-092 f12b6f5: controlled scheduler ready-to-delivery 3010 to 1504 ms; recorded SIP call sim-coveredack-8d3132ed-a69c-4e8c-bbc4-3a10ce5e8fbe archived with both audio channels/chat and zero receiver warnings. New retirement branch not exercised in SIP trial. Conversational content p95 1105 ms n=4; separate quote SLA gate FAIL: six unverified links and insufficient samples. Original quotation delay/configs and Active Call preserved. No handset qualification or production promotion.'
req=urllib.request.Request('http://127.0.0.1:8792/api/logs/telephony-latency',data=text.encode(),headers={'Authorization':'Bearer '+token,'Content-Type':'text/plain; charset=utf-8'},method='POST')
with urllib.request.urlopen(req,timeout=15) as r:assert r.status==200
