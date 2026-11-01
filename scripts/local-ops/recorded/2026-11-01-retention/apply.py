#!/usr/bin/env python3
"""Historical retention-only operation, run on box through the Mac runner."""
import json
import os
from pathlib import Path
import re
import shutil
import signal
import subprocess
import time
import urllib.request

TTL = '8760'
BOX_HOME = Path('/home/chuanming')
ROOT = BOX_HOME / 'golive-telephony'
BACKUP = ROOT / 'retention-releases' / time.strftime('%Y%m%d-%H%M%S')
PORTS = [8765, 9080, 9875, 9885, 9895]
JPORTS = [8790, 9876, 9886, 9896, 9906, 9916]

def read(url):
    with urllib.request.urlopen(url, timeout=10) as response:
        return json.load(response)

def idle(port):
    h = read(f'http://127.0.0.1:{port}/healthz')
    assert h.get('status') == 'ok' and h.get('sessions') == 0, f'{port} not proven idle'
    return h

def callids(port):
    d = read(f'http://192.168.71.9:{port}/api/overview')
    return {c['id'] for c in d['calls']}

versions = {p:idle(p)['version'] for p in PORTS}
before = {p:callids(p) for p in JPORTS}
active = subprocess.check_output(['docker','inspect','--format','{{.Id}} {{.State.StartedAt}}','active-call'],text=True)
BACKUP.mkdir(parents=True, exist_ok=False)
BACKUP.chmod(0o700)
changed = []

def write(path, data):
    saved = BACKUP / str(path.relative_to(BOX_HOME))
    saved.parent.mkdir(parents=True, exist_ok=True)
    shutil.copy2(path, saved)
    tmp = path.with_name(path.name + '.retention-new')
    tmp.write_bytes(data)
    tmp.chmod(path.stat().st_mode & 0o777)
    os.replace(tmp, path)
    changed.append(str(path))

def processes():
    for p in Path('/proc').iterdir():
        if not p.name.isdigit(): continue
        try:
            args = [v.decode() for v in (p/'cmdline').read_bytes().split(b'\0') if v]
            yield p,args
        except (FileNotFoundError,PermissionError,ProcessLookupError): pass

pid = int((ROOT/'.phone-brain.journal.pid').read_text())
assert (Path('/proc')/str(pid)/'cwd').resolve() == ROOT
engine = next(p for p,args in processes() if args and 'golive-linux-' in args[0] and '-addr' in args and args[args.index('-addr')+1]=='127.0.0.1:8765')
env = dict(v.decode().split('=',1) for v in (engine/'environ').read_bytes().split(b'\0') if b'=' in v)
assert env.get('GOLIVE_STORE_TTL_HOURS') in ['720',TTL]

# Persist the original launch default without replacing the rest of the script.
script = ROOT/'scripts/phone-brain.sh'
text = script.read_text()
assert 'CALLS_TTL_HOURS:-720' in text or 'CALLS_TTL_HOURS:-8760' in text
text = text.replace('CALLS_TTL_HOURS:-720','CALLS_TTL_HOURS:-8760').replace('(720: a month, the owner\'s choice 10-15)','(8760: 365 days, the owner\'s choice 2026-11-01)')
write(script,text.encode())
subprocess.run(['bash','-n',str(script)],check=True)

