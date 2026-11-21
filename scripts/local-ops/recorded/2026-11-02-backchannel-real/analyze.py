from pathlib import Path
import array, collections, hashlib, importlib.util, json, os, shlex, subprocess, time, urllib.request, wave
os.umask(0o077)
call='out-mvzfq218-qbqy'
root=Path.home()/'golive-telephony'
out=root/'codex-latency'/('review-'+call)
assert not out.exists(), 'Analysis attempt already exists; inspect it'
def get(url):
    with urllib.request.urlopen(url,timeout=12) as f:return json.load(f)
def sha(p):return hashlib.sha256(p.read_bytes()).hexdigest()
def parse(line):
    try:return json.loads(line) if line.lstrip().startswith('{') else dict(w.split('=',1) for w in shlex.split(line) if '=' in w)
    except ValueError:return {}
deadline=time.monotonic()+60
while True:
    data=get('http://192.168.71.9:8790/api/calls/'+call)
    c=data['call'];a=data.get('archive',{})
    assert c['id']==call and c['build']=='b51e959' and not data.get('sim_run'), 'Wrong call identity'
    if c.get('ended_at') and not c['ended_at'].startswith('0001-') and a.get('archived') and a.get('audio'):break
    assert time.monotonic()<deadline, 'Call still active or archive pending; no redial or analysis'
    time.sleep(3)
src=Path(a['dir']).resolve(strict=True)
assert src.is_relative_to(Path.home()/'golive-archive')
m=json.loads((src/'manifest.json').read_text())
assert m['id']==call and m['session']==c['session']
for name in ('audio.wav','transcript.txt','call.json','session.json','golive.log'):
    p=src/name;e=m['files'][name]
    assert not p.is_symlink() and p.stat().st_size==e['bytes']>0 and sha(p)==e['sha256'],name
meta=json.loads((src/'session.json').read_text())
assert meta['id']==c['session'] and meta.get('closed_at') and not meta['closed_at'].startswith('0001-')
signals=[False,False]
with wave.open(str(src/'audio.wav'),'rb') as w:
    assert w.getnchannels()==2 and w.getsampwidth()==2
    seconds=w.getnframes()/w.getframerate();rate=w.getframerate()
    while chunk:=w.readframes(rate):
        pcm=array.array('h',chunk)
        for i in (0,1):signals[i]|=any(pcm[i::2])
assert all(signals), 'Missing audio direction'
speakers=collections.Counter(x.get('who') for x in c.get('transcript',[]) if x.get('text','').strip())
assert speakers['caller'] and speakers['assistant'], 'Missing transcript side'
out.mkdir(mode=0o700)
def save(name,value):(out/name).write_text(json.dumps(value,indent=2)+'\n')
lines=[line for line in (src/'golive.log').read_text().splitlines(True) if parse(line).get('session')==c['session']]
rows=[parse(line) for line in lines]
spec=importlib.util.spec_from_file_location('lat',str(Path.home()/'golive/call-latency-review-20261102.py'))
lat=importlib.util.module_from_spec(spec);spec.loader.exec_module(lat)
report,measured=lat.analyse(lines,380,1200)
reasons=lat.gate_reasons(report,20,'both','separate')
report['gate']={'enabled':True,'passed':not reasons,'metric':'both','min_turns':20,'quote_sla':'separate','reasons':reasons}
save('strict.json',report)
origins={r['origin_turn'] for r in report['tool_result_rows'] if r['quotation']}
origins.update(r['turn'] for r in rows if r.get('msg')=='tools: task submitted' and r.get('tool')=='quote_total' and r.get('turn'))
ordinary=[];acks=[]
for row,audio,content,pause in measured:
    if content is None:continue
    if row.get('_quote_result_joined') is True:continue
    clean={'turn':row.get('turn'),'first_content_total_ms':content+pause,'interrupted':lat.boolean(row,'truncated')}
    for key in ('end_silence_ms','asr_final_ms','turn_start_ms','llm_first_token_ms','first_segment_ms','tts_first_audio_ms','first_content_ms'):
        v=lat.integer(row,key)
        if v is not None:clean[key]=v
    (acks if row.get('turn') in origins else ordinary).append(clean)
save('normal-rows.json',ordinary);save('quotation-acknowledgments.json',acks)
# Reuse the established interruption pairing, in a private derived directory.
for name in ('call.json','golive.log'):(out/name).write_bytes((src/name).read_bytes())
run=subprocess.run(['python3','-B',str(Path.home()/'golive/analyze-interruptions-review-20261102.py'),str(out)],capture_output=True,text=True,timeout=20)
assert run.returncode==0,'Interruption analyzer failed'
interruptions=json.loads((out/'interruptions.json').read_text())
# Numeric engine/tool events only; never export customer words or tool payloads.
events=[]
for r in rows:
    msg=r.get('msg','')
    if not (msg.startswith('tools:') or msg.startswith('task:') or msg.startswith('hangup:') or msg=='turn: response latency'):continue
    clean={k:r[k] for k in ('time','msg','turn','task','origin_turn') if k in r}
    for k,v in r.items():
        if k.endswith('_ms') or k in ('failed','interrupting','memory_changed','readout_expected','first_readout_present','quote_result','status'):
            if v in (True,False,'true','false'):clean[k]=v
            else:
                try:clean[k]=int(v)
                except (ValueError,TypeError):pass
    events.append(clean)
save('tool-events.json',events)
openers=collections.Counter(r.get('source','preset') for r in rows if r.get('msg')=='speak: opened the answer from the phrase cache at the pause')
health=get('http://127.0.0.1:8765/healthz')
summary={'call':call,'session':c['session'],'build':c['build'],'started_at':c.get('started_at'),'ended_at':c.get('ended_at'),'audio_seconds':seconds,'audio_rate':rate,'archive_files_verified':5,'channels_with_signal':sum(signals),'transcript_lines':dict(speakers),'normal_excluding_quote_origins':lat.distribution([r['first_content_total_ms'] for r in ordinary],1200),'quotation_acknowledgments':lat.distribution([r['first_content_total_ms'] for r in acks],1200),'conversational_first_content':report['conversational_first_content'],'first_audio':report['first_audio'],'quote_readout':report['quote_readout'],'counts':report['counts'],'gate':report['gate'],'interruptions':interruptions['all_cuts'],'unpaired_cuts':interruptions['unpaired_cuts'],'opener_enqueues':dict(openers),'service_health':health,'acoustic_handset_verified':False,'backchannel_mode':'asr_assist'}
detector=[]
for r in rows:
    if not r.get('msg','').startswith('backchannel:'):continue
    detector.append({k:r[k] for k in ('time','msg','mode','reason','processed_chunks','utterance_start_ms','floor_probability','backchannel_probability','frames','input_lag_ms','asr_chars','pause_ms','original_pause_ms') if k in r})
summary['detector']={'connected':sum(r.get('msg')=='backchannel: connected' for r in rows),'processed_chunks':sum(int(r.get('processed_chunks',0)) for r in rows if r.get('msg')=='backchannel: stream closed'),'fallbacks':[r.get('reason') for r in rows if r.get('msg')=='backchannel: legacy fallback'],'observations':sum(r.get('msg')=='backchannel: floor evidence' for r in rows),'assists':sum(r.get('msg')=='backchannel: earlier ASR final requested' for r in rows)}
summary['focus_verdicts']=interruptions['focus_verdicts']
save('detector.json',detector)
save('summary.json',summary)
print(json.dumps(summary))
