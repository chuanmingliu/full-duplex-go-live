#timeout 120
#push codex-notes-36e638e.tar.gz /home/chuanming/golive/codex-notes-36e638e.tar.gz
#pull /home/chuanming/golive-telephony/codex-latency/notes-36e638e/attempt.json codex-notes-36e638e-attempt.json
set -eu
cd "$HOME/golive-telephony"
python3 -B - <<'PY'
from pathlib import Path
import fcntl,hashlib,json,os,re,shutil,tarfile,urllib.request
os.umask(0o077)
root=Path.cwd();state_dir=root/'codex-latency/notes-36e638e'
def require(ok,message):
 if not ok:raise RuntimeError(message)
def digest(p):return hashlib.sha256(p.read_bytes()).hexdigest()
def get(url):
 with urllib.request.urlopen(url,timeout=20) as r:return json.load(r)
OLD={'journal/issues.json': '188faf99a11024063cafe8dc799eed4945e6e2cfd9bc0d4fb564b3529563c3b3', 'journal/retros/out-mvvxxkse-wiqh.md': 'c006deac08bc50486fad1359b16ada74cb89bc835e2166228049607dfb68c617', 'journal/retros/out-mvw03ge0-j0y3.md': '5e634018dcd37d9ce76efbef3ca49d097da348ea44d551b357e3c0357f3a9851'}
NEW={'journal/issues.json': 'dfc35c014c6f981d8e891f06b773aa0a1da48088115de7ce3ff292e7a6857b2a', 'journal/retros/out-mvvxxkse-wiqh.md': '2f38dd8bfad33f3e9b019ea2f9b7163aa61f8f723d53b88032c0ce844837d309', 'journal/retros/out-mvw03ge0-j0y3.md': '3971568bfed686fe5dbebcd0c345ecd52289cd5f3745bba1a8305f043442b01f'}
lock=(root/'.codex-deploy.lock').open('a');fcntl.flock(lock,fcntl.LOCK_EX|fcntl.LOCK_NB)
require(not state_dir.exists(),'notes attempt exists; inspect instead of retrying')
require(get('http://127.0.0.1:8765/healthz')['version']=='9547d22','runtime changed')
require(digest(root/'codex-latency/verify-opener-release-20261031-5-9547d22.json')=='99d693f5b047a548972fa1ea5167ca1d489dbfb87bfe102258378c29d4897972','deployment verification changed')
bundle=Path.home()/'golive/codex-notes-36e638e.tar.gz'
require(digest(bundle)=='5cfa53e0ea850aff54ecc6751439abec71ea9520550b11ef74943ca778c142df','notes bundle differs')
with tarfile.open(bundle) as f:
 members=f.getmembers();require(len(members)==3 and all(x.isfile() for x in members) and {x.name for x in members}==set(NEW),'notes allowlist differs')
 data={x.name:f.extractfile(x).read() for x in members}
 require(all(hashlib.sha256(data[n]).hexdigest()==NEW[n] for n in NEW),'notes content differs')
require(all((root/n).is_file() and not (root/n).is_symlink() and digest(root/n)==OLD[n] for n in OLD),'published notes changed')
state_dir.mkdir();back=state_dir/'previous';back.mkdir()
state={'stage':'prepared','runtime':'9547d22','notes_commit':'36e638e','files':NEW}
def save():
 p=state_dir/'attempt.tmp';p.write_text(json.dumps(state,indent=2)+'\n');os.replace(p,state_dir/'attempt.json')
save();installed=[]
try:
 for n in NEW:
  dest=root/n;backup=back/n;backup.parent.mkdir(parents=True,exist_ok=True);shutil.copy2(dest,backup)
  tmp=dest.with_name(dest.name+'.notes-36e638e');tmp.write_bytes(data[n]);os.replace(tmp,dest);installed.append(n)
 base='http://192.168.71.9:8790'
 require(get(base+'/api/issues')==json.loads(data['journal/issues.json']),'published issues mismatch')
 for call in ('out-mvvxxkse-wiqh','out-mvw03ge0-j0y3'):
  require(get(base+'/api/calls/'+call)['retrospective']['text']==data['journal/retros/'+call+'.md'].decode(),'published retrospective mismatch')
 state['stage']='notes_verified';save()
except Exception:
 for n in installed:
  require(digest(root/n)==NEW[n],'concurrent note change; do not overwrite')
  shutil.copy2(back/n,root/n)
 state['stage']='notes_rolled_back';save();raise
line='entry_id=codex-opener-9547d22-2026-10-31 stage=deployed runtime=9547d22 previous=f842928 CI=quality-normal-race-passed fix=ISS087-started-answer-interruption-accounting archives=16 archive_files=80 recording=enabled retained_hours=720 saved_settings=preserved real_call=out-mvw03ge0-j0y3 call_build=f842928 answers=9 first_audio_p95_ms=380 first_content_p95_ms=1189 strict_gate=insufficient-samples handset_target=unproven exact-interruption-replay=pending architecture_docs=52b5813 notes=36e638e'
line=re.sub(r'[0-9]{7,}','<num>',line)
token=(Path.home()/'golive-desk/codex.token').read_text().strip();require(bool(token),'Desk token absent')
def api(method,suffix='',payload=None):
 req=urllib.request.Request('http://127.0.0.1:8792/api/logs/telephony-latency'+suffix,data=None if payload is None else payload.encode(),headers={'Authorization':'Bearer '+token,'Content-Type':'text/plain; charset=utf-8'},method=method)
 with urllib.request.urlopen(req,timeout=20) as r:return json.load(r)
def rows():
 result=[];after=0
 while True:
  page=api('GET','?after='+str(after)+'&limit=5000');require(page['stream']=='telephony-latency' and page['total']<=50000,'invalid Desk stream')
  part=page.get('lines',[]);result+=part
  if not part or part[-1]['n']>=page['total']:break
  after=part[-1]['n']
 return result
matches=[r for r in rows() if 'entry_id=codex-opener-9547d22-2026-10-31' in r['text'].split()]
require(len(matches)<=1,'duplicate Desk entry')
if matches:
 require(matches[0]['text']==line and matches[0]['who']=='codex','Desk entry conflict');row=matches[0]
else:
 state['stage']='desk_post_requested';save()
 answer=api('POST',payload=line);require(answer.get('added')==1,'Desk append response differs')
 matches=[r for r in rows() if r['text']==line and r['who']=='codex'];require(len(matches)==1,'Desk readback differs');row=matches[0]
state.update(stage='complete',desk_line=row['n']);save()
print(json.dumps(state))
PY
