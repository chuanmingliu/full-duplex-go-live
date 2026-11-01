import os,json,re,time,subprocess,urllib.request,hashlib,math,datetime,shutil,socket,uuid
from pathlib import Path
os.umask(0o077)
root=Path('/home/chuanming/golive-sip-ab/tool-ack-focused-20261101')
real=Path('/home/chuanming/golive-sip-ab/native-real-fb11e2c')
previous=Path('/home/chuanming/golive-sip-ab/native-ab-20261031')
assert not root.exists(),'already attempted; inspect instead of retry'
assert json.loads(Path('/home/chuanming/golive-sip-ab/tool-ack-ab-20261101/attempt.json').read_text()).get('completed_at'),'broad comparison still running'
def envfile(p):
 return {k:v.strip().strip('\"\'') for l in p.read_text().splitlines() if l and not l.startswith('#') and '=' in l for k,v in [l.split('=',1)]}
token=envfile(Path('/home/chuanming/golive-sim/tokens.env'))['SIM_CONTROL_TOKEN']
def request(url,body=None,auth=None,key=None):
 h={'Authorization':'Bearer '+auth} if auth else {}
 if body is not None:h['Content-Type']='application/json'
 if key:h['Idempotency-Key']=key
 q=urllib.request.Request(url,data=json.dumps(body).encode() if body is not None else None,headers=h)
 with urllib.request.urlopen(q,timeout=40) as r:return json.load(r)
def sim(path,body=None,key=None):return request('http://127.0.0.1:18865'+path,body,token,key)
def save(p,d):p.write_text(json.dumps(d,indent=2)+'\n')
def say(d):print(json.dumps(d),flush=True)
def idle():
 for p in [8765,9875,9885,9895]:assert request(f'http://127.0.0.1:{p}/v1/pool')['in_use']==0,'real stack busy; stop simulations'
terminal={'completed','failed','cancelled','expired','rejected','aborted'}
idle();runs=sim('/sim/v1/runs');runs=runs.get('runs',[]) if isinstance(runs,dict) else runs
assert all(x['phase'] in terminal for x in runs)
for port in [9915,9916,9917]:
 with socket.socket() as s:s.bind(('0.0.0.0',port))
with socket.socket(socket.AF_INET,socket.SOCK_DGRAM) as s:s.bind(('0.0.0.0',15068))
root.mkdir();(root/'state').mkdir();(root/'state/notes/retros').mkdir(parents=True)
shutil.copytree('/home/chuanming/golive-crm',root/'crm-snapshot')
shutil.copytree(real/'private',root/'private')
for mode in ['baseline','candidate']:
 for service in ['engine','crm']:shutil.copy2('/tmp/toolack-'+mode+'-'+service,root/(mode+'-'+service));(root/(mode+'-'+service)).chmod(0o700)
shutil.copy2(real/'bin/adapter',root/'adapter');shutil.copy2(real/'bin/journal',root/'journal')
originals=[Path('/home/chuanming/golive-telephony/configs/telephony.json'),Path('/home/chuanming/golive-last-call.json'),real/'private/agent.json',real/'private/adapter.json']+list(Path('/home/chuanming/golive-crm/configs').glob('*/*.json'))
original_hashes={str(p):hashlib.sha256(p.read_bytes()).hexdigest() for p in originals};save(root/'original-hashes.json',original_hashes)
env=envfile(root/'private/engine.env');env.update(GOLIVE_ADDR='127.0.0.1:9915',GOLIVE_LAST_CALL_FILE=str(root/'state/last-call.json'),GOLIVE_STORE_DIR=str(root/'state/store'))
for k,v in env.items():env[k]=v.replace('127.0.0.1:8791','127.0.0.1:9917')
crm_env=dict(x.decode().split('=',1) for x in Path('/proc/3313962/environ').read_bytes().split(b'\0') if b'=' in x)
def remap(v):
 if isinstance(v,dict):return {k:remap(x) for k,x in v.items()}
 if isinstance(v,list):return [remap(x) for x in v]
 if isinstance(v,str):return v.replace('127.0.0.1:8791','127.0.0.1:9917')
 return v
