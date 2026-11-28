#timeout 660
#pull /home/chuanming/golive-telephony/codex-latency/activecall-backchannel-60dbe53/summary.json activecall-backchannel-60dbe53-summary.json
set -eu
python3 -B - <<'VERIFY'
import hashlib
from pathlib import Path
p=Path.home()/"golive/call-latency-45b5564.py"
assert hashlib.sha256(p.read_bytes()).hexdigest()=='c5df3bf2a120d4bcc241f75a1e2c28b3205a903dd94b33516fa21e11ea549a50'
VERIFY
python3 -B - <<'SIMULATION'
import array, hashlib, json, math, os, re, subprocess, time, urllib.request, wave
from pathlib import Path
from collections import Counter
os.umask(0o077)
HOME_DIR=Path.home(); ROOT=HOME_DIR/'golive-telephony'; SIM=HOME_DIR/'golive-sim'
OUT=ROOT/'codex-latency/activecall-backchannel-60dbe53'
TARGET='60dbe53'
SCENARIO=SIM/'scenarios/custom/set-a-01-barge-in.json'
SCENARIO_HASH='3d2f3fbffb27759bf84e551944b99bef19b4489d83035ff734940ef1dd496c1a'
PROFILE_HASH=json.loads((ROOT/'codex-latency/deployment-backchannel-60dbe53.json').read_text())['trial_profile_sha256']
BINARY_HASH='f4d36a4814514e5ddcfb405a3af086457c8235d5b8a4d826a9952f186132dd9a'
SCRIPT_HASH='c21b7fa28d483ec18fb7c2a6eb714c08b6bf40358e48c4235b034d7b441d482a'
REPORT=HOME_DIR/'golive/call-latency-45b5564.py'

def require(ok, msg):
    if not ok: raise RuntimeError(msg)
def sha(p):
    h=hashlib.sha256()
    with Path(p).open('rb') as f:
        for chunk in iter(lambda:f.read(1048576),b''): h.update(chunk)
    return h.hexdigest()
def get(url, token=None):
    q=urllib.request.Request(url,headers={'Authorization':'Bearer '+token} if token else {})
    with urllib.request.urlopen(q,timeout=15) as r:
        if url.endswith('/readyz'):
            require(r.status == 204, 'simulator readiness failed')
            return {'ready': True}
        return json.load(r)
def save(name,data):
    tmp=OUT/(name+'.tmp'); tmp.write_text(json.dumps(data,ensure_ascii=False,indent=2)+'\n'); os.replace(tmp,OUT/name)
def guard():
    h=get('http://127.0.0.1:8765/healthz')
    require(h.get('status')=='ok' and h.get('version')==TARGET,'engine version/health changed')
    require(sha(ROOT/'configs/telephony.json')==PROFILE_HASH,'profile changed')
    require(sha(ROOT/'bin/golive-linux-amd64')==BINARY_HASH,'engine binary changed')
    require(sha(ROOT/'scripts/phone-brain.sh')==SCRIPT_HASH,'dial helper changed')
    require(sha(SCENARIO)==SCENARIO_HASH,'scenario changed')
    return h
