#timeout 180
#pull /home/chuanming/golive-telephony/codex-latency/real-analysis-out-mvw03ge0-j0y3/audio.wav codex-real-out-mvw03ge0-j0y3-audio.wav
#pull /home/chuanming/golive-telephony/codex-latency/real-analysis-out-mvw03ge0-j0y3/call.json codex-real-out-mvw03ge0-j0y3-call.json
#pull /home/chuanming/golive-telephony/codex-latency/real-analysis-out-mvw03ge0-j0y3/session.json codex-real-out-mvw03ge0-j0y3-session.json
#pull /home/chuanming/golive-telephony/codex-latency/real-analysis-out-mvw03ge0-j0y3/transcript.txt codex-real-out-mvw03ge0-j0y3-transcript.txt
#pull /home/chuanming/golive-telephony/codex-latency/real-analysis-out-mvw03ge0-j0y3/golive.log codex-real-out-mvw03ge0-j0y3-golive.log
#pull /home/chuanming/golive-telephony/codex-latency/real-analysis-out-mvw03ge0-j0y3/manifest.json codex-real-out-mvw03ge0-j0y3-manifest.json
#pull /home/chuanming/golive-telephony/codex-latency/real-analysis-out-mvw03ge0-j0y3/strict.json codex-real-out-mvw03ge0-j0y3-strict.json
#pull /home/chuanming/golive-telephony/codex-latency/real-analysis-out-mvw03ge0-j0y3/summary.json codex-real-out-mvw03ge0-j0y3-summary.json
#pull /home/chuanming/golive-telephony/codex-latency/real-analysis-out-mvw03ge0-j0y3/api.json codex-real-out-mvw03ge0-j0y3-api.json
# Execute only after the user says done for this real call.
set -eu
cd "$HOME/golive-telephony"
python3 -B - <<'PY'
from pathlib import Path
import hashlib,json,os,subprocess,time,urllib.request,wave
os.umask(0o077)
call_id='out-mvw03ge0-j0y3';session_id=None
root=Path.cwd();deadline=time.monotonic()+120
while True:
 with urllib.request.urlopen('http://192.168.71.9:8790/api/calls/'+call_id,timeout=10) as r:d=json.load(r)
 c=d['call'];a=d.get('archive',{})
 if session_id is None:
  session_id=c['session']
  assert isinstance(session_id,str) and session_id.startswith('sess_')
 assert c['id']==call_id and c['session']==session_id and c['build']=='f842928'
 assert not d.get('sim_run'),'expected real call'
 if c.get('ended_at') and not c['ended_at'].startswith('0001-') and a.get('archived') and a.get('audio'):break
 if time.monotonic()>deadline:raise RuntimeError('Selected real call has not finished archiving; no analysis performed')
 time.sleep(5)
source=Path(a['dir']).resolve(strict=True)
assert source.is_relative_to((Path.home()/'golive-archive').resolve(strict=True))
manifest_raw=(source/'manifest.json').read_bytes()
manifest=json.loads(manifest_raw)
assert manifest['id']==call_id and manifest['session']==session_id and manifest['build']=='f842928'
names=('audio.wav','transcript.txt','call.json','session.json','golive.log')
def digest(raw):return hashlib.sha256(raw).hexdigest()
snapshots={}
for name in names:
 p=source/name;assert p.is_file() and not p.is_symlink()
 raw=p.read_bytes()
 assert len(raw)==manifest['files'][name]['bytes']>0
 assert digest(raw)==manifest['files'][name]['sha256']
 snapshots[name]=raw
analyzer_sha=digest((root/'scripts/call_latency.py').read_bytes())
assert analyzer_sha=='41e5c29c16c5bf0011fcdad8a535290796d561f3be88904962d62abb78c4a850','deployed analyzer changed'
saved=json.loads((source/'session.json').read_bytes())
assert saved['id']==session_id and saved.get('closed_at') and not saved['closed_at'].startswith('0001-')
with wave.open(str(source/'audio.wav'),'rb') as w:
 assert w.getnchannels()==2 and w.getsampwidth()==2 and w.getframerate()==16000
 seconds=w.getnframes()/w.getframerate()
result=subprocess.run(['python3','-B','scripts/call_latency.py',str(source/'golive.log'),'--gate','--metric','both','--target-ms','1200','--min-turns','20','--json'],capture_output=True,text=True,timeout=30)
assert result.returncode in (0,1),'strict analyzer failed'
strict=json.loads(result.stdout)
out=root/'codex-latency'/('real-analysis-'+call_id)
assert not out.exists() and not out.is_symlink(),'Existing analysis attempt requires inspection'
out.mkdir(mode=0o700)
def save(name,raw):
 with (out/name).open('xb') as f:f.write(raw);f.flush();os.fsync(f.fileno())
for name in names:save(name,snapshots[name])
save('manifest.json',manifest_raw)
save('api.json',(json.dumps(d,ensure_ascii=False,indent=2)+'\n').encode())
save('strict.json',(json.dumps(strict,indent=2)+'\n').encode())
summary={'kind':'real handset call; server-output evidence only','call':call_id,'session':session_id,'build':c['build'],'archive':str(source),'archive_manifest_sha256':digest(manifest_raw),'analyzer_sha256':analyzer_sha,'files':manifest['files'],'audio_seconds':seconds,'strict_gate':strict['gate'],'first_audio':strict['first_audio'],'first_content':strict['first_content'],'tool_result':strict['tool_result'],'quote_readout':strict['quote_readout'],'counts':strict['counts'],'handset_acoustics_analyzed':False,'handset_target_proven':False}
save('summary.json',(json.dumps(summary,indent=2)+'\n').encode())
print(json.dumps({k:v for k,v in summary.items() if k not in ('files',)}))
PY
