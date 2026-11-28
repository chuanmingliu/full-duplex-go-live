from pathlib import Path
import collections,hashlib,importlib.util,json,shlex,subprocess
root=Path.home()/'golive-telephony/codex-latency/preset-focus-ab-20261101'
spec=importlib.util.spec_from_file_location('latency',str(Path.home()/'golive/talker-call-latency.py'));lat=importlib.util.module_from_spec(spec);spec.loader.exec_module(lat)
out={'experiment':json.loads((root/'summary.json').read_text()),'modes':{}}
rows=json.loads((root/'benchmark-private.json').read_text())
for mode in ['preset','candidate']:
 measured=[r for r in rows if r['stack']==mode and r['measured']]
 metas=[json.loads(p.read_text()) for p in (root/(mode+'-calls')).glob('*/session.json')]
 metas.sort(key=lambda m:m['created_at'])
 assert len(metas)==22,'unexpected session count: '+mode+' '+str(len(metas))
 ids={m['id'] for m in metas[2:]}
 selected=[];parsed=[]
 for line in (root/(mode+'.log')).read_text().splitlines(True):
  try:r=dict(w.split('=',1) for w in shlex.split(line) if '=' in w)
  except ValueError:continue
  if r.get('session') in ids:selected.append(line);parsed.append(r)
 report,_=lat.analyse(selected,380,1200)
 reasons=lat.gate_reasons(report,20,'both','separate')
 report['gate']={'passed':not reasons,'reasons':reasons,'quote_sla':'separate','min_turns':20}
 canonical={(r.get('session'),r.get('turn'),r.get('text')) for r in parsed if r.get('msg')=='transcript: assistant segment started'}
 enqueues=[r for r in parsed if r.get('msg')=='speak: opened the answer from the phrase cache at the pause']
 prep=[r for r in parsed if r.get('msg')=='opener: preparation']
 choices=[r for r in parsed if r.get('msg')=='opener: selection']
 latency_rows=[{k:int(r[k]) for k in ['end_silence_ms','asr_final_ms','turn_start_ms','llm_first_token_ms','first_segment_ms','tts_first_audio_ms','first_content_ms'] if k in r} for r in parsed if r.get('msg')=='turn: response latency' and r.get('response_to_speech')=='true' and r.get('first_content_present')=='true' and r.get('truncated')=='false' and r.get('quote_result')!='true']
 out['modes'][mode]={'measured_sessions':len(measured),'errors':sum(bool(r.get('error')) for r in measured),'client_first_audio':lat.distribution([r['first_audio_ms'] for r in measured if not r.get('error')],1200),'server':report,'opener':{'prepared_statuses':dict(collections.Counter(r.get('status') for r in prep)),'selection_sources':dict(collections.Counter(r.get('source') for r in choices)),'selection_reasons':dict(collections.Counter(r.get('reason') for r in choices)),'enqueued_sources':dict(collections.Counter(r.get('source','preset') for r in enqueues)),'canonical_output_sources':dict(collections.Counter(r.get('source','preset') for r in enqueues if (r.get('session'),r.get('turn'),r.get('text')) in canonical)),'unverified_enqueues':sum((r.get('session'),r.get('turn'),r.get('text')) not in canonical for r in enqueues),'model':lat.distribution([int(r['model_ms']) for r in prep],800),'preparation':lat.distribution([int(r['prepare_ms']) for r in prep],800)}}
 out['modes'][mode]['normal_latency_components']=latency_rows
(root/'sanitized-comparison.json').write_text(json.dumps(out,indent=2)+'\n')
print(json.dumps({k:{'samples':v['measured_sessions'],'errors':v['errors'],'audio':v['client_first_audio'],'content':v['server']['conversational_first_content'],'gate':v['server']['gate']} for k,v in out['modes'].items()}))

# Full Active Call interruption evidence is a separate scope.
import urllib.request,datetime
base=Path.home()/'golive-telephony/codex-latency/activecall-preset-scheduling-baseline-20261101'
baseline=json.loads((base/'summary.json').read_text());call=baseline['call_id']
with urllib.request.urlopen('http://192.168.71.9:8790/api/calls/'+call,timeout=10) as f:data=json.load(f)
assert data['call']['id']==call and data['archive']['archived']
archive=Path(data['archive']['dir']).resolve();assert archive.is_relative_to(Path.home()/'golive-archive')
last=None;cuts=[];unpaired=0
for line in (archive/'golive.log').read_text().splitlines():
 try:r=json.loads(line) if line.lstrip().startswith('{') else dict(x.split('=',1) for x in shlex.split(line) if '=' in x)
 except ValueError:continue
 if r.get('session')!=baseline['session']:continue
 msg=r.get('msg');yes=lambda k:r.get(k) in (True,'true')
 if msg=='listen: utterance opened':last=datetime.datetime.fromisoformat(r['time']) if yes('barge_in') else None
 elif msg=='new query while answering: cutting the answer' and yes('barge_in') and yes('speaking'):
  if last is None:unpaired+=1
  else:cuts.append(round((datetime.datetime.fromisoformat(r['time'])-last).total_seconds()*1000,3));last=None
result={'call':call,'scope':'full Active Call baseline; VAD open to server cut, excludes pre-detection and handset stop','cuts_ms':cuts,'distribution':lat.distribution(cuts,1200),'unpaired':unpaired}
(root/'activecall-baseline-cuts.json').write_text(json.dumps(result,indent=2)+'\n');print(json.dumps(result))
