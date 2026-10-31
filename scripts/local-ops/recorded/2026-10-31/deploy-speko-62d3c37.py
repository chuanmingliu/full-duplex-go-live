import os,pathlib,json,re,urllib.request,subprocess,signal,time,shutil,hashlib
repo=pathlib.Path('/Users/chuanmingliu/Documents/ChatGPT/full-duplex-go-live')
runtime=repo/'bin/speko-runtime'
commit='62d3c375e28808943eeef3bd2d199712fcd14c64'
ci=json.loads(subprocess.check_output(['gh','run','view','41214917072','--json','headSha,status,conclusion'],cwd=repo))
assert ci['headSha']==commit and ci['status']=='completed' and ci['conclusion']=='success','CI not passed for this commit'
values={}
for line in (pathlib.Path.home()/'.config/golive/speko.env').read_text().splitlines():
 m=re.fullmatch(r'\s*(?:export\s+)?([A-Z0-9_]+)\s*=\s*(.*?)\s*',line)
 if m:values[m[1]]=m[2].strip('\"\'')
def pool():
 r=urllib.request.Request('http://127.0.0.1:8095/v1/pool',headers={'Authorization':'Bearer '+values['GOLIVE_AUTH_TOKEN']})
 return json.load(urllib.request.urlopen(r,timeout=2))
def stopped(pid):
 for _ in range(120):
  if subprocess.run(['kill','-0',str(pid)],capture_output=True).returncode:return
  time.sleep(.1)
 raise RuntimeError('process did not stop; refusing binary replacement')
assert pool()['in_use']==0,'active session; refusing restart'
pid=int((runtime/'bridge.pid').read_text())
command=subprocess.check_output(['ps','-p',str(pid),'-o','command='],text=True)
assert str(runtime/'golive-speko') in command,'unexpected process'
old_release=(runtime/'release.json').read_text()
old=json.loads(old_release)
assert old['commit']=='3a58396','unexpected release'
assert hashlib.sha256((runtime/'golive-speko').read_bytes()).hexdigest()==old['sha256'],'old binary mismatch'
assert hashlib.sha256(pathlib.Path('/tmp/golive-speko-overflow-release').read_bytes()).hexdigest()=='7f782fb6abe78c3fa2932b1db0dcc74b9de987228f745cc435faee53d527b2e3','candidate hash mismatch'
backup=runtime/'golive-speko-before-62d3c37'
assert not backup.exists(),'backup already exists'
(runtime/'release-before-62d3c37.json').write_text(old_release)
shutil.copy2(runtime/'golive-speko',backup)
shutil.copy2('/tmp/golive-speko-overflow-release',runtime/'golive-speko.next')
assert pool()['in_use']==0,'active session; refusing restart'
os.kill(pid,signal.SIGTERM)
stopped(pid)
os.replace(runtime/'golive-speko.next',runtime/'golive-speko')
current=None
def launch(new):
 global current
 env=dict(os.environ,SPEKO_DELEGATION_MODE='client',SPEKO_PRECONNECT='1',SPEKO_LOCAL_BARGE_IN='1')
 if new:env.update(SPEKO_AUTO_HANGUP='0',SPEKO_RESPONSES_REASONING='none')
 else:
  env.pop('SPEKO_AUTO_HANGUP',None);env.pop('SPEKO_RESPONSES_REASONING',None)
 with (runtime/'bridge.log').open('ab') as log:
  current=subprocess.Popen([str(runtime/'golive-speko'),'-addr','127.0.0.1:8095','-env',str(pathlib.Path.home()/'.config/golive/speko.env'),'-max-sessions','1'],env=env,stdout=log,stderr=log,start_new_session=True)
 (runtime/'bridge.pid').write_text(str(current.pid))
 for _ in range(100):
  if current.poll() is not None:raise RuntimeError('bridge exited')
  try:
   if pool().get('status')=='ready':return current.pid
  except Exception:pass
  time.sleep(.1)
 raise RuntimeError('bridge not ready')
try:
 newpid=launch(True)
 release=dict(deployed_at=time.strftime('%Y-%m-%dT%H:%M:%SZ',time.gmtime()),commit=commit,sha256=hashlib.sha256((runtime/'golive-speko').read_bytes()).hexdigest(),delegation_mode='client',preconnect=True,local_barge_in=True,auto_hangup=False,responses_reasoning='none',playout_overflow='cancel_reply',address='127.0.0.1:8095',ci_run=41214917072)
 (runtime/'release.json').write_text(json.dumps(release,indent=2))
 print(json.dumps({'deployed':release,'pid':newpid,'status':pool()['status']}))
except Exception:
 if current and current.poll() is None:
  current.terminate();current.wait(timeout=12)
 shutil.copy2(backup,runtime/'golive-speko')
 launch(False)
 (runtime/'release.json').write_text(old_release)
 raise
