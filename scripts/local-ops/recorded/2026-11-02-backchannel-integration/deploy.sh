#timeout 650
#pull /home/chuanming/golive-telephony/codex-latency/deployment-backchannel-60dbe53.json deployment-backchannel-60dbe53.json
set -eu
python3 -B - <<'PY'
import fcntl,hashlib,json,os,re,shutil,signal,subprocess,time,urllib.request
from pathlib import Path
os.umask(0o077)
root=Path.home()/'golive-telephony';target=root/'bin/golive-linux-amd64'
source=Path.home()/'golive/golive-60dbe53-linux-amd64'
release=Path.home()/'golive-releases/backchannel-60dbe53'
receipt=root/'codex-latency/deployment-backchannel-60dbe53.json'
OLD='30978356547cce422e0af04363828af9ee7cf2397406d5efc278811c86b32c22'
NEW='f4d36a4814514e5ddcfb405a3af086457c8235d5b8a4d826a9952f186132dd9a'
COMMIT='60dbe53bb7c3ae75b47260cfb5d2d7a07b495c76'
def require(ok,msg):
 if not ok:raise RuntimeError(msg)
def sha(p):
 h=hashlib.sha256()
 with p.open('rb') as f:
  for b in iter(lambda:f.read(1048576),b''):h.update(b)
 return h.hexdigest()
def get(url):
 with urllib.request.urlopen(url,timeout=8) as r:return json.load(r)
def health():return get('http://127.0.0.1:8765/healthz')
def identity(pid):return (str(pid),(Path('/proc')/str(pid)/'stat').read_text().rsplit(')',1)[1].split()[19])
def alive(ident):
 try:return identity(ident[0])==ident
 except FileNotFoundError:return False
def listener(port):
 rows=subprocess.check_output(['ss','-lntpH',f'sport = :{port}'],text=True).splitlines()
 ids=set()
 for row in rows:
  found=set(re.findall(r'pid=(\d+)',row));require(bool(found),'unresolved listener');ids.update(found)
 require(len(ids)==1,'expected exactly one listener');return ids.pop()
def stamp(port):
 pid=listener(port);p=Path('/proc')/pid
 return {'identity':identity(pid),'exe':sha(p/'exe'),'args':sha(p/'cmdline'),'env':sha(p/'environ')}
def containers():
 ids=subprocess.check_output(['docker','ps','-aq'],text=True).split()
 return sorted((d['Id'],d['State']['StartedAt'],d['RestartCount'],d['State']['Status']) for d in json.loads(subprocess.check_output(['docker','inspect',*ids],text=True)))
def idle(version):
 h=health();require(h.get('status')=='ok' and h.get('version')==version and h.get('sessions')==0,'phone service busy or changed')
 saved=json.loads((Path.home()/'golive-last-call.json').read_text());last=get('http://192.168.71.9:8790/api/calls/'+saved['call'])['call']
 require(last.get('ended_at') and not last['ended_at'].startswith('0001-'),'latest call has not ended')
 d=get('http://127.0.0.1:9080/healthz');require(d.get('status')=='ok' and d.get('version')=='45b5564' and d.get('sessions')==0,'demo busy or changed')
def save():
 tmp=receipt.with_suffix('.tmp');tmp.write_text(json.dumps(state,indent=2)+'\n');os.replace(tmp,receipt)
def install(src):
 tmp=target.with_suffix('.interruption-new');shutil.copyfile(src,tmp);tmp.chmod(0o755);os.replace(tmp,target)
def check_supervisor():
 require(alive(supervisor) and sha(superproc/'cmdline')==supercmd and sha(superproc/'environ')==superenv,'supervisor changed')
 require((root/'.phone-brain.pid').read_text().strip()==supervisor[0] and not (root/'.phone-brain.stop').exists(),'supervisor state changed')
