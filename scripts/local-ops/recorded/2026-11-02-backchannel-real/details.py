from pathlib import Path
import datetime, hashlib, json, shlex, urllib.request
root=Path.home()/'golive-telephony';call='out-mvzfq218-qbqy';out=root/'codex-latency'/('review-'+call)
summary=json.loads((out/'summary.json').read_text());session=summary['session']
def parse(line):
    try:return json.loads(line) if line.lstrip().startswith('{') else dict(w.split('=',1) for w in shlex.split(line) if '=' in w)
    except ValueError:return {}
def stamp(x):return datetime.datetime.fromisoformat(x.replace('Z','+00:00'))
rows=[parse(l) for l in (out/'golive.log').read_text().splitlines()];rows=[r for r in rows if r.get('session')==session]
traces=[]
for cut in json.loads((out/'interruptions.json').read_text())['cuts']:
    start=stamp(cut['opened_at']);end=stamp(cut['cut_at']);events=[]
    for r in rows:
        if not r.get('time') or not start<=stamp(r['time'])<=end:continue
        msg=r.get('msg','')
        if not (msg.startswith(('asr:','focus:','listen:','floor:','backchannel:')) or msg=='new query while answering: cutting the answer'):continue
        v={'offset_ms':round((stamp(r['time'])-start).total_seconds()*1000),'msg':msg}
        for k in ('run','verdict','primary_ms','voiced_ms','coverage','waited_ms','active_ms','barge_in','speaking','kept','connected','prefed_ms'):
            if k in r:v[k]=r[k]
        if 'text' in r:v['text_chars']=len(r['text'])
        events.append(v)
    traces.append({'open_to_cut_ms':cut['open_to_cut_ms'],'events':events})
phases=[]
for line in (root/'crm.out').read_text(errors='replace').splitlines():
    if call not in line:continue
    r=parse(line)
    if r.get('call')!=call or r.get('msg')!='crm: quote phases':continue
    phases.append({k:r[k] for k in ('time','ahead','failed','total_ms','before_ms','submit_ms','poll_http_ms','poll_wait_ms','polls','phase') if k in r})
quotes=[]
strict=json.loads((out/'strict.json').read_text())
for link in strict['tool_result_rows']:
    if not link['quotation']:continue
    task=link['task'];done=[r for r in rows if r.get('task')==task and r.get('msg')=='tools: task done'];delivery=[r for r in rows if r.get('task')==task and r.get('msg')=="tools: delivering a task's result"]
    readout=[r for r in strict['quote_readout_rows'] if r['task']==task]
    quotes.append({'task':task,'tool_execution_ms':int(done[0]['took_ms']) if len(done)==1 else None,'ready_to_delivery_wait_ms':int(delivery[0]['waited_for_gap_ms']) if len(delivery)==1 else None,'last_sound_to_result_content_ms':link['total_ms'],'readout':readout})
saved=json.loads((Path.home()/'golive-last-call.json').read_text());expected={'instructions':'639dcecf24dddc73274a7412c017e36258b51679c2fd3ad4e72a595453aa8d25','voice':'65d9d6c5c7c2d5c29e38b777edf8da1dbd764f67deeeaa230724967cafe4e684','greeting':'fa7c33a6b9c732413d932a7453c5c85d4936fcdd8515822bf92e3fc07e2f4df8'}
context_preserved=all(hashlib.sha256(saved.get(k,'').encode()).hexdigest()==v for k,v in expected.items())
result={'call':call,'interruption_traces':traces,'quote_phases':phases,'quote_breakdown':quotes,'saved_original_context_preserved':context_preserved,'enabled_trial_profile_preserved':hashlib.sha256((root/'configs/telephony.json').read_bytes()).hexdigest()=='c98dea3a562a9544d6e7242fda0e86621e0a514c941ccd3a66000c085dd483ae'}
# Correlate each assist with its exact utterance, not the next arbitrary cut.
assists=[];kept=[]
for i,r in enumerate(rows):
    if r.get('msg')=='listen: backchannel, not an interruption; keeping the floor':
        kept.append({'time':r['time'],'text_chars':len(r.get('text',''))})
    if r.get('msg')!='backchannel: earlier ASR final requested':continue
    start=int(r['utterance_start_ms']);open_index=next((j for j in range(i-1,-1,-1) if rows[j].get('msg')=='listen: utterance opened' and int(rows[j].get('start_ms',-1))==start),None)
    assert open_index is not None,'Assist missing matching utterance'
    end_index=next((j for j in range(i+1,len(rows)) if rows[j].get('msg')=='listen: utterance opened'),len(rows))
    began=stamp(rows[open_index]['time']);events=[]
    for event in rows[open_index:end_index]:
        msg=event.get('msg','')
        if not (msg.startswith(('backchannel:','asr:','listen:','focus:')) or msg=='new query while answering: cutting the answer'):continue
        clean={'offset_ms':round((stamp(event['time'])-began).total_seconds()*1000),'msg':msg}
        for k in ('run','verdict','primary_ms','voiced_ms','coverage','waited_ms','active_ms','barge_in','speaking','kept','pause_ms','original_pause_ms','utterance_start_ms'):
            if k in event:clean[k]=event[k]
        if 'text' in event:clean['text_chars']=len(event['text'])
        events.append(clean)
    assists.append({'utterance_start_ms':start,'opened_at':rows[open_index]['time'],'assist_after_open_ms':round((stamp(r['time'])-began).total_seconds()*1000),'events':events})
result['assisted_utterances']=assists
result['legacy_acknowledgments_kept']=kept
result['message_counts']={m:sum(r.get('msg')==m for r in rows) for m in ('listen: backchannel, not an interruption; keeping the floor','listen: not a backchannel; yielding the floor')}
(out/'details.json').write_text(json.dumps(result,indent=2)+'\n');print(json.dumps(result))
