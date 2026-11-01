#timeout 120
#pull /home/chuanming/golive-telephony/codex-latency/published-activecall-repeat.json published-activecall-repeat.json
set -eu
python3 -B - <<'PUBLISH'
import hashlib,json,os,urllib.request
from pathlib import Path
os.umask(0o077)
root=Path.home()/'golive-telephony';journal=root/'journal';receipt=root/'codex-latency/published-activecall-repeat.json'
assert not receipt.exists(), 'Already attempted; inspect instead of replaying'
def sha(p):return hashlib.sha256(p.read_bytes()).hexdigest()
def atomic(p,raw):
 t=p.with_name(p.name+'.repeat.tmp')
 with t.open('xb') as f:f.write(raw)
 os.replace(t,p)
def get(url,token=None,data=None):
 q=urllib.request.Request(url,data=data,headers=({'Authorization':'Bearer '+token,'Content-Type':'application/json'} if token else {}))
 with urllib.request.urlopen(q,timeout=15) as r:
  assert r.status==200
  return json.load(r)
payload={'issue': {'id': 'ISS-100', 'title': 'Later system utterance overwrites the initial call greeting', 'area': 'journal', 'status': 'fixed', 'found': '2026-11-01', 'found_call': 'out-mvxksnvy-4i3q', 'symptom': 'Post-call config verification reports a changed greeting although the initial opening matches the saved 41-character greeting.', 'cause': 'The journal assigned Greeting for every speak: opening line event; a later 12-character system utterance replaced it.', 'fix': 'Keep the first nonempty opening as the call greeting; still classify every system opening outside the response SLA.', 'verified': 'Regression fails against the deployed 45b5564 parser and passes with the fix; Journal and cmd/journal race tests pass. Full push/PR CI 41289836788/41289841450 passed. Journal-only rollout verified on 2026-11-01: three greeting summaries corrected from canonical initial openings, 654 existing raw files across 178 archived entries unchanged, original configurations and all other services preserved. Engine remains 45b5564.', 'effect': 'Restores initial-greeting provenance; no speech or latency change.', 'commits': ['0c2ba18']}, 'retros': {'out-mvxksnvy-4i3q.md': '# Active Call scripted interruptions — 2026-11-01\n\nCall `out-mvxksnvy-4i3q`, build `45b5564`, original GoLive configuration,\nscenario `set-a-01-barge-in`. Both scripted cut-ins completed. No simulator\nwarnings or failures. No real recipient was dialed.\n\nAll five permanent files verified; 16.631 seconds, stereo signal on both\nchannels, 3 caller / 4 assistant transcript lines. Two paired server VAD-open\nto active-playback cut decisions were **154 / 169 ms**. These exclude the\n320 ms active-input detection window and transport / audible playout stop.\n\nNo qualifying normal content samples: the minimum-20 gate fails. One linked\nquotation readout measured 844 ms from its caller endpoint; this is not a\nseparate quotation calculation-duration measurement. Quotation delay remains\nunchanged. First audio including opener was 380 ms. No missing, duplicate or\nunverified output records. No handset target claim is made.\n\nInitial verification stopped on the Journal greeting field. Read-only collection\nproved the first 41-character opening exactly matched the saved greeting;\na later 12-character system opening overwrote the derived field. ISS-100 fixes\nthe Journal parser to retain the initial greeting. Call audio was not altered,\nthe failed receipt is retained, and no redial was made. Journal-only fix `0c2ba18` is now deployed after full CI. The phone engine\nremains `45b5564`; this metadata correction does not change its latency.\n', 'out-mvxlfkke-kr5y.md': "# Original GoLive Active Call repeat — 2026-11-01\n\nCall `out-mvxlfkke-kr5y`, run `sim-437b48b131514b62dd78a571`, engine `45b5564`.\nThe same `kb-coverage-nerd` scenario and saved prompt, voice and greeting were\nused; the intervening update changed only Journal to `0c2ba18`.\nNo real recipient was dialed.\n\n30 normal responses: content p50 / p95 / max **787 / 1049 / 1147 ms**.\nFirst audio including opener: **380 / 380 / 404 ms**. The strict server-output\ngate passes. Adding the owner's assumed 50 ms gives content p95 **1099 ms**;\nthis remains an estimate, not acoustic handset verification. No quotation\nresult/readout samples; original calculation delay is unchanged.\n\nAll five permanent files match their manifest. Stereo PCM16 has signal on both\nchannels over 296.954 seconds, with 31 caller and 32 assistant transcript lines.\nNo missing, duplicate, malformed or unverified output records. The simulator\nreported no warnings or failures; semantic quality was not independently scored.\n\nSeven answered records were interrupted. Only one paired server VAD-open to\ncut decision occurred while playback was active: **336 ms**, after a 320 ms\nactive-input detection window. No unpaired playback cuts. This is not audible\nhandset-stop latency and one event is not a representative interruption p95.\n\nThe previous candidate run on the same engine had content p95 1200 ms. The\nnew 1049 ms result shows run-to-run variation; it is not a Journal-induced\nspeedup or a reason to hide the previous miss. Results remain separate.\nHandset qualification still needs a clean acoustic capture and twenty valid\nnormal responses on the configured real phone.\n"}, 'old_retro_sha256': '9732382ce03227e727bb4203803f90a3a5042f83a3c9150e72b52790b6ab2701'}

