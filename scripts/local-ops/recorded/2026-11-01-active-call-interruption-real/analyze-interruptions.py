import json,shlex,datetime,statistics,math,sys,collections
from pathlib import Path
p=Path(sys.argv[1]);c=json.loads((p/'call.json').read_text());session=c['session']
def stamp(r):return datetime.datetime.fromisoformat(r['time'].replace('Z','+00:00'))
def stats(v):
 v=sorted(v);return {'n':len(v),'p50_ms':statistics.median(v) if v else None,'p95_ms':v[math.ceil(.95*len(v))-1] if v else None,'max_ms':max(v) if v else None}
def yes(v):return v in (True,'true')
rows=[]
for line in (p/'golive.log').read_text().splitlines():
 try:r=json.loads(line) if line.lstrip().startswith('{') else dict(x.split('=',1) for x in shlex.split(line) if '=' in x)
 except ValueError:continue
 rows.append(r)
opened=None;cuts=[];opens=[];focus=[];unpaired=0;pending_cut=None;asr=None;other_cuts=[]
for r in rows:
 if r.get('session')!=session:continue
 m=r.get('msg')
 if m=='listen: utterance opened':
  opened={'opened_at':r['time'],'opened_barge_in':yes(r.get('barge_in')),'assistant_speaking_at_open':yes(r.get('assistant_speaking')),'vad_active_ms':int(r.get('active_ms',0)),'start_ms':int(r.get('start_ms',0)),'first_hypothesis_ms':None,'prefed_ms':None,'cut':False};opens.append(opened);asr=None
 elif m=='asr: stream opened' and opened and int(r.get('start_ms',-1))==opened['start_ms'] and opened['prefed_ms'] is None:
  asr=r.get('run');opened.update(prefed_ms=int(r.get('prefed_ms',0)),predialled=yes(r.get('predialled')),connected=yes(r.get('connected')),connect_ms=int(r.get('connect_ms',0)),asr_run=asr)
 elif m in ('asr: partial','asr: final') and opened and r.get('run')==asr and opened['first_hypothesis_ms'] is None:
  opened['first_hypothesis_ms']=round((stamp(r)-datetime.datetime.fromisoformat(opened['opened_at'])).total_seconds()*1000,3)
  opened['first_hypothesis_chars']=len(r.get('text',''))
 elif m=='focus: a sound over the answer was judged':
  focus.append({k:r.get(k) for k in ('time','verdict','primary_ms','voiced_ms','coverage','waited_ms')})
 elif m=='new query while answering: cutting the answer' and yes(r.get('speaking')) and not yes(r.get('barge_in')):
  other_cuts.append({'time':r['time'],'barge_in':False,'speaking':True,'nearest_open_age_ms':round((stamp(r)-datetime.datetime.fromisoformat(opened['opened_at'])).total_seconds()*1000,3) if opened else None})
 elif m=='new query while answering: cutting the answer' and yes(r.get('speaking')) and yes(r.get('barge_in')):
  if opened and not opened['cut']:
   opened['cut']=True
   cut=dict(opened,cut_at=r['time'],barge_in_at_cut=yes(r.get('barge_in')),open_to_cut_ms=round((stamp(r)-datetime.datetime.fromisoformat(opened['opened_at'])).total_seconds()*1000,3));assert cut['open_to_cut_ms']>=0
   cuts.append(cut);pending_cut=cut
  else:unpaired+=1
 elif m=='speak: turn truncated' and pending_cut:
  ms=round((stamp(r)-datetime.datetime.fromisoformat(pending_cut['cut_at'])).total_seconds()*1000,3)
  if 0<=ms<=1000:pending_cut['server_cut_to_truncation_log_ms']=ms;pending_cut['truncated_turn']=r.get('turn')
  pending_cut=None
result={'call':c['id'],'build':c['build'],'scope':'Server VAD-open to cut decision during playback; excludes speech onset before VAD and acoustic playback stop. Only explicit barge_in=true cut events; all preceding opens included even if playback began later. Non-barge cancellations retained separately, not paired as interruption latency.','all_cuts':stats([x['open_to_cut_ms'] for x in cuts]),'opened_during_playback':stats([x['open_to_cut_ms'] for x in cuts if x['opened_barge_in']]),'prefed_cuts':stats([x['open_to_cut_ms'] for x in cuts if x['prefed_ms'] and x['prefed_ms']>0]),'nonprefed_cuts':stats([x['open_to_cut_ms'] for x in cuts if not x['prefed_ms']]),'other_non_barge_cancellations':other_cuts,'unpaired_cuts':unpaired,'utterance_opens':len(opens),'prefed_utterance_opens':sum(bool(x['prefed_ms']) for x in opens),'focus_verdicts':dict(collections.Counter(x['verdict'] for x in focus)),'cuts':cuts,'opens':opens,'focus_decisions':focus}
(p/'interruptions.json').write_text(json.dumps(result,indent=2)+'\n')
print(json.dumps({k:v for k,v in result.items() if k not in ('cuts','opens','focus_decisions')},indent=2))
