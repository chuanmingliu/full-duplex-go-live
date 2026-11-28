from pathlib import Path
import hashlib,json,os,re,subprocess,time,urllib.request,signal
os.umask(0o077)
root=Path.home()/'golive-telephony'
out=root/'codex-latency/preset-focus-ab-20261101'
assert not out.exists(),'attempt exists; inspect before restarting'
def get(url):
 with urllib.request.urlopen(url,timeout=6) as r:return json.load(r)
def sha(p):return hashlib.sha256(Path(p).read_bytes()).hexdigest()
def owner(port):
 ids=set(re.findall(r'pid=(\d+)',subprocess.check_output(['ss','-lntpH','sport = :'+str(port)],text=True)))
 assert len(ids)==1,'listener ambiguous'
 return Path('/proc')/ids.pop()
p=owner(8765)
protected={x:sha(x) for x in [root/'configs/telephony.json',Path.home()/'golive-last-call.json',p/'exe',p/'cmdline',p/'environ']}
assert protected[root/'configs/telephony.json']=='072ab8dbda962eff682fa56fcd049f70cd3717530e64a06bec97e66618878e7d'
assert get('http://127.0.0.1:8765/healthz').get('sessions')==0
assert get('http://127.0.0.1:8765/healthz').get('version')=='f1fd9a6'
binding=get('http://127.0.0.1:3000/api/realtime/brain')
assert binding['endpoint']=='ws://127.0.0.1:8765/v1/realtime' and binding['revision']==13
assert not subprocess.check_output(['ss','-lntH','sport = :18765 or sport = :18766'],text=True).strip()
out.mkdir(mode=0o700)
(out/'baseline').write_bytes((p/'exe').read_bytes());(out/'baseline').chmod(0o700)
env={k.decode():v.decode() for k,v in (piece.split(b'=',1) for piece in (p/'environ').read_bytes().split(b'\0') if b'=' in piece)}
args=[a.decode() for a in (p/'cmdline').read_bytes().split(b'\0') if a]
envfile=args[args.index('-env')+1] if '-env' in args else '.env.local'
if not Path(envfile).is_absolute():envfile=str(root/envfile)
# Match config.LoadDotEnv without shell expansion or logging credentials.
for line in Path(envfile).read_text().splitlines():
 line=line.strip()
 if not line or line.startswith('#'):continue
 line=line.removeprefix('export ')
 k,sep,v=line.partition('=')
 if not sep:continue
 k=k.strip();v=v.strip()
 if len(v)>=2 and v[0] in ('"', "'") and v[-1]==v[0]:v=v[1:-1]
 if k and k not in env:env[k]=v
saved=json.loads((Path.home()/'golive-last-call.json').read_text())
original=json.loads((root/'configs/telephony.json').read_text())
assert not original['duplex'].get('talker_opener_url')
assert original['client_rate']==16000 and original['realtime']['sample_rate']==16000
voice=saved['voice']
builtin=set('marin cedar quartz ripple vesper willow stone gleam meridian bossa tempo beacon delta cinder alloy ash ballad coral echo sage shimmer verse fable onyx nova'.split())
voice=original.get('voice_aliases',{}).get(voice,env.get('GOLIVE_VOICE',original.get('voice','')) if voice in builtin else voice)

