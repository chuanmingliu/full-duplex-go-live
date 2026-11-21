from pathlib import Path
import hashlib, json, os, re, subprocess, tarfile, time, urllib.request
os.umask(0o077)
home=Path.home();root=home/'golive-telephony'
release=home/'golive-backchannel/releases/20261102-4896f47'
receipt=root/'codex-latency/deploy-backchannel-20261102.json'
payload=home/'golive/golive-backchannel-20261102.tar.gz'
assert not release.exists() and not receipt.exists(), 'Attempt exists; inspect instead of replaying'
assert hashlib.sha256(payload.read_bytes()).hexdigest()=='4f59bcd28518f3e9e50b51ba9642a5b9033d774429513e294946b941ef92289e'
def cmd(args,**kwargs):return subprocess.check_output(args,text=True,stderr=subprocess.STDOUT,**kwargs).strip()
def get(url):
    with urllib.request.urlopen(url,timeout=5) as f:return json.load(f)
def sha(p):return hashlib.sha256(p.read_bytes()).hexdigest()
def live():return get('http://127.0.0.1:8765/healthz')
assert live()=={'model':'golive-1','sessions':0,'status':'ok','version':'f1fd9a6'}
binding=get('http://127.0.0.1:3000/api/realtime/brain')
assert binding['backend']=='local' and binding['revision']==13
pids=set(re.findall(r'pid=(\d+)',cmd(['ss','-lntpH','sport = :8765'])))
assert len(pids)==1;proc=Path('/proc')/pids.pop()
protected={p:sha(p) for p in [root/'configs/telephony.json',home/'golive-last-call.json',proc/'exe',proc/'cmdline',proc/'environ']}
assert protected[root/'configs/telephony.json']=='072ab8dbda962eff682fa56fcd049f70cd3717530e64a06bec97e66618878e7d'
assert protected[proc/'exe']=='30978356547cce422e0af04363828af9ee7cf2397406d5efc278811c86b32c22'
assert not cmd(['ss','-lntH','sport = :18087'])
assert not cmd(['docker','ps','-aq','--filter','label=com.docker.compose.project=golive-backchannel-detector'])
original_containers=set(cmd(['docker','ps','-q','--no-trunc']).splitlines())
state={'stage':'prepared','release':str(release),'model_revision':'4896f4774cdf1a9c5b581b0cd4a0c4000e8bb854','default_controller':'legacy','live_engine_hook_enabled':False,'phone_build':'f1fd9a6'}
def save(stage):
    state['stage']=stage;receipt.write_text(json.dumps(state,indent=2)+'\n');print(json.dumps({'stage':stage}),flush=True)
save('prepared')
release.mkdir(parents=True,mode=0o755);release.chmod(0o755)
with tarfile.open(payload) as tar:
    for member in tar.getmembers():
        p=(release/member.name).resolve()
        assert p.is_relative_to(release) and member.isfile() and member.size<10_000_000,'Invalid archive member'
        p.parent.mkdir(parents=True,exist_ok=True);p.parent.chmod(0o755)
        p.write_bytes(tar.extractfile(member).read());p.chmod(0o644)
(release/'.env').write_text('BCD_RELEASE=20261102-4896f47\n')
image='golive-backchannel-detector:20261102-4896f47'
compose=['docker','compose','--project-directory',str(release),'-f',str(release/'compose.yaml')]
started=False
try:
    subprocess.run(compose+['config','--quiet'],check=True)
    save('building')
    subprocess.run(compose+['build','detector'],check=True,timeout=600)
    save('testing')
    test=['docker','run','--rm','--network','none','--cpus','1','--memory','384m','--read-only','--cap-drop','ALL','--security-opt','no-new-privileges',
          '-v',str(release)+':/tests:ro','-v',str(release/'models')+':/models:ro','-w','/tests','-e','BCD_MODEL_DIR=/models',image,'python','-m','unittest','-v','test_service']
    subprocess.run(test,check=True,timeout=90)
    assert live()['sessions']==0,'A call began during build; defer service launch'
    save('starting')
    subprocess.run(compose+['up','-d','--no-deps','detector'],check=True,timeout=60);started=True
    def ready():
        until=time.monotonic()+60
        while time.monotonic()<until:
            try:
                h=get('http://127.0.0.1:18087/healthz')
                if h['status']=='ready' and h['default_controller']=='legacy':return h
            except Exception:pass
            time.sleep(1)
        raise RuntimeError('Detector health timeout')
    state['health']=ready()
    probe=['docker','run','--rm','--network','host','--cpus','1','--memory','384m','--read-only','--cap-drop','ALL','--security-opt','no-new-privileges',
           '--user',f'{os.getuid()}:{os.getgid()}','-v',str(release)+':/tests:ro',image,'python','/tests/probe.py']
    state['smoke']=json.loads(cmd(probe+['--seconds','4'],timeout=45))
    call='out-mvykfqfn-wibn';d=get('http://192.168.71.9:8790/api/calls/'+call)
    assert d['call']['id']==call and d['archive']['archived'] and d['call']['ended_at']
    archive=Path(d['archive']['dir']).resolve();assert archive.is_relative_to(home/'golive-archive')
    manifest=json.loads((archive/'manifest.json').read_text());wav=archive/'audio.wav'
    assert sha(wav)==manifest['files']['audio.wav']['sha256']
    replay=probe[:probe.index(image)]+['-v',str(wav)+':/input.wav:ro']+probe[probe.index(image):]+['--wav','/input.wav','--seconds','180']
    state['replay']=json.loads(cmd(replay,timeout=180))
    save('replay_verified')
    # Exercise observer unavailability without touching any phone service.
    subprocess.run(compose+['stop','-t','5','detector'],check=True,timeout=30)
    state['legacy_healthy_with_observer_stopped']=live()['status']=='ok' and all(sha(p)==s for p,s in protected.items())
    subprocess.run(compose+['up','-d','--no-deps','detector'],check=True,timeout=60)
    state['health_after_restart']=ready()
    cid=cmd(compose+['ps','-q','detector'])
    inspection=json.loads(cmd(['docker','inspect',cid]))[0]
    state['container_id']=cid;state['image_id']=inspection['Image']
    state['port_bindings']=inspection['HostConfig']['PortBindings']
    assert state['port_bindings']['18087/tcp'][0]['HostIp']=='127.0.0.1'
    state['original_runtime_and_configs_preserved']=all(sha(p)==s for p,s in protected.items())
    state['aria_route_preserved']=get('http://127.0.0.1:3000/api/realtime/brain')==binding
    state['original_containers_preserved']=original_containers.issubset(set(cmd(['docker','ps','-q','--no-trunc']).splitlines()))
    assert all(state[k] for k in ['legacy_healthy_with_observer_stopped','original_runtime_and_configs_preserved','aria_route_preserved','original_containers_preserved'])
    save('verified');print(json.dumps(state),flush=True)
except Exception as e:
    state['error_type']=type(e).__name__
    if started:
        subprocess.run(compose+['stop','-t','5','detector'],timeout=30,check=False)
    save('failed');raise
