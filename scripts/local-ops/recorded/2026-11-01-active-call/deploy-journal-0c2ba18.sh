#timeout 300
#pull /home/chuanming/golive-telephony/codex-latency/deployment-journal-0c2ba18.json deployment-journal-0c2ba18.json
set -eu
python3 -B - <<'JOURNAL'
import fcntl,hashlib,json,os,re,shlex,shutil,signal,subprocess,time,urllib.request
from pathlib import Path
os.umask(0o077)
root=Path.home()/'golive-telephony'; target=root/'bin/journal-linux-amd64'
source=Path.home()/'golive/journal-0c2ba18-linux-amd64'
release=Path.home()/'golive-releases/journal-0c2ba18'
receipt=root/'codex-latency/deployment-journal-0c2ba18.json'
OLD='4b098f57749623de459dd99cd3abb9f0c72e367fec6432eac30bbb7a05231cc1'
NEW='7dabde9169796c502340a66502c11192b0ec95154ab2ea0f23a04d72f1e9c700'
def require(ok,msg):
 if not ok:raise RuntimeError(msg)
def digest(p):
 h=hashlib.sha256()
 with p.open('rb') as f:
  for chunk in iter(lambda:f.read(1048576),b''):h.update(chunk)
 return h.hexdigest()
def get(url):
 with urllib.request.urlopen(url,timeout=10) as r:return json.load(r)
def identity(pid):return (str(pid),(Path('/proc')/str(pid)/'stat').read_text().rsplit(')',1)[1].split()[19])
def alive(ident):
 try:return identity(ident[0])==ident
 except FileNotFoundError:return False

def listener(port):
 rows=subprocess.check_output(['ss','-lntpH',f'sport = :{port}'],text=True).splitlines()
 ids=set()
 for row in rows:
  found=set(re.findall(r'pid=(\d+)',row));require(bool(found),'ambiguous listener');ids.update(found)
 require(len(ids)==1,'expected one listener');return ids.pop()
def stamp(port):
 pid=listener(port);p=Path('/proc')/pid
 return {'identity':identity(pid),'exe':digest(p/'exe'),'args':digest(p/'cmdline'),'env':digest(p/'environ')}
def docker_state():
 ids=subprocess.check_output(['docker','ps','-aq'],text=True).split()
 return sorted((d['Id'],d['State']['StartedAt'],d['RestartCount'],d['State']['Status']) for d in json.loads(subprocess.check_output(['docker','inspect',*ids],text=True)))
def idle():
 for port in (8765,9080):
  h=get(f'http://127.0.0.1:{port}/healthz');require(h.get('status')=='ok' and h.get('version')=='45b5564' and h.get('sessions')==0,'engine not idle at 45b5564')
def save():
 tmp=receipt.with_suffix('.tmp');tmp.write_text(json.dumps(state,indent=2)+'\n');os.replace(tmp,receipt)
def install(src):
 tmp=target.with_suffix('.codex-journal-new');shutil.copyfile(src,tmp);tmp.chmod(0o755);os.replace(tmp,target)
def stop(ident):
 require(alive(ident),'journal process changed before stop');os.kill(int(ident[0]),signal.SIGTERM)
 until=time.monotonic()+20
 while alive(ident) and time.monotonic()<until:time.sleep(.2)
 require(not alive(ident),'journal did not exit; no forced kill or replacement')
def launch():
 with (root/'journal.log').open('ab',buffering=0) as out:
  child=subprocess.Popen(args,cwd=cwd,env=env,stdin=subprocess.DEVNULL,stdout=out,stderr=out,start_new_session=True)
 pidfile.write_text(str(child.pid)+'\n');return identity(child.pid)
def await_ready(ident,wanted):
 until=time.monotonic()+90
 while time.monotonic()<until:
  require(alive(ident),'Journal exited')
  try:
   d=get('http://192.168.71.9:8790/api/overview')
   require(identity(listener(8790))==ident,'another Journal owns listener')
   p=Path('/proc')/ident[0]
   require(digest(p/'exe')==wanted and digest(target)==wanted,'Journal binary mismatch')
   require((p/'cmdline').read_bytes()==argvbytes and (p/'environ').read_bytes()==envbytes and (p/'cwd').resolve()==cwd,'Journal launch context changed')
   require(d.get('recording') and d.get('retention_hours')==8760 and d.get('target_ms')==1200,'Journal recording/retention changed')
   return
  except (OSError,ValueError):time.sleep(1)
 raise RuntimeError('Journal readiness timeout')