require(not OUT.exists(),'attempt exists; collect it, never redial')
deployment=json.loads((ROOT/'codex-latency/deployment-backchannel-60dbe53.json').read_text())
require(deployment.get('stage')=='verified' and deployment.get('phrase_warm_verified') is True and deployment.get('new_version')==TARGET,'candidate deployment not verified')
require(guard().get('sessions')==0,'engine has a call')
overview=get('http://192.168.71.9:8790/api/overview')
require(overview.get('recording') is True and overview.get('retention_hours')==8760,'recording/retention not ready')
kept_path=HOME_DIR/'golive-last-call.json'; kept=json.loads(kept_path.read_text())
require(bool(kept.get('instructions')),'saved prompt missing')
context_hashes={k:hashlib.sha256(str(kept.get(k,'')).encode()).hexdigest() for k in ('instructions','voice','greeting')}
token=next((l.split('=',1)[1].strip().strip('\"\'') for l in (SIM/'tokens.env').read_text().splitlines() if l.startswith('SIM_CONTROL_TOKEN=')), '')
require(bool(token),'simulator credential missing')
def sim(path): return get('http://127.0.0.1:18865'+path,token)
sim('/readyz'); runs=sim('/sim/v1/runs'); runs=runs.get('runs',[]) if isinstance(runs,dict) else runs
terminal={'completed','rejected','expired','failed','cancelled','aborted'}
require(isinstance(runs,list) and all(r.get('phase') in terminal for r in runs),'simulator reservation already active')
OUT.mkdir(mode=0o700)
state={'kind':'simulated','build':TARGET,'scenario':'set-a-01-barge-in','scenario_sha256':SCENARIO_HASH,'context_sha256':context_hashes,'stage':'reservation_requested'}
save('attempt.json',state)
try:
    prepared=subprocess.run(['bash',str(SIM/'scripts/sim-brain.sh'),'prepare',str(SCENARIO),'codex-activecall-backchannel-60dbe53'],capture_output=True,text=True,timeout=40)
    match=re.search(r'^run_id:\s+(sim-[A-Za-z0-9_-]+)',prepared.stdout,re.M)
    require(prepared.returncode==0 and match is not None,'reservation unsuccessful or uncertain; do not retry')
    run=match.group(1); state.update(run_id=run,stage='reserved'); save('attempt.json',state)
    require(sim('/sim/v1/runs/'+run).get('phase') in {'prepared','waiting'},'owned reservation not ready')
    require(guard().get('sessions')==0,'call began before simulation')
    require(all(json.loads(kept_path.read_text()).get(k)==kept.get(k) for k in context_hashes),'saved context changed')
    state['stage']='dial_requested'; save('attempt.json',state)
    dial=subprocess.run(['bash',str(ROOT/'scripts/phone-brain.sh'),'call','sim',run],cwd=ROOT,capture_output=True,text=True,timeout=60,
        env=dict(os.environ,LAST_CALL=str(kept_path),SIM_DIR=str(SIM),ARIA_ENV=str(HOME_DIR/'aria/.env'),ARIA_URL='http://127.0.0.1:3000'))
    match=re.search(r'paired: call (out-[A-Za-z0-9-]+) <-> run '+re.escape(run),dial.stdout)
    require(dial.returncode==0 and match is not None,'dial uncertain or failed; do not redial')
    call=match.group(1); state.update(call_id=call,stage='dial_accepted'); save('attempt.json',state)
    print(json.dumps({'kind':'simulated','build':TARGET,'call':call,'run':run}),flush=True)
    until=time.monotonic()+355; previous=None
    while time.monotonic()<until:
        phase=sim('/sim/v1/runs/'+run).get('phase')
        if phase!=previous:
            print(json.dumps({'phase':phase if phase in terminal|{'prepared','waiting','connected','active','ringing','answering','ending','running'} else 'other'}),flush=True);previous=phase
        if phase in terminal: break
        time.sleep(3)
    else: raise RuntimeError('simulation observation limit; inspect same run')
    state['stage']='ended';save('attempt.json',state)
    until=time.monotonic()+120
    while True:
        data=get('http://192.168.71.9:8790/api/calls/'+call); c=data['call']; archive=data.get('archive',{})
        if c.get('ended_at') and not c['ended_at'].startswith('0001-') and archive.get('archived') and archive.get('audio'):break
        require(time.monotonic()<until,'archive still pending; collect this call later');time.sleep(5)
    require(c['id']==call and c.get('build')==TARGET and data.get('sim_run')==run,'journal identity mismatch')
    source=Path(archive['dir']).resolve();require(source.is_relative_to(HOME_DIR/'golive-archive'),'unexpected archive root')
    manifest=json.loads((source/'manifest.json').read_text());require(manifest['id']==call and manifest['session']==c['session'] and manifest.get('audio'),'manifest identity mismatch')
    for name in ('audio.wav','transcript.txt','call.json','session.json','golive.log'):
        entry=manifest['files'][name];p=source/name
        require(p.is_file() and not p.is_symlink() and p.stat().st_size==entry['bytes'] and sha(p)==entry['sha256'],'archive verification failed: '+name)
    meta=json.loads((source/'session.json').read_text()); require(meta.get('closed_at') and meta.get('id')==c['session'],'session not closed')
    require(hashlib.sha256(meta['session']['instructions'].encode()).hexdigest()==context_hashes['instructions'],'recorded prompt changed')
    require(meta['session'].get('audio',{}).get('output',{}).get('voice','')==kept.get('voice',''),'recorded voice changed')
    require(c.get('greeting')==kept.get('greeting'),'recorded greeting changed')
    speakers=Counter(t.get('who') for t in c.get('transcript',[]) if t.get('text','').strip());require(speakers['caller'] and speakers['assistant'],'transcript missing a side')
    signal=[False,False]
    with wave.open(str(source/'audio.wav'),'rb') as w:
        require(w.getnchannels()==2 and w.getsampwidth()==2,'audio must be stereo PCM16');seconds=w.getnframes()/w.getframerate()
        while chunk:=w.readframes(16000):
            values=array.array('h',chunk)
            for i in (0,1):signal[i]|=any(values[i::2])
    require(all(signal),'audio channel is silent')
    report=subprocess.run(['python3','-B',str(REPORT),str(source/'golive.log'),'--gate','--metric','both','--target-ms','1200','--min-turns','20','--quote-sla','separate','--json'],capture_output=True,text=True,timeout=30)
    require(report.returncode in (0,1),'latency analyzer failed');strict=json.loads(report.stdout);save('strict.json',strict)
    receiver=sim('/sim/v1/runs/'+run+'/report');save('receiver-private.json',receiver)
    require(receiver.get('run_id')==run and receiver.get('scenario_id')=='set-a-01-barge-in','receiver identity mismatch')
    values=sorted(g['value_ms'] for g in receiver.get('assistant_response_gaps',[]) if g.get('valid') is True and g.get('evidence')=='application_audio_boundary' and type(g.get('value_ms')) is int and g['value_ms']>=0)
    summary={**state,'stage':'verified','session':c['session'],'seconds':seconds,'archive_files_verified':5,'channels_with_signal':sum(signal),'caller_lines':speakers['caller'],'assistant_lines':speakers['assistant'],'counts':strict['counts'],'gate':strict['gate'],'handset_target_proven':False,
        'receiver_gap_n':len(values),'receiver_gap_p95_ms':values[max(0,math.ceil(.95*len(values))-1)] if values else None,'receiver_failure_count':len(receiver.get('failures',[])),'receiver_warning_count':len(receiver.get('warnings') or [])}
    for k in ('first_audio','first_content','conversational_first_content','tool_result','quote_readout'):summary[k]=strict.get(k)
    require(guard().get('sessions')==0,'engine still occupied after simulator ended')
    require(all(json.loads(kept_path.read_text()).get(k)==kept.get(k) for k in context_hashes),'saved context changed')
    import shlex
    records=[]
    for line in (source/'golive.log').read_text().splitlines():
        try:r=json.loads(line) if line.lstrip().startswith('{') else dict(x.split('=',1) for x in shlex.split(line) if '=' in x)
        except ValueError:continue
        if r.get('session')==c['session']:records.append(r)
    import datetime
    pending=None; cuts=[]; unpaired=0
    for r in records:
        message=r.get('msg')
        if message=='listen: utterance opened':
            pending=datetime.datetime.fromisoformat(r['time'].replace('Z','+00:00')) if r.get('barge_in') in (True,'true') else None
        elif message=='new query while answering: cutting the answer' and r.get('barge_in') in (True,'true') and r.get('speaking') in (True,'true'):
            if pending is None: unpaired+=1
            else:
                cuts.append(round((datetime.datetime.fromisoformat(r['time'].replace('Z','+00:00'))-pending).total_seconds()*1000,3));pending=None
    summary['vad_open_to_server_cut_ms']=cuts
    summary['unpaired_playback_cuts']=unpaired
    summary['detector_connected']=sum(r.get('msg')=='backchannel: connected' for r in records)
    summary['detector_processed_chunks']=sum(int(r.get('processed_chunks',0)) for r in records if r.get('msg')=='backchannel: stream closed')
    summary['detector_fallbacks']=[r.get('reason') for r in records if r.get('msg')=='backchannel: legacy fallback']
    summary['detector_evidence']=sum(r.get('msg')=='backchannel: floor evidence' for r in records)
    summary['detector_assists']=sum(r.get('msg')=='backchannel: earlier ASR final requested' for r in records)
    require(summary['detector_connected']>0 and summary['detector_processed_chunks']>0,'detector did not receive this call')
    require(not summary['detector_fallbacks'],'detector fell back during integration simulator')
    require(summary['receiver_failure_count']==0,'simulator receiver failed')
    save('summary.json',summary);state['stage']='verified';save('attempt.json',state);print(json.dumps(summary),flush=True)
except Exception as e:
    state['failure_type']=type(e).__name__
    if isinstance(e,RuntimeError):state['failure']=str(e)
    save('attempt.json',state)
    print(json.dumps({'stage':state['stage'],'failure_type':type(e).__name__,'detail':str(e) if isinstance(e,RuntimeError) else 'private diagnostic retained'}),flush=True)
    raise SystemExit(1)

SIMULATION
