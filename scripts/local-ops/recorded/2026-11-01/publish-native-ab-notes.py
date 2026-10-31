from pathlib import Path
import json,hashlib,re
r=Path('/home/chuanming/golive-sip-ab/native-ab-20261031')
payload=r/'report-payload';base=Path('/home/chuanming/golive-telephony')
m=json.loads((payload/'manifest.json').read_text())
for name,sha in m.items():
 p=payload/name;assert p.is_relative_to(payload);assert hashlib.sha256(p.read_bytes()).hexdigest()==sha
notes=list((payload/'retros').glob('*.md'));assert len(notes)==13
for p in notes:
 assert re.fullmatch(r'[A-Za-z0-9_-]+\.md',p.name)
 dst=base/'journal/retros'/p.name
 if dst.exists():assert dst.read_bytes()==p.read_bytes(),'existing note differs'
 else:dst.write_bytes(p.read_bytes())
p=base/'journal/issues.json';d=json.loads(p.read_text());byid={x['id']:x for x in d['issues']}
for x in json.loads((payload/'issues-additions.json').read_text()):
 assert x['id'] in ['ISS-088','ISS-089']
 if x['id'] in byid:assert x==byid[x['id']],'existing issue differs'
 else:d['issues'].append(x)
tmp=p.with_name(p.name+'.native-ab.tmp');tmp.write_text(json.dumps(d,ensure_ascii=False,indent=1)+'\n');tmp.replace(p)
dst=base/'docs/telephony/2026-11-01-native-sip-comparison.md';dst.parent.mkdir(parents=True,exist_ok=True)
if dst.exists():assert dst.read_bytes()==(payload/'report.md').read_bytes(),'existing report differs'
else:dst.write_bytes((payload/'report.md').read_bytes())
print(json.dumps(dict(notes=len(notes),issues=['ISS-088','ISS-089'],report=str(dst))))
