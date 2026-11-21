#timeout 120
#pull /home/chuanming/golive-telephony/codex-latency/published-real-out-mvxo348d-w5pb.json published-real-out-mvxo348d-w5pb.json
set -eu
python3 -B - <<'PY'
from pathlib import Path
import hashlib,json,os,urllib.request
os.umask(0o077)
root=Path.home()/'golive-telephony'; journal=root/'journal'
payload=json.loads((Path.home()/'golive/activecall-real-review-payload.json').read_text())
call=payload['call']; assert call=='out-mvxo348d-w5pb'
receipt=root/('codex-latency/published-real-'+call+'.json')
assert not receipt.exists(),'Already attempted; inspect instead of replaying'
def get(url,token=None,data=None):
 req=urllib.request.Request(url,data=data,headers=({'Authorization':'Bearer '+token,'Content-Type':'application/json'} if token else {}))
 with urllib.request.urlopen(req,timeout=15) as r:
  assert r.status==200
  return json.load(r)
def atomic(p,raw):
 temp=p.with_name(p.name+'.real-review.tmp')
 with temp.open('xb') as f:f.write(raw)
 os.replace(temp,p)
c=get('http://192.168.71.9:8790/api/calls/'+call)
assert c['call']['id']==call and c['call']['build']=='45b5564' and c['call']['ended_at'] and c['archive']['archived'] and c['archive']['audio']
ip=journal/'issues.json';old=ip.read_bytes()
assert hashlib.sha256(old).hexdigest()=='30d86434256d33acbcb440ff1bf9e45344843f86cb17e5eaace70e9279ae7cdc','Issue file changed'
d=json.loads(old);matches=[x for x in d['issues'] if x['id']=='ISS-095']; assert len(matches)==1
assert call not in matches[0]['verified'] and matches[0]['status']=='open'
retro=journal/('retros/'+call+'.md');assert not retro.exists()
token=(Path.home()/'golive-desk/codex.token').read_text().strip()
me=get('http://127.0.0.1:8792/api/me',token);assert me['who']=='codex'
stream='http://127.0.0.1:8792/api/logs/telephony-latency';marker='activecall-real-review-'+call
prior=get(stream+'?limit=5000',token);assert not any(marker in x.get('text','') for x in prior['lines'])
state={'call':call,'stage':'prepared','normal_n':32,'content_p95_ms':1078,'content_max_ms':1081,'handset_verified':False}
atomic(receipt,(json.dumps(state,indent=2)+'\n').encode())
with (root/('codex-latency/issues-before-'+call+'.json')).open('xb') as f:f.write(old)
matches[0]['verified']+=' '+payload['note']
atomic(ip,(json.dumps(d,ensure_ascii=False,indent=1)+'\n').encode())
with retro.open('x') as f:f.write(payload['retro'])
fresh=get('http://192.168.71.9:8790/api/calls/'+call)
assert fresh['call']['id']==call and fresh.get('retrospective')
assert any(x.get('id')=='ISS-095' and call in x.get('verified','') for x in get('http://192.168.71.9:8790/api/issues')['issues'])
line=marker+' build=45b5564 real=true normal_n=32 normal_content_p50_ms=963.5 normal_content_p95_ms=1078 normal_content_max_ms=1081 first_audio_p95_ms=381 all_playback_cuts=14 detection_to_cut_p95_ms=686 vad_active_ms=320 quote_readouts=3 quote_readout_p95_ms=1084 calculation_poll_min_ms=3004 calculation_poll_max_ms=4509 archive_files_verified=5 server_gate=pass actual_handset_proven=false original_configs=preserved'
state['stage']='desk_post_requested';atomic(receipt,(json.dumps(state,indent=2)+'\n').encode())
posted=get(stream,token,json.dumps({'lines':[line]}).encode());assert posted.get('added')==1
check=get(stream+'?after='+str(prior['total']),token);assert sum(marker in x.get('text','') for x in check['lines'])==1
assert get('http://127.0.0.1:8792/api/me',token)['control']==me['control']
state.update(stage='verified',issue_id='ISS-095',issues_sha256=hashlib.sha256(ip.read_bytes()).hexdigest(),retro_sha256=hashlib.sha256(retro.read_bytes()).hexdigest(),desk_lines_added=1,desk_control_preserved=True)
atomic(receipt,(json.dumps(state,indent=2)+'\n').encode());print(json.dumps(state))
PY
