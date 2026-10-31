import os,json,hashlib,shutil,subprocess,time,signal,urllib.request
from pathlib import Path
os.umask(0o077)
r=Path('/home/chuanming/golive-sip-ab/native-ab-20261031')
assert json.loads((r/'finalization.json').read_text())['test_engine_stopped']
base=Path('/home/chuanming/golive-telephony');archive=Path('/home/chuanming/golive-archive')
pidfile=base/'.phone-brain.journal.pid';pid=int(pidfile.read_text());proc=Path('/proc')/str(pid)
args=[x.decode() for x in (proc/'cmdline').read_bytes().split(b'\0') if x]
assert args[args.index('-archive')+1]==str(archive)
assert args[args.index('-addr')+1]=='192.168.71.9:8790'
assert os.readlink(proc/'cwd')==str(base)
exe=Path(os.readlink(proc/'exe'));assert exe.name=='journal-linux-amd64'
actual=(proc/'exe').read_bytes();assert hashlib.sha256(actual).digest()==hashlib.sha256(exe.read_bytes()).digest()
env=dict(x.decode().split('=',1) for x in (proc/'environ').read_bytes().split(b'\0') if b'=' in x)
h=json.load(urllib.request.urlopen('http://127.0.0.1:8765/healthz'));assert h['version']=='9547d22' and h['sessions']==0
entries=json.loads((r/'archive-verification.json').read_text());assert len(entries)==10
for item in entries:
 src=Path(item['archive']);assert src.is_relative_to(r/'archive/calls')
 dest=archive/src.relative_to(r/'archive');assert not dest.exists(),'existing import; inspect instead'
 m=json.loads((src/'manifest.json').read_text());assert m['id']==item['call']
 for name,info in m['files'].items():
  b=(src/name).read_bytes();assert len(b)==info['bytes'] and hashlib.sha256(b).hexdigest()==info['sha256']
 dest.parent.mkdir(parents=True,exist_ok=True)
 stage=dest.with_name(dest.name+'.importing');shutil.copytree(src,stage);stage.rename(dest)
# Append explicit simulator pairing; never label these archive imports real calls.
pairs=Path('/home/chuanming/golive-sim/data/dials.jsonl')
existing={x['call']:x['run'] for x in (json.loads(l) for l in pairs.read_text().splitlines() if l.strip())}
with pairs.open('a') as f:
 for item in entries:
  if item['call'] in existing:assert existing[item['call']]==item['run'];continue
  f.write(json.dumps({'call':item['call'],'run':item['run'],'source':'native-sip-comparison'})+'\n')
backup=r/'journal-original';backup.write_bytes(actual);backup.chmod(0o700)
# Reload exactly the existing journal executable, arguments, cwd and environment.
os.kill(pid,signal.SIGTERM)
for _ in range(50):
 if not proc.exists() or '\nState:\tZ' in (proc/'status').read_text():break
 time.sleep(.2)
else:raise RuntimeError('old Journal has not stopped; do not start a duplicate')
with (base/'journal.log').open('ab') as log:
 child=subprocess.Popen(args,cwd=base,env=env,stdout=log,stderr=subprocess.STDOUT,start_new_session=True)
pidfile.write_text(str(child.pid)+'\n')
for _ in range(50):
 try:urllib.request.urlopen('http://192.168.71.9:8790/api/overview',timeout=2).close();break
 except OSError:time.sleep(.2)
else:raise RuntimeError('Journal did not become ready')
for item in entries:
 d=json.load(urllib.request.urlopen('http://192.168.71.9:8790/api/calls/'+item['call'],timeout=30))
 assert d['archive']['audio'] and d['archive']['archived'] and d['sim_run']==item['run']
 assert d.get('retrospective'),'missing comparison note'
 with urllib.request.urlopen(urllib.request.Request('http://192.168.71.9:8790/api/calls/'+item['call']+'/audio.wav?raw=1',method='HEAD'),timeout=15) as q:
  assert q.status==200 and int(q.headers['Content-Length'])>44
h=json.load(urllib.request.urlopen('http://127.0.0.1:8765/healthz'));assert h['version']=='9547d22' and h['sessions']==0
out=dict(imported=len(entries),journal_pid=child.pid,journal_binary_unchanged=True,original_engine_version=h['version'],all_audio_available=True,all_notes_available=True,all_labeled_simulated=True)
(r/'journal-import.json').write_text(json.dumps(out,indent=2)+'\n');print(json.dumps(out))
