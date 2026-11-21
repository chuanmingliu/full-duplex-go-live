import datetime,json,math,statistics,urllib.request,shlex,sys
from pathlib import Path
CALL=sys.argv[1]
assert __import__('re').fullmatch(r'[A-Za-z0-9_-]{1,80}',CALL)
with urllib.request.urlopen('http://192.168.71.9:8790/api/calls/'+CALL,timeout=15) as r:data=json.load(r)
assert data['call']['id']==CALL and data['archive']['archived']
rows=[];pending=None;focus=[];unpaired=0;opens=0;parsed=0;prefeed=[]
for line in (Path(data['archive']['dir'])/'golive.log').read_text().splitlines():
    try:r=json.loads(line)
    except ValueError:
        try:r=dict(x.split("=",1) for x in shlex.split(line) if "=" in x)
        except ValueError:continue
    if r.get('session')!=data['call']['session']:continue
    message=r.get('msg')
    if 'prefed_ms' in r:
        prefeed.append({k:r.get(k) for k in ('predialled','connected','prefed_ms','connect_ms')})
    if message not in ('listen: utterance opened','new query while answering: cutting the answer','focus: a sound over the answer was judged'):continue
    parsed+=1
    t=datetime.datetime.fromisoformat(r['time'].replace('Z','+00:00'))
    if message=='listen: utterance opened':
        pending=(t,r) if r.get('barge_in') in (True,'true') else None
        if pending:opens+=1
    elif message=='focus: a sound over the answer was judged':
        focus.append({k:r.get(k) for k in ('verdict','primary_ms','voiced_ms','coverage','waited_ms')})
    elif r.get('barge_in') in (True,'true') and r.get('speaking') in (True,'true'):
        if pending:
            dt=(t-pending[0]).total_seconds()*1000
            assert dt>=0
            rows.append({'detection_to_cut_ms':round(dt,3),'vad_active_ms':pending[1].get('active_ms')});pending=None
        else:unpaired+=1
assert parsed>0, 'No relevant events parsed; do not report an empty result'
values=sorted(x['detection_to_cut_ms'] for x in rows)
print(json.dumps({'call':CALL,'scope':'Server VAD utterance-open to cut decision while playback was active; excludes pre-detection voice time, transport and audible playout stop. One cut per latest open; unpaired events retained separately.','barge_in_opens':opens,'paired_playback_cuts':len(rows),'unpaired_playback_cuts':unpaired,'p50_ms':statistics.median(values) if values else None,'p95_ms':values[max(0,math.ceil(len(values)*.95)-1)] if values else None,'max_ms':max(values) if values else None,'rows':rows,'focus_decisions':focus,'recognizer_openings':prefeed},indent=2))
