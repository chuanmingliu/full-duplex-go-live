import json,hashlib,wave,array,collections,urllib.request,re
from pathlib import Path
summaries=[]
for suffix,port in [('ab',9906),('focused',9916)]:
 root=Path('/home/chuanming/golive-sip-ab/tool-ack-'+suffix+'-20261101')
 if not (root/'attempt.json').exists():continue
 state=json.loads((root/'attempt.json').read_text())
 for idx,row in enumerate(state['runs'],1):
  if row['stage']!='ended':continue
  with urllib.request.urlopen('http://192.168.71.9:'+str(port)+'/api/calls/'+row['call'],timeout=15) as q:detail=json.load(q)
  archive=Path(detail['archive']['dir']);assert archive.is_relative_to(root/'state/archive')
  assert detail['archive']['archived'] and detail['archive']['audio']
  manifest=json.loads((archive/'manifest.json').read_text());assert manifest['id']==row['call']
  required=['audio.wav','call.json','session.json','transcript.txt','golive.log']
  for name,entry in manifest['files'].items():
   f=archive/name;assert f.resolve().is_relative_to(archive.resolve());assert f.stat().st_size==entry['bytes'];assert hashlib.sha256(f.read_bytes()).hexdigest()==entry['sha256']
  assert all(n in manifest['files'] for n in required)
  saved=json.loads((archive/'call.json').read_text());session=json.loads((archive/'session.json').read_text());assert session['closed_at'] and session['id']==saved['session']==manifest['session']
  counts=collections.Counter(x['who'] for x in saved['transcript'] if x.get('text'))
  with wave.open(str(archive/'audio.wav'),'rb') as w:
   assert w.getnchannels()==2 and w.getsampwidth()==2
   duration=w.getnframes()/w.getframerate();pcm=array.array('h',w.readframes(w.getnframes()));channels_with_signal=int(any(pcm[::2]))+int(any(pcm[1::2]))
  tasks=[];acks=[]
  for line in (archive/'golive.log').read_text().splitlines():
   d={k:(q or b) for k,q,b in re.findall(r'(\w+)=(?:"((?:\\.|[^"\\])*)"|(\S+))',line)}
   if d.get('msg') in ['tools: task done',"tools: delivering a task's result"]:tasks.append({k:v for k,v in d.items() if k in ['msg','task','took_ms','waited_for_gap_ms','origin_to_result_start_ms']})
   if d.get('msg')=='tools: saying the ack in place of the model':acks.append({k:d.get(k) for k in ['task','chars']})
  receiver=json.loads((root/('%02d-%s'%(idx,row['mode']))/'receiver-report.json').read_text())
  summaries.append(dict(suite=suffix,index=idx,mode=row['mode'],call=row['call'],run=row['run'],archive=str(archive),audio_seconds=duration,speakers=dict(counts),channels_with_signal=channels_with_signal,capture_complete=bool(counts["caller"] and counts["assistant"] and channels_with_signal==2),files_verified=len(manifest['files']),gaps_ms=row['gaps_ms'],failures=receiver.get('failures',[]),termination_reason=row['termination_reason'],tasks=tasks,acknowledgments=acks))
 (root/'verification.json').write_text(json.dumps([x for x in summaries if x['suite']==suffix],indent=2)+'\n')
print(json.dumps(summaries))
