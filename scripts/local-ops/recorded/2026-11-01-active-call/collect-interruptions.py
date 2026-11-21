import array, hashlib, json, math, os, re, subprocess, time, urllib.request, wave
from pathlib import Path
from collections import Counter
os.umask(0o077)
HOME_DIR=Path.home(); ROOT=HOME_DIR/'golive-telephony'; SIM=HOME_DIR/'golive-sim'
OUT=ROOT/'codex-latency/activecall-interrupt-45b5564-20261101-a'
TARGET='45b5564'
SCENARIO=SIM/'scenarios/custom/set-a-01-barge-in.json'
SCENARIO_HASH='3d2f3fbffb27759bf84e551944b99bef19b4489d83035ff734940ef1dd496c1a'
PROFILE_HASH='072ab8dbda962eff682fa56fcd049f70cd3717530e64a06bec97e66618878e7d'
BINARY_HASH='0af8a624053a706bc4f2f20ec2b9f847409eef2e6f502e21520cca60a8ec8a29'
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
    pass # Read-only collection; no remote writes

call='out-mvxksnvy-4i3q'; run='sim-2b244c1dd4612f1a20113b1b'
state=json.loads((OUT/'attempt.json').read_text())
kept=json.loads((HOME_DIR/'golive-last-call.json').read_text()); context_hashes=state['context_sha256']
token=next(l.split('=',1)[1].strip().strip('\"\'') for l in (SIM/'tokens.env').read_text().splitlines() if l.startswith('SIM_CONTROL_TOKEN='))
def sim(path): return get('http://127.0.0.1:18865'+path,token)
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
# Prove the initial opening from canonical logs, retaining the derived-field mismatch.
import shlex
openings=[]
for line in (source/'golive.log').read_text().splitlines():
    try: row=json.loads(line)
    except ValueError:
        try: row=dict(x.split('=',1) for x in shlex.split(line) if '=' in x)
        except ValueError: continue
    if row.get('session')==c['session'] and row.get('msg')=='speak: opening line': openings.append(row.get('text'))
require(bool(openings) and openings[0]==kept.get('greeting'),'initial greeting changed')
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
summary.update(stage='collected_readonly', initial_greeting_verified=True, journal_greeting_overwritten=True, opening_event_count=len(openings), action_outcomes=receiver.get('action_outcomes'), termination_reason=receiver.get('termination_reason'), limitations=['Only a short scripted interruption scenario; not twenty normal responses.', 'Journal greeting field overwritten by later system utterance; canonical initial opening matches saved greeting.', 'Server and application timings do not establish acoustic handset performance.'])
print(json.dumps(summary,indent=2))