def await_ready(previous,version,wanted):
 until=time.monotonic()+40
 while time.monotonic()<until:
  check_supervisor()
  try:
   if alive(previous):time.sleep(.2);continue
   h=health();pid=listener(8765);p=Path('/proc')/pid
   require(h.get('status')=='ok' and h.get('version')==version,'replacement version mismatch')
   require(sha(p/'exe')==wanted and sha(target)==wanted,'replacement binary mismatch')
   require((p/'cmdline').read_bytes()==argv and (p/'environ').read_bytes()==env and (p/'cwd').resolve()==cwd,'launch context changed')
   require((p/'stat').read_text().rsplit(')',1)[1].split()[1]==supervisor[0],'replacement not owned by original supervisor')
   return identity(pid)
  except (OSError,ValueError):time.sleep(.5)
 raise RuntimeError('replacement did not become healthy; inspect before further changes')
lock=(root/'.codex-deploy.lock').open('r');fcntl.flock(lock,fcntl.LOCK_EX|fcntl.LOCK_NB)
require(not release.exists() and not receipt.exists(),'attempt exists; inspect instead of replay')
ci=json.loads((Path.home()/'golive/backchannel-60dbe53-ci.json').read_text())
require(ci['headSha']==COMMIT and ci['status']=='completed' and ci['conclusion']=='success','candidate CI not passed')
require({j['name'] for j in ci['jobs']}=={'quality','Go tests (normal)','Go tests (race)'} and all(j['conclusion']=='success' for j in ci['jobs']),'CI jobs not all passed')
require(sha(source)==NEW and sha(target)==OLD,'binary inputs changed');idle('f1fd9a6')
binding=get('http://127.0.0.1:3000/api/realtime/brain')
require(binding['backend']=='local' and binding['revision']==13 and binding['endpoint']=='ws://127.0.0.1:8765/v1/realtime','Aria binding changed')
restore=root/'codex-latency/aria-binding-20261101'
require(sha(restore/'golive-45b5564-linux-amd64')=='0af8a624053a706bc4f2f20ec2b9f847409eef2e6f502e21520cca60a8ec8a29','requested Aria restore binary absent')
pid=listener(8765);old=identity(pid);p=Path('/proc')/pid
require(old[0]=='1528817' and sha(p/'exe')==OLD,'phone runtime changed')
argv=(p/'cmdline').read_bytes();env=(p/'environ').read_bytes();cwd=(p/'cwd').resolve()
require(cwd==root and (p/'fd/1').resolve()==root/'golive-telephony.log','unexpected launch location')
environment=dict(v.split(b'=',1) for v in env.split(b'\0') if b'=' in v)
require(environment.get(b'GOLIVE_REALTIME_STORE_CALLS')==b'1' and environment.get(b'GOLIVE_STORE_TTL_HOURS')==b'8760','recording config missing')
supervisor=identity((p/'stat').read_text().rsplit(')',1)[1].split()[1]);superproc=Path('/proc')/supervisor[0]
require(b'keepalive' in (superproc/'cmdline').read_bytes(),'not the expected restart supervisor')
supercmd=sha(superproc/'cmdline');superenv=sha(superproc/'environ');check_supervisor()
ports=(9080,8790,8791,8792);protected={port:stamp(port) for port in ports};docker=containers()
configs={f:sha(f) for f in (root/'configs').rglob('*') if f.is_file()}
for f in [root/'scripts/phone-brain.sh',Path.home()/'golive-last-call.json']:configs[f]=sha(f)
require(configs[root/'configs/telephony.json']=='072ab8dbda962eff682fa56fcd049f70cd3717530e64a06bec97e66618878e7d','phone profile changed')
archives={}
for m in (Path.home()/'golive-archive/calls').glob('*/*/*/manifest.json'):
 manifest=json.loads(m.read_text())
 for name,entry in manifest['files'].items():
  f=m.parent/name;require(f.is_file() and not f.is_symlink() and f.stat().st_size==entry['bytes'] and sha(f)==entry['sha256'],'archive integrity failure')
  archives[f]=entry['sha256']