p=journal/'issues.json';assert sha(p)=='7a5e09246e8c1d8919bcecd03506c99cec2c9299d13ed10ad57b61bc3fcfdbf1', 'Issues changed'
old=journal/'retros/out-mvxksnvy-4i3q.md';new=journal/'retros/out-mvxlfkke-kr5y.md'
assert sha(old)==payload['old_retro_sha256'] and not new.exists(), 'Retro state changed'
deploy=json.loads((root/'codex-latency/deployment-journal-0c2ba18.json').read_text());assert deploy['stage']=='verified'
sim=json.loads((root/'codex-latency/activecall-repeat-45b5564-20261101-a/summary.json').read_text())
assert sim['stage']=='verified' and sim['call_id']=='out-mvxlfkke-kr5y' and sim['gate']['passed'] and sim['conversational_first_content']['p95_ms']==1049
state={'stage':'prepared','engine':'45b5564','journal':'0c2ba18','repeat_call':sim['call_id'],'normal_n':30,'content_p95_ms':1049,'handset_proven':False}
atomic(receipt,(json.dumps(state,indent=2)+'\n').encode())
backup=root/'codex-latency/issues-before-activecall-repeat.json'
with backup.open('xb') as f:f.write(p.read_bytes())
d=json.loads(p.read_text());matches=[n for n,x in enumerate(d['issues']) if x['id']=='ISS-100'];assert len(matches)==1
d['issues'][matches[0]]=payload['issue'];atomic(p,(json.dumps(d,ensure_ascii=False,indent=1)+'\n').encode())
atomic(old,payload['retros'][old.name].encode())
with new.open('x') as f:f.write(payload['retros'][new.name])
for name in payload['retros']:
 c=get('http://192.168.71.9:8790/api/calls/'+name[:-3]);assert c['call']['id']==name[:-3] and c.get('retrospective')
token=(Path.home()/'golive-desk/codex.token').read_text().strip();me=get('http://127.0.0.1:8792/api/me',token);assert me['who']=='codex'
stream='http://127.0.0.1:8792/api/logs/telephony-latency';marker='activecall-repeat-journal-0c2ba18'
prior=get(stream+'?limit=5000',token);assert not any(marker in x.get('text','') for x in prior['lines'])
line=marker+' engine=45b5564 journal=0c2ba18 journal_ci=passed archive_raw_files_preserved=654 greeting_summaries_corrected=3 call=out-mvxlfkke-kr5y normal_n=30 content_p50_ms=787 content_p95_ms=1049 content_max_ms=1147 first_audio_p95_ms=380 quote_results=0 warnings=0 server_gate=pass assumed_handset_p95_ms=1099 actual_handset_proven=false playback_cut_samples=1 detection_to_cut_ms=336 original_configs=preserved'
state['stage']='desk_post_requested';atomic(receipt,(json.dumps(state,indent=2)+'\n').encode())
r=get(stream,token,json.dumps({'lines':[line]}).encode());assert r.get('added')==1
out=get(stream+'?after='+str(prior['total']),token);assert sum(marker in x.get('text','') for x in out['lines'])==1
assert get('http://127.0.0.1:8792/api/me',token)['control']==me['control']
state.update(stage='verified',issues_sha256=sha(p),retros_verified=2,desk_lines_added=1,desk_control_preserved=True)
atomic(receipt,(json.dumps(state,indent=2)+'\n').encode());print(json.dumps(state))
PUBLISH
