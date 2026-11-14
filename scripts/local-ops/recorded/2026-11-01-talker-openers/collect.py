from pathlib import Path
import collections,hashlib,importlib.util,json,shlex,subprocess
root=Path.home()/'golive-telephony/codex-latency/talker-ab-20261101-d'
spec=importlib.util.spec_from_file_location('latency',str(Path.home()/'golive/talker-call-latency.py'));lat=importlib.util.module_from_spec(spec);spec.loader.exec_module(lat)
out={'experiment':json.loads((root/'summary.json').read_text()),'modes':{}}
rows=json.loads((root/'benchmark-private.json').read_text())
for mode in ['preset','talker']:
 measured=[r for r in rows if r['stack']==mode and r['measured']]
 metas=[json.loads(p.read_text()) for p in (root/(mode+'-calls')).glob('*/session.json')]
 metas.sort(key=lambda m:m['created_at'])
 assert len(metas)==12,'unexpected session count: '+mode+' '+str(len(metas))
 ids={m['id'] for m in metas[2:]}
 selected=[];parsed=[]
 for line in (root/(mode+'.log')).read_text().splitlines(True):
  try:r=dict(w.split('=',1) for w in shlex.split(line) if '=' in w)
  except ValueError:continue
  if r.get('session') in ids:selected.append(line);parsed.append(r)
 report,_=lat.analyse(selected,380,1200)
 reasons=lat.gate_reasons(report,10,'both','separate')
 report['gate']={'passed':not reasons,'reasons':reasons,'quote_sla':'separate'}
 canonical={(r.get('session'),r.get('turn'),r.get('text')) for r in parsed if r.get('msg')=='transcript: assistant segment started'}
 enqueues=[r for r in parsed if r.get('msg')=='speak: opened the answer from the phrase cache at the pause']
 prep=[r for r in parsed if r.get('msg')=='opener: preparation']
 choices=[r for r in parsed if r.get('msg')=='opener: selection']
 out['modes'][mode]={'measured_sessions':len(measured),'errors':sum(bool(r.get('error')) for r in measured),'client_first_audio':lat.distribution([r['first_audio_ms'] for r in measured if not r.get('error')],1200),'server':report,'opener':{'prepared_statuses':dict(collections.Counter(r.get('status') for r in prep)),'selection_sources':dict(collections.Counter(r.get('source') for r in choices)),'selection_reasons':dict(collections.Counter(r.get('reason') for r in choices)),'enqueued_sources':dict(collections.Counter(r.get('source','preset') for r in enqueues)),'canonical_output_sources':dict(collections.Counter(r.get('source','preset') for r in enqueues if (r.get('session'),r.get('turn'),r.get('text')) in canonical)),'unverified_enqueues':sum((r.get('session'),r.get('turn'),r.get('text')) not in canonical for r in enqueues),'model':lat.distribution([int(r['model_ms']) for r in prep],800),'preparation':lat.distribution([int(r['prepare_ms']) for r in prep],800)}}
(root/'sanitized-comparison.json').write_text(json.dumps(out,indent=2)+'\n')
print(json.dumps(out))