require(len(archives)>650,'archive inventory incomplete')
require(get('http://127.0.0.1:18087/healthz')['status']=='ready','detector not ready')
profile=root/'configs/telephony.json';original_profile=json.loads(profile.read_text())
require(not original_profile['duplex'].get('backchannel_detector_mode'),'unexpected existing detector mode')
new_profile=json.loads(json.dumps(original_profile));new_profile['duplex']['backchannel_detector_url']='ws://127.0.0.1:18087/v1/stream';new_profile['duplex']['backchannel_detector_mode']='asr_assist'
release.mkdir();shutil.copy2(profile,release/'profile.previous.json');shutil.copy2(p/'exe',release/'previous-golive-linux-amd64');shutil.copy2(source,release/'candidate-golive-linux-amd64');shutil.copy2(root/'VERSION',release/'VERSION.previous')
(release/'cmdline.private').write_bytes(argv);(release/'environ.private').write_bytes(env)
state={'stage':'prepared','commit':COMMIT,'old_version':'f1fd9a6','new_version':'60dbe53','old_sha256':OLD,'new_sha256':NEW,'release':str(release),'ci_run':ci['databaseId'],'aria_backend':'local','aria_revision':13,'restore_only_on_owner_request':True};save()
current=old
try:
 idle('f1fd9a6');check_supervisor();require(identity(listener(8765))==old,'phone changed before replacement')
 require(all(sha(f)==value for f,value in configs.items()),'configuration changed before restart')
 offset=(root/'golive-telephony.log').stat().st_size
 tmp=profile.with_suffix('.backchannel-new');tmp.write_text(json.dumps(new_profile,ensure_ascii=False,indent=2)+'\n');os.replace(tmp,profile)
 configs[profile]=sha(profile)
 install(source);(root/'VERSION').write_text(COMMIT+'\n')
 state['stage']='restarting_phone_only';save();os.kill(int(old[0]),signal.SIGTERM)
 current=await_ready(old,'60dbe53',NEW);state['stage']='warming';save()
 until=time.monotonic()+310
 while True:
  require(alive(current) and identity(listener(8765))==current,'candidate restarted during warm-up')
  with (root/'golive-telephony.log').open('rb') as f:f.seek(offset);lines=f.read().decode(errors='replace').splitlines()
  messages=[]
  for line in lines:
   try:messages.append(json.loads(line).get('msg',''))
   except ValueError:
    m=re.search(r'(?:^|\s)msg="([^"\\]*)"(?:\s|$)',line)
    if m:messages.append(m.group(1))
  require(messages.count('providers registered')<=1,'candidate restarted')
  require('phrase cache only partly warmed' not in messages and "the lead service's openers were not cached" not in messages,'warm-up failure')
  if 'phrase cache warmed' in messages and 'openers by scenario cached' in messages:break
  require(time.monotonic()<until,'warm-up timeout');time.sleep(3)
 idle('60dbe53');check_supervisor()
 require(all(sha(f)==value for f,value in configs.items()),'original configuration changed')
 require(all(sha(f)==value for f,value in archives.items()),'existing archive changed')
 current_profile=json.loads(profile.read_text());current_profile['duplex'].pop('backchannel_detector_url');current_profile['duplex'].pop('backchannel_detector_mode')
 require(current_profile==original_profile,'original settings changed')
 require({port:stamp(port) for port in ports}==protected and containers()==docker,'other service changed')
 fresh=get('http://127.0.0.1:3000/api/realtime/brain');require(fresh==binding,'Aria route changed')
 j=get('http://192.168.71.9:8790/api/overview');require(j['recording'] and j['retention_hours']==8760,'Journal recording changed')
 state.update(stage='verified',pid_identity=list(current),phrase_warm_verified=True,archive_files_unchanged=len(archives),original_settings_preserved=True,original_profile_backup=str(release/'profile.previous.json'),trial_profile_sha256=sha(profile),detector_mode='asr_assist',other_services_preserved=True,aria_route_preserved=True);save();print(json.dumps(state))
except Exception as error:
 state.update(stage='failed_requires_inspection',failure_type=type(error).__name__,failure=str(error) if isinstance(error,RuntimeError) else 'private diagnostic retained');save();print(json.dumps(state));raise SystemExit(1)
PY