profile=remap(json.loads((root/'private/agent.json').read_text()));profile.update(addr='127.0.0.1:9915',store_dir=str(root/'state/store'),last_call_file=str(root/'state/last-call.json'))
save(root/'private/profile.json',profile)
saved=json.loads((root/'private/saved-call.json').read_text());save(root/'state/last-call.json',saved)
scenario=json.loads((previous/'private/scenario.json').read_text());scenario['max_duration_ms']=90000
scenario.update(id='tool-ack-focused',persona='你是已经决定续保的客户，只核实报价，不闲聊，不询问公司身份。',goal='先要求计算默认方案总价，随后按指定变更要求重新计算总价，等报价说完。',greeting='请直接帮我算一下默认方案的总保费。',behavior=['不讨论购买流程，不重复问身份。','对方开始计算时等结果，不催促；听到金额后简短确认。','所有报价明确让对方计算，不自行编造金额。'],expectations=[],hangup_after_goal=False)
scenario['actions']=[dict(id='change-one',trigger='connected',occurrence=1,after_ms=25000,require_remote_speaking=False,kind='speak',text='三者险改成五百万，驾乘险改成八十万，不要座位险，请重新计算总价。',max_hold_ms=5000),dict(id='change-two',trigger='connected',occurrence=1,after_ms=60000,require_remote_speaking=False,kind='speak',text='三者险改成三百万，驾乘险改成五十万，保留其他项目，请重新计算总价。',max_hold_ms=5000)]
adapter_template=json.loads((root/'private/adapter.json').read_text());adapter_template.update(golive_url='http://127.0.0.1:9915',sip_listen='0.0.0.0:15068',rtp_min=31400,rtp_max=31431,record_dir=str(root/'state/adapter'))
state={'order':['baseline','candidate'],'runs':[],'started_at':datetime.datetime.now(datetime.timezone.utc).isoformat(),'binary_hashes':{m+'-'+s:hashlib.sha256((root/(m+'-'+s)).read_bytes()).hexdigest() for m in ['baseline','candidate'] for s in ['engine','crm']}}
save(root/'attempt.json',state)
log=(root/'state/golive.log').open('ab')
jlog=(root/'state/journal.log').open('ab')
journal=subprocess.Popen([str(root/'journal'),'-addr','192.168.71.9:9916','-log',str(root/'state/golive.log'),'-store',str(root/'state/store'),'-data',str(root/'state/journal'),'-archive',str(root/'state/archive'),'-adapter-records',str(root/'state/adapter'),'-notes',str(root/'state/notes'),'-last-call',str(root/'state/last-call.json'),'-target','1200','-retention-hours','720'],stdout=jlog,stderr=subprocess.STDOUT)
state['journal_pid']=journal.pid;save(root/'attempt.json',state)
for idx,mode in enumerate(state['order'],1):
 idle();out=root/f'{idx:02d}-{mode}';out.mkdir();shutil.copytree(root/'crm-snapshot',out/'crm')
 clog=(out/'crm-process.log').open('ab')
 crm=subprocess.Popen([str(root/(mode+'-crm')),'-dir',str(out/'crm'),'-addr','127.0.0.1:9917','-originator','http://127.0.0.1:1','-golive','http://127.0.0.1:9915'],env=crm_env,stdout=clog,stderr=subprocess.STDOUT)
 engine=None;adapter=None
 try:
  for _ in range(80):
   try:
    if request('http://127.0.0.1:9917/healthz'):break
   except OSError:pass
   assert crm.poll() is None,'isolated CRM exited';time.sleep(.25)
  else:raise RuntimeError('CRM not ready')
  engine=subprocess.Popen([str(root/(mode+'-engine')),'-profile',str(root/'private/profile.json'),'-env',str(root/'private/providers.env'),'-addr','127.0.0.1:9915'],env=env,cwd='/home/chuanming/golive-telephony',stdout=log,stderr=subprocess.STDOUT)
  for _ in range(120):
   try:
    pool=request('http://127.0.0.1:9915/v1/pool')
    if pool.get('ok') and pool['in_use']==0:break
   except OSError:pass
   assert engine.poll() is None,'isolated engine exited';time.sleep(.25)
  else:raise RuntimeError('engine not ready')
  assert request('http://127.0.0.1:9915/healthz')['version']=='toolack-'+mode
  for _ in range(80):
   w=request('http://127.0.0.1:9915/v1/opening',{'greeting':saved['greeting'],'warm':True});assert not w.get('kicked')
   if w.get('cached') and all(x.get('ready') for x in w['cached']):break
   time.sleep(.3)
  else:raise RuntimeError('opening not warmed')
  run_info=sim('/sim/v1/runs',scenario,'tool-ack-focused-20261101-'+str(idx));run=run_info['run_id'];assert re.fullmatch(r'sim-[A-Za-z0-9_-]+',run)
  cid='sim-toolack-'+str(uuid.uuid4());row=dict(mode=mode,run=run,call=cid,stage='reserved');state['runs'].append(row);save(root/'attempt.json',state);save(out/'reservation.json',run_info)
  cfg=dict(adapter_template,allowed_uri='sip:'+run+'@192.168.71.9');assert re.fullmatch(r'sip:sim-[A-Za-z0-9_-]+@192\.168\.71\.9',cfg['allowed_uri']);save(out/'adapter.json',cfg);save(out/'call.json',dict(call_id=cid,**{k:saved.get(k,'') for k in ['instructions','greeting','voice']}))
  row['stage']='dial_requested';save(root/'attempt.json',state)
  alog=(out/'adapter.log').open('ab');adapter=subprocess.Popen([str(root/'adapter'),'-config',str(out/'adapter.json'),'-call',str(out/'call.json'),'-dial'],env=dict(os.environ,**envfile(root/'private/adapter.env')),stdout=alog,stderr=subprocess.STDOUT)
  row.update(adapter_pid=adapter.pid,stage='dialed');save(root/'attempt.json',state);say(row)
  deadline=time.monotonic()+155
  while time.monotonic()<deadline:
   current=sim('/sim/v1/runs/'+run)
   if current['phase'] in terminal:break
   time.sleep(2)
  else:raise RuntimeError('simulator timeout; inspect before continuing')
  report=sim('/sim/v1/runs/'+run+'/report');save(out/'receiver-report.json',report)
  adapter.wait(timeout=10);assert adapter.returncode==0,'adapter failed after simulator end'
  vals=sorted(x['value_ms'] for x in report.get('assistant_response_gaps',[]) if x.get('valid') is True and x.get('evidence')=='application_audio_boundary' and x.get('value_ms',-1)>=0)
  row.update(stage='ended',phase=current['phase'],adapter_exit=adapter.returncode,gaps_ms=vals,termination_reason=report.get('termination_reason'));save(root/'attempt.json',state);say(row)
  assert request('http://127.0.0.1:9915/v1/pool')['in_use']==0
 finally:
  if adapter and adapter.poll() is None:adapter.terminate();adapter.wait(timeout=10)
  if engine and engine.poll() is None:engine.terminate();engine.wait(timeout=45)
  if crm.poll() is None:crm.terminate();crm.wait(timeout=120)
 idle()
assert subprocess.check_output(['docker','inspect','--format','{{.State.Running}}','active-call'],text=True).strip()=='true'
assert all(hashlib.sha256(Path(p).read_bytes()).hexdigest()==h for p,h in original_hashes.items()),'original configs changed'
state['completed_at']=datetime.datetime.now(datetime.timezone.utc).isoformat();save(root/'attempt.json',state);say({'complete':True,'originals_unchanged':True})
