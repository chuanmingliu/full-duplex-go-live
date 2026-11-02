from pathlib import Path
import hashlib,json,os,re,subprocess,time,urllib.request,tarfile
os.umask(0o077)
home=Path.home();root=home/'golive-telephony';out=root/'codex-latency/backchannel-matrix-20261102'
assert not out.exists(),'test attempt already exists'
def get(url):
 with urllib.request.urlopen(url,timeout=6) as r:return json.load(r)
def sha(p):return hashlib.sha256(Path(p).read_bytes()).hexdigest()
pids=set(re.findall(r'pid=(\d+)',subprocess.check_output(['ss','-lntpH','sport = :8765'],text=True)))
assert len(pids)==1
procroot=Path('/proc')/pids.pop()
protected={str(p):sha(p) for p in [root/'configs/telephony.json',home/'golive-last-call.json',procroot/'exe',procroot/'cmdline',procroot/'environ']}
assert protected[str(root/'configs/telephony.json')]=='072ab8dbda962eff682fa56fcd049f70cd3717530e64a06bec97e66618878e7d'
assert get('http://127.0.0.1:8765/healthz')['sessions']==0
assert get('http://127.0.0.1:8765/healthz')['version']=='f1fd9a6'
assert get('http://127.0.0.1:18087/healthz')['active_sessions']==0
binding=get('http://127.0.0.1:3000/api/realtime/brain')
assert binding['revision']==13 and binding['endpoint']=='ws://127.0.0.1:8765/v1/realtime'
assert not subprocess.check_output(['ss','-lntH','sport = :18765'],text=True).strip()
source=home/'golive/bcd-matrix-source-20261102.tar.gz'
assert sha(source)=='c437fafef3414af1042c4b50193de72630d30e2325c79f6a3e3caa63ed71fe99'
out.mkdir(mode=0o700);src=out/'source';src.mkdir()
with tarfile.open(source) as tar:
 assert set(tar.getnames())=={'chinese_eval.py', 'probe.py', 'chinese_cases.json', 'chinese_matrix_eval.py', 'test_chinese_eval.py', 'chinese_archive_eval.py', 'test_chinese_matrix.py', 'chinese_matrix.json'}
 assert all(m.isfile() and m.size<50000 for m in tar.getmembers())
 tar.extractall(src,filter='data')
(out/'baseline').write_bytes((procroot/'exe').read_bytes());(out/'baseline').chmod(0o700)
env={k.decode():v.decode() for k,v in (part.split(b'=',1) for part in (procroot/'environ').read_bytes().split(b'\0') if b'=' in part)}
args=[a.decode() for a in (procroot/'cmdline').read_bytes().split(b'\0') if a]
envfile=args[args.index('-env')+1] if '-env' in args else '.env.local'
if not Path(envfile).is_absolute():envfile=str(root/envfile)
for line in Path(envfile).read_text().splitlines():
 line=line.strip()
 if not line or line.startswith('#'):continue
 k,sep,v=line.removeprefix('export ').partition('=');k=k.strip();v=v.strip()
 if len(v)>=2 and v[0] in ('"',"'") and v[-1]==v[0]:v=v[1:-1]
 if sep and k not in env:env[k]=v