stacks = [('cb6c639',9875),('32bc369',9885),('native-real-fb11e2c',9895)]
for name, port in stacks:
    stack=BOX_HOME/'golive-sip-ab'/name
    compose=stack/'compose.json'
    config=json.loads(compose.read_text())
    engine_config=config['services']['engine']
    environment=engine_config.setdefault('environment',{})
    assert isinstance(environment,dict)
    environment['GOLIVE_STORE_TTL_HOURS']=TTL
    args=config['services']['journal']['command']
    assert '-retention-hours' in args
    args[args.index('-retention-hours')+1]=TTL
    write(compose,(json.dumps(config,indent=2)+'\n').encode())
    # Validate privately: a compose render could contain provider credentials.
    subprocess.run(['docker','compose','-f',str(compose),'config','-q'],cwd=stack,check=True,stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
    idle(port)
    with (BACKUP/(name+'.restart.log')).open('wb') as log:
        subprocess.run(['docker','compose','-f',str(compose),'up','-d','--no-deps','engine','journal'],cwd=stack,check=True,stdout=log,stderr=log,timeout=90)
    for _ in range(60):
        try:
            assert idle(port)['version']==versions[port]
            break
        except Exception: time.sleep(.5)
    else: raise RuntimeError(f'{name} did not recover; backups in {BACKUP}')

# Restart only the two standalone simulator Journal readers; engines are stopped.
for port in [9906,9916]:
    p,args=next((p,args) for p,args in processes() if args and args[0].endswith('/journal') and '-addr' in args and args[args.index('-addr')+1]==f'192.168.71.9:{port}')
    cwd=(p/'cwd').resolve()
    oldenv=dict(v.decode().split('=',1) for v in (p/'environ').read_bytes().split(b'\0') if b'=' in v)
    stack=Path(args[0]).parent
    args[args.index('-retention-hours')+1]=TTL
    (BACKUP/f'journal-{port}-argv.json').write_text(json.dumps(args))
    os.kill(int(p.name),signal.SIGTERM)
    for _ in range(50):
        if not p.exists(): break
        time.sleep(.1)
    else: raise RuntimeError('Journal did not stop; no forced termination')
    with (stack/'journal-retention.log').open('ab') as log:
        child=subprocess.Popen(args,cwd=cwd,env=oldenv,stdin=subprocess.DEVNULL,stdout=log,stderr=log,start_new_session=True)
    (stack/'journal-runtime.json').write_text(json.dumps({'pid':child.pid,'retention_hours':int(TTL),'argv':args},indent=2))

# Reuse existing provider/environment settings, changing only the retention values.
idle(8765);idle(9080)
env['GOLIVE_STORE_TTL_HOURS']=TTL
env['CALLS_TTL_HOURS']=TTL
with (BACKUP/'original.restart.log').open('wb') as log:
    subprocess.run(['bash','scripts/phone-brain.sh','restart'],cwd=ROOT,env=env,stdout=log,stderr=log,check=True,timeout=150)

for port in JPORTS:
    for _ in range(40):
        try:
            data=read(f'http://192.168.71.9:{port}/api/overview')
            assert data['retention_hours']==int(TTL)
            assert before[port] <= {c['id'] for c in data['calls']}
            break
        except Exception: time.sleep(.5)
    else: raise RuntimeError(f'Journal {port} verification failed')
for port in PORTS: assert idle(port)['version']==versions[port]
assert subprocess.check_output(['docker','inspect','--format','{{.Id}} {{.State.StartedAt}}','active-call'],text=True)==active
live=[]
for p,args in processes():
    if not args: continue
    if ('golive-linux-' in args[0] or args[0].endswith('/golive')):
        try:
            values=dict(v.decode().split('=',1) for v in (p/'environ').read_bytes().split(b'\0') if b'=' in v)
            if values.get('GOLIVE_STORE_DIR') and values.get('GOLIVE_REALTIME_STORE_CALLS')=='1':
                assert values.get('GOLIVE_STORE_TTL_HOURS')==TTL, 'Recording engine TTL mismatch'
                live.append({'pid':int(p.name),'ttl_hours':int(TTL)})
        except PermissionError: pass
receipt={'retention_days':365,'retention_hours':int(TTL),'journals':{p:len(before[p]) for p in JPORTS},'versions':versions,'recording_processes':live,'changed_files':changed,'archive_policy':'permanent; no archive deletions','active_call':'unchanged','backup':str(BACKUP)}
(BACKUP/'verified.json').write_text(json.dumps(receipt,indent=2))
print(json.dumps(receipt))