processes=[]; logs=[]
state={'kind':'isolated_realtime_loopback_simulation','active_call_transport_included':False,'stage':'starting','source_build':'f1fd9a6','candidate_build':'411b6d0-focus-wake-sim','original_config_sha256':protected[root/'configs/telephony.json'],'saved_context_sha256':{k:hashlib.sha256(saved.get(k,'').encode()).hexdigest() for k in ('instructions','greeting','voice')}}
def save(): (out/'summary.json').write_text(json.dumps(state,indent=2)+'\n')
save()
try:
 for mode,port,binary in [('preset',18765,out/'baseline'),('candidate',18766,Path.home()/'golive/golive-preset-focus-candidate')]:
  cfg=json.loads(json.dumps(original))
  profile=out/(mode+'.json');profile.write_text(json.dumps(cfg))
  ce=dict(env,GOLIVE_ADDR='127.0.0.1:'+str(port),GOLIVE_LOG_FORMAT='text',GOLIVE_LOG_LEVEL='debug',GOLIVE_STORE_DIR=str(out/(mode+'-calls')),GOLIVE_LAST_CALL_FILE=str(out/(mode+'-last-call.json')),GOLIVE_INSTRUCTIONS=saved['instructions'],GOLIVE_VOICE=voice,GOLIVE_GREETING=saved['greeting'])
  if not voice:ce.pop('GOLIVE_VOICE',None)
  log=(out/(mode+'.log')).open('wb');logs.append(log)
  proc=subprocess.Popen([str(binary),'-profile',str(profile),'-env',envfile,'-addr','127.0.0.1:'+str(port)],cwd=root,env=ce,stdout=log,stderr=subprocess.STDOUT,start_new_session=True);processes.append(proc)
  until=time.monotonic()+310
  while True:
   assert proc.poll() is None,'isolated process exited'
   txt=(out/(mode+'.log')).read_text(errors='replace')
   if 'msg="phrase cache warmed"' in txt and 'msg="openers by scenario cached"' in txt:break
   assert time.monotonic()<until,'isolated warming timeout'
   time.sleep(2)
  assert get('http://127.0.0.1:'+str(port)+'/healthz')['sessions']==0
  state['stage']=mode+'_warmed';save();print(json.dumps({'stage':state['stage']}),flush=True)
 bench=Path.home()/'golive/golive-talker-bench-d'
 # Synthetic fixture speech only; no real recipient or SIP dial.
 synthetic='车损险包含什么？|理赔需要哪些手续？|电子保单怎么查看？|保险什么时候生效？|你们的营业时间是什么时候？'
 with (out/'benchmark-private.txt').open('wb') as f:
  gen=subprocess.run([str(bench),'-a','clips=ws://127.0.0.1:18765/v1/live','-speak',synthetic,'-keep-clips',str(out/'clips'),'-rate','16000','-runs','0','-warmup','0'],env=env,cwd=root,stdout=f,stderr=subprocess.STDOUT,timeout=150)
  assert gen.returncode==0,'fixture synthesis failed'
  state['stage']='comparing';save();print(json.dumps({'stage':state['stage']}),flush=True)
  compared=subprocess.run([str(bench),'-a','preset=ws://127.0.0.1:18765/v1/realtime?hold=0','-a-kind','realtime','-b','candidate=ws://127.0.0.1:18766/v1/realtime?hold=0','-b-kind','realtime','-clips',str(out/'clips/*.wav'),'-rate','16000','-call-ids','-runs','20','-warmup','2','-timeout-s','12','-json',str(out/'benchmark-private.json')],cwd=root,env=env,stdout=f,stderr=subprocess.STDOUT,timeout=1000)
  assert compared.returncode==0,'comparison failed'
  rows=json.loads((out/'benchmark-private.json').read_text())
  assert all(not r.get('error') for r in rows),'one or more benchmark sessions failed'
  assert sum(bool(r.get('measured')) for r in rows)==40,'wrong sample population'
 state['stage']='completed'
except Exception as error:
 state['stage']='failed';state['error']=str(error) if isinstance(error,AssertionError) else type(error).__name__
finally:
 for proc in processes:
  if proc.poll() is None:proc.terminate()
 for proc in processes:
  try:proc.wait(timeout=20)
  except subprocess.TimeoutExpired:proc.kill();proc.wait(timeout=5)
 for log in logs:log.close()
 state['original_config_and_runtime_preserved']=all(sha(k)==v for k,v in protected.items())
 state['aria_route_preserved']=get('http://127.0.0.1:3000/api/realtime/brain')==binding
 state['isolated_processes_stopped']=all(proc.poll() is not None for proc in processes)
 save();print(json.dumps(state),flush=True)
 if state['stage']!='completed':raise SystemExit(1)
