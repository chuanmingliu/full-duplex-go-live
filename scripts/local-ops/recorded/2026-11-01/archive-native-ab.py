import os,json,subprocess,time,urllib.request,hashlib,wave,array,socket
from pathlib import Path
from collections import Counter
os.umask(0o077)
r=Path('/home/chuanming/golive-sip-ab/native-ab-20261031')
assert json.load(urllib.request.urlopen('http://127.0.0.1:9895/v1/pool'))['in_use']==0
states=[json.loads(p.read_text()) for p in r.glob('attempt-v*.json')]
assert all(s.get('completed_at') for s in states)
calls=[x for s in states for x in s['runs'] if x['mode']!='activecall']
(r/'sim-dials.jsonl').write_text(''.join(json.dumps({'call':x['call'],'run':x['run']})+'\n' for x in calls))
with socket.socket() as s:s.bind(('127.0.0.1',9896))
cmd=[str(r/'journal-f758708'),'-addr','127.0.0.1:9896','-log',str(r/'golive.log'),'-store',str(r/'store'),'-data',str(r/'journal'),'-archive',str(r/'archive'),'-adapter-records',str(r/'records'),'-notes',str(r/'notes'),'-sim-dials',str(r/'sim-dials.jsonl'),'-last-call',str(r/'private/saved-call.json')]
log=(r/'archive-job.log').open('ab');p=subprocess.Popen(cmd,stdout=log,stderr=subprocess.STDOUT)
results=[]
try:
 for _ in range(30):
  try:urllib.request.urlopen('http://127.0.0.1:9896/api/overview',timeout=2).close();break
  except OSError:time.sleep(.2)
 for x in calls:
  cid=x['call'];d=json.load(urllib.request.urlopen('http://127.0.0.1:9896/api/calls/'+cid,timeout=30)); a=d.get('archive',{});assert a.get('archived') and a.get('audio'),cid
  path=Path(a['dir']);assert path.is_relative_to(r/'archive')
  m=json.loads((path/'manifest.json').read_text());assert m['id']==cid
  for name,info in m['files'].items():
   file=path/name;assert file.is_relative_to(path);b=file.read_bytes();assert len(b)==info['bytes'] and hashlib.sha256(b).hexdigest()==info['sha256']
  speakers=Counter(t['who'] for t in d['call'].get('transcript',[]))
  with wave.open(str(path/'audio.wav')) as w:
   assert w.getnchannels()==2 and w.getsampwidth()==2
   rate=w.getframerate();data=array.array('h',w.readframes(w.getnframes()));signal=[any(data[i::2]) for i in (0,1)]
  result=dict(call=cid,run=x['run'],files_verified=len(m['files']),audio_rate=rate,channels_with_signal=sum(signal),transcript_counts=speakers,archive=str(path))
  (r/'journal'/('detail-'+cid+'.json')).write_text(json.dumps(d))
  results.append(result)
finally:
 p.terminate();p.wait(timeout=10)
(r/'archive-verification.json').write_text(json.dumps(results,indent=2)+'\n')
print(json.dumps(results,indent=2))
