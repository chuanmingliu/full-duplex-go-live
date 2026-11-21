from pathlib import Path
import datetime, hashlib, json, shlex, urllib.request
root=Path.home()/'golive-telephony';call='out-mvykfqfn-wibn';out=root/'codex-latency'/('review-'+call)
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
        if not (msg.startswith(('asr:','focus:','listen:','floor:')) or msg=='new query while answering: cutting the answer'):continue
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
result={'call':call,'interruption_traces':traces,'quote_phases':phases,'quote_breakdown':quotes,'saved_original_context_preserved':context_preserved,'original_profile_preserved':hashlib.sha256((root/'configs/telephony.json').read_bytes()).hexdigest()=='072ab8dbda962eff682fa56fcd049f70cd3717530e64a06bec97e66618878e7d'}
(out/'details.json').write_text(json.dumps(result,indent=2)+'\n');print(json.dumps(result))