saved=json.loads((home/'golive-last-call.json').read_text())
original=json.loads((root/'configs/telephony.json').read_text())
assert not original['duplex'].get('talker_opener_url')
voice=saved['voice'];builtin=set('marin cedar quartz ripple vesper willow stone gleam meridian bossa tempo beacon delta cinder alloy ash ballad coral echo sage shimmer verse fable onyx nova'.split())
voice=original.get('voice_aliases',{}).get(voice,env.get('GOLIVE_VOICE',original.get('voice','')) if voice in builtin else voice)
(out/'profile.json').write_text(json.dumps(original))
state={'protocol_sha256':sha(src/'chinese_matrix.json'),'stage':'starting','production_build':'f1fd9a6','production_config_sha256':protected[str(root/'configs/telephony.json')],'mode':'chinese_synthetic_and_recorded_telephony_replay','real_dials':0,'live_control_actions':0}
def save(): (out/'operation.json').write_text(json.dumps(state,indent=2)+'\n')
save();p=None
try:
 ce=dict(env,GOLIVE_ADDR='127.0.0.1:18765',GOLIVE_LOG_LEVEL='debug',GOLIVE_LOG_FORMAT='text',GOLIVE_STORE_DIR=str(out/'synthesis-calls'),GOLIVE_LAST_CALL_FILE=str(out/'isolated-last-call.json'),GOLIVE_INSTRUCTIONS=saved['instructions'],GOLIVE_VOICE=voice,GOLIVE_GREETING=saved['greeting'])
 with (out/'synthesis-private.log').open('wb') as log:
  p=subprocess.Popen([str(out/'baseline'),'-profile',str(out/'profile.json'),'-env',envfile,'-addr','127.0.0.1:18765'],cwd=root,env=ce,stdout=log,stderr=subprocess.STDOUT,start_new_session=True)
  deadline=time.monotonic()+310
  while True:
   assert p.poll() is None,'isolated engine exited'
   content=(out/'synthesis-private.log').read_text(errors='replace')
   if 'msg="phrase cache warmed"' in content and 'msg="openers by scenario cached"' in content:break
   assert time.monotonic()<deadline,'warming deadline'
   time.sleep(2)
  assert get('http://127.0.0.1:18765/healthz')['sessions']==0
  clips=out/'clips';clips.mkdir()
  for index,case in enumerate(json.loads((src/'chinese_matrix.json').read_text())['cases'],1):
   # Reuse only exact regression WAVs, keeping source hashes for comparison.
   if index<=12:
    old=root/'codex-latency/backchannel-zh-20261102'
    previous=json.loads((old/'source/chinese_cases.json').read_text())[index-1]
    assert previous['id']==case['id'] and previous['text']==case['text']
    (clips/('spoken_%02d.wav'%index)).write_bytes((old/'clips'/('spoken_%02d.wav'%index)).read_bytes())
    continue
   # Each new synthesis has its own directory; no failed-clip renumbering.
   case_dir=out/('synthesis-'+str(index))
   with (out/('fixture-'+str(index)+'-private.log')).open('wb') as f:
    result=subprocess.run([str(home/'golive/golive-talker-bench-d'),'-a','clips=ws://127.0.0.1:18765/v1/live','-speak',case['text'],'-keep-clips',str(case_dir),'-rate','16000','-runs','0','-warmup','0'],env=env,cwd=root,stdout=f,stderr=subprocess.STDOUT,timeout=70)
   assert result.returncode==0 and len(list(case_dir.glob('*.wav')))==1,'fixture synthesis failed: '+case['id']
   (clips/('spoken_%02d.wav'%index)).write_bytes((case_dir/'spoken_01.wav').read_bytes())
   state.update(stage='synthesizing',fixtures=index);save();print(json.dumps({'fixtures':index}),flush=True)
 p.terminate();p.wait(timeout=20);p=None
 state['stage']='evaluating';save();print(json.dumps({'stage':'evaluating'}),flush=True)
 # Select the whole recent eligible population before looking at predictions.
 eligible=[]
 for manifest_path in (home/'golive-archive/calls/2026/11').glob('*/manifest.json'):
  manifest=json.loads(manifest_path.read_text())
  if manifest.get('build')!='f1fd9a6' or not manifest.get('audio'):continue
  call=json.loads((manifest_path.parent/'call.json').read_text())
  if not call.get('ended_at') or call.get('session')!=manifest.get('session'):continue
  eligible.append(manifest_path.parent)
 archives=sorted(eligible,reverse=True)[:8]
 state['selected_archives']=[json.loads((p/'manifest.json').read_text())['id'] for p in archives];save()
 image='golive-backchannel-detector:20261102-4896f47'
 docker=['docker','run','--rm','--network','host','--user',str(os.getuid())+':'+str(os.getgid()),'--read-only','--cap-drop','ALL','--security-opt','no-new-privileges','--cpus','1','--memory','384m','-e','PYTHONDONTWRITEBYTECODE=1','-v',str(src)+':/test:ro','-v',str(out)+':/work']
 for i,directory in enumerate(archives):docker.extend(['-v',str(directory)+':/archive'+str(i)+':ro'])
 with (out/'evaluation-private.log').open('wb') as f:
  tested=subprocess.run(docker+[image,'python','-m','unittest','discover','-s','/test','-p','test_chinese*.py','-v'],stdout=f,stderr=subprocess.STDOUT,timeout=30)
  assert tested.returncode==0,'evaluator unit tests failed'
  cmd=docker+[image,'python','/test/chinese_matrix_eval.py','--clips','/work/clips','--output','/work/results.json']
  for i in range(len(archives)):cmd.extend(['--archive','/archive'+str(i)])
  evaluated=subprocess.run(cmd,stdout=f,stderr=subprocess.STDOUT,timeout=700)
  assert evaluated.returncode==0,'evaluation failed; inspect private log'
 state['stage']='completed'
except Exception as error:
 state['stage']='failed';state['error']=str(error) if isinstance(error,AssertionError) else type(error).__name__
finally:
 if p and p.poll() is None:
  p.terminate()
  try:p.wait(timeout=20)
  except subprocess.TimeoutExpired:p.kill();p.wait(timeout=5)
 state['production_preserved']=all(sha(k)==v for k,v in protected.items())
 state['aria_preserved']=get('http://127.0.0.1:3000/api/realtime/brain')==binding
 state['engine_after']=get('http://127.0.0.1:8765/healthz')
 state['detector_after']=get('http://127.0.0.1:18087/healthz')
 state['isolated_engine_stopped']=p is None or p.poll() is not None
 save();print(json.dumps(state),flush=True)
 if state['stage']!='completed':raise SystemExit(1)