lock=(root/'.codex-deploy.lock').open('r');fcntl.flock(lock,fcntl.LOCK_EX|fcntl.LOCK_NB)
require(not receipt.exists() and not release.exists(),'attempt already exists; inspect, never replay')
ci=json.loads((Path.home()/'golive/journal-0c2ba18-ci.json').read_text())
require(ci['headSha']=='0c2ba1804e8bbd790883a427732412c9efb0390d' and ci['status']=='completed' and ci['conclusion']=='success','CI not passed')
require({j['name'] for j in ci['jobs']}=={'quality','Go tests (normal)','Go tests (race)'} and all(j['conclusion']=='success' for j in ci['jobs']),'required CI jobs not passed')
require(digest(source)==NEW and digest(target)==OLD,'binary input changed');idle()
pidfile=root/'.phone-brain.journal.pid';pid=listener(8790);require(pidfile.read_text().strip()==pid,'pidfile mismatch')
require(identity(pid)==('1287800','183823397'),'Journal changed since reviewed rollout')
p=Path('/proc')/pid; oldident=identity(pid)
require(digest(p/'exe')==OLD,'running Journal changed')
argvbytes=(p/'cmdline').read_bytes();envbytes=(p/'environ').read_bytes()
args=[os.fsdecode(x) for x in argvbytes.split(b'\0') if x];env=dict(os.fsdecode(x).split('=',1) for x in envbytes.split(b'\0') if b'=' in x);cwd=(p/'cwd').resolve()
require((cwd/args[0]).resolve()==target and cwd==root,'unexpected Journal executable/cwd')
require((p/'fd/1').resolve()==root/'journal.log' and (p/'fd/2').resolve()==root/'journal.log','unexpected log target')
ports=(8765,9080,8791,8792);protected={port:stamp(port) for port in ports};containers=docker_state()
paths=[root/'configs/telephony.json',Path.home()/'golive-last-call.json',root/'scripts/phone-brain.sh',root/'VERSION']
configs={f:digest(f) for f in paths}
archives={}
for m in (Path.home()/'golive-archive/calls').glob('*/*/*/manifest.json'):
 d=m.parent; manifest=json.loads(m.read_text());raw={}
 for name in ('audio.wav','transcript.txt','session.json','golive.log'):
  f=d/name
  if f.exists():
   require(not f.is_symlink(),'linked raw archive');raw[name]=digest(f)
   require(manifest['files'][name]['sha256']==raw[name] and manifest['files'][name]['bytes']==f.stat().st_size,'preexisting archive integrity failure')
 c=d/'call.json';require(c.is_file(),'missing call metadata')
 archives[d]={'raw':raw,'call':json.loads(c.read_text()),'bytes':c.read_bytes()}
require(len(archives)>=146,'archive inventory unexpectedly small')
release.mkdir();shutil.copy2(target,release/'previous-journal-linux-amd64');shutil.copy2(source,release/'candidate-journal-linux-amd64')
state={'stage':'prepared','commit':ci['headSha'],'engine_unchanged':'45b5564','old_sha256':OLD,'new_sha256':NEW,'archive_count':len(archives),'ci_run':ci['databaseId'],'release':str(release)};save()
current=oldident;stopped=False
try:
 idle();require(stamp(8790)['identity']==oldident,'Journal restarted before stop')
 state['stage']='stopping_journal';save();stop(current);stopped=True
 state['stage']='starting_journal';save();install(source);current=launch();stopped=False;await_ready(current,NEW)
 call=get('http://192.168.71.9:8790/api/calls/out-mvxksnvy-4i3q')
 kept=json.loads((Path.home()/'golive-last-call.json').read_text())
 require(call['call'].get('greeting')==kept.get('greeting'),'initial greeting not restored')
 changed=0;rawcount=0
 for d,before in archives.items():
  m=json.loads((d/'manifest.json').read_text())
  for name,sha in before['raw'].items():
   f=d/name;require(digest(f)==sha and m['files'][name]['sha256']==sha and m['files'][name]['bytes']==f.stat().st_size,'raw archive changed');rawcount+=1
  f=d/'call.json';after=json.loads(f.read_text()); require(digest(f)==m['files']['call.json']['sha256'] and f.stat().st_size==m['files']['call.json']['bytes'],'summary manifest mismatch')
  if before['call']!=after:
   old=before['call'].copy();new=after.copy();old.pop('greeting',None);new.pop('greeting',None);require(old==new,'metadata changed beyond greeting')
   initial=None
   for line in (d/'golive.log').read_text().splitlines():
    try:r=json.loads(line)
    except ValueError:
     try:r=dict(x.split('=',1) for x in shlex.split(line) if '=' in x)
     except ValueError:continue
    if r.get('session')==after['session'] and r.get('msg')=='speak: opening line' and r.get('text'):
     initial=r['text'];break
   require(initial and after.get('greeting')==initial,'corrected greeting lacks canonical proof');changed+=1
 require(changed>=1,'target summary was not corrected')
 require({port:stamp(port) for port in ports}==protected and docker_state()==containers,'independent services changed')
 require(all(digest(f)==sha for f,sha in configs.items()),'original configuration changed');idle()
 state.update(stage='verified',pid_identity=list(current),raw_archive_files_unchanged=rawcount,greeting_summaries_corrected=changed,configuration_preserved=True,other_services_preserved=True);save();print(json.dumps(state))
except Exception as error:
 state['failure_type']=type(error).__name__;state['failure']=str(error) if isinstance(error,RuntimeError) else 'private diagnostic retained';state['stage']='rollback_requested';save()
 try:
  idle()
  if alive(current):stop(current)
  install(release/'previous-journal-linux-amd64');current=launch();await_ready(current,OLD)
  state['stage']='rolled_back';save()
 except Exception as rollback:
  state['stage']='rollback_needs_attention';state['rollback_failure_type']=type(rollback).__name__;save()
 print(json.dumps(state));raise SystemExit(1)

JOURNAL
