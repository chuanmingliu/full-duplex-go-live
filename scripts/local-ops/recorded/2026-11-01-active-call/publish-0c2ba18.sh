#timeout 120
#pull /home/chuanming/golive-telephony/codex-latency/published-activecall-0c2ba18.json published-activecall-0c2ba18.json
set -eu
python3 -B - <<'PUBLISH'
import hashlib,json,os,urllib.request
from pathlib import Path
os.umask(0o077)
root=Path.home()/'golive-telephony'; journal=root/'journal'
receipt=root/'codex-latency/published-activecall-0c2ba18.json'
assert not receipt.exists(), 'Publication attempted already; inspect receipt, never replay'
def sha(p):return hashlib.sha256(p.read_bytes()).hexdigest()
def atomic(p,b):
 t=p.with_name(p.name+'.codex-publish.tmp')
 with t.open('xb') as f:f.write(b)
 os.replace(t,p)
def get(url,token=None,data=None):
 q=urllib.request.Request(url,data=data,headers=({'Authorization':'Bearer '+token,'Content-Type':'application/json'} if token else {}))
 with urllib.request.urlopen(q,timeout=15) as r:
  assert r.status==200
  return json.load(r)
payload={'issues': [{'id': 'ISS-099', 'title': 'Exact caller duration rounds below the interruption threshold', 'area': 'focus', 'status': 'fixed', 'found': '2026-11-01', 'found_on': 'Controlled speaker-frame boundary tests', 'found_call': '', 'symptom': 'Exactly 400 ms of caller speech can be treated as 399 ms and fail the existing barge-in gate.', 'cause': 'Converting primary fraction times total voiced duration back to an integer loses precision for some exact frame sums.', 'fix': 'Carry the tracker’s exact primary frame duration into all three caller acceptance decisions; keep thresholds, masks and echo exclusions.', 'commits': ['45b5564'], 'verified': 'Regression cases cover 400 ms among 820/940/1160 ms other speech, 399 ms rejection, 160 ms dominance boundaries, held-floor acceptance, masks, clipped overlap and echo. No real-call improvement is claimed. Active Call deployment verified 2026-11-01; coverage and scripted simulations retained all output records. Scripted VAD-open to cut decisions were 154/169 ms; this is neither a matched causal comparison nor handset-stop evidence.', 'verified_call': 'out-mvxksnvy-4i3q', 'effect': 'Avoids a false threshold rejection; live delay reduction remains unmeasured.'}, {'id': 'ISS-100', 'title': 'Later system utterance overwrites the initial call greeting', 'area': 'journal', 'status': 'fixed', 'found': '2026-11-01', 'found_call': 'out-mvxksnvy-4i3q', 'symptom': 'Post-call config verification reports a changed greeting although the initial opening matches the saved 41-character greeting.', 'cause': 'The journal assigned Greeting for every speak: opening line event; a later 12-character system utterance replaced it.', 'fix': 'Keep the first nonempty opening as the call greeting; still classify every system opening outside the response SLA.', 'verified': 'Focused Journal and cmd/journal race tests pass, including a regression with separate opening and farewell events. Runtime is still 45b5564; this journal-only follow-up is not deployed.', 'effect': 'Restores initial-greeting provenance; no speech or latency change.', 'commits': []}], 'retros': {'out-mvxk9u0n-11lw.md': '# Active Call simulation out-mvxk9u0n-11lw — 2026-11-01\n\nBuild `9547d22`; original GoLive configuration and saved prompt/voice/greeting.\nScenario `kb-coverage-nerd`. No real recipient was dialed. Permanent archive:\nfive files verified, stereo signal on both channels, 289.356 seconds, 33 / 34\ncaller / assistant transcript lines. Signal does not prove intelligibility.\n\n33 normal response samples: content p50 / p95 / max **945 / 1157 / 1279 ms**;\nfirst audio p95 **380 ms**, including openers. No missing, duplicate or\nunverified output records. Baseline strict server gate passes; adding the owner’s assumed 50 ms gives p95 1207 ms, an estimate only.\nNo quotation result samples; calculation settings were unchanged.\n\nThe simulator reported two TTS chunk failure(s); this is not a clean quality\npass. The generated dialogue differs from the comparison run, so p95 changes\nare not causal proof. Two paired server VAD-open to playback-cut decisions:\n**229 / 371 ms**, excluding pre-detection voice and audible stop latency.\nNo actual handset latency is established.\n\nSee `docs/telephony/2026-11-01-active-call-rollout.md` for deployment, comparison,\narchive reconciliation and exact evidence. The sub-1200 ms handset goal remains open.\n', 'out-mvxkn1v4-jeyp.md': '# Active Call simulation out-mvxkn1v4-jeyp — 2026-11-01\n\nBuild `45b5564`; original GoLive configuration and saved prompt/voice/greeting.\nScenario `kb-coverage-nerd`. No real recipient was dialed. Permanent archive:\nfive files verified, stereo signal on both channels, 243.019 seconds, 33 / 33\ncaller / assistant transcript lines. Signal does not prove intelligibility.\n\n31 normal response samples: content p50 / p95 / max **933 / 1200 / 1300 ms**;\nfirst audio p95 **380 ms**, including openers. No missing, duplicate or\nunverified output records. Candidate strict server gate fails because 1200 ms is not below 1200 ms. Adding the assumed 50 ms gives 1250 ms, an estimate only.\nNo quotation result samples; calculation settings were unchanged.\n\nThe simulator reported one TTS chunk failure(s); this is not a clean quality\npass. The generated dialogue differs from the comparison run, so p95 changes\nare not causal proof. Two paired server VAD-open to playback-cut decisions:\n**185 / 146 ms**, excluding pre-detection voice and audible stop latency.\nNo actual handset latency is established.\n\nSee `docs/telephony/2026-11-01-active-call-rollout.md` for deployment, comparison,\narchive reconciliation and exact evidence. The sub-1200 ms handset goal remains open.\n', 'out-mvxksnvy-4i3q.md': '# Active Call scripted interruptions — 2026-11-01\n\nCall `out-mvxksnvy-4i3q`, build `45b5564`, original GoLive configuration,\nscenario `set-a-01-barge-in`. Both scripted cut-ins completed. No simulator\nwarnings or failures. No real recipient was dialed.\n\nAll five permanent files verified; 16.631 seconds, stereo signal on both\nchannels, 3 caller / 4 assistant transcript lines. Two paired server VAD-open\nto active-playback cut decisions were **154 / 169 ms**. These exclude the\n320 ms active-input detection window and transport / audible playout stop.\n\nNo qualifying normal content samples: the minimum-20 gate fails. One linked\nquotation readout measured 844 ms from its caller endpoint; this is not a\nseparate quotation calculation-duration measurement. Quotation delay remains\nunchanged. First audio including opener was 380 ms. No missing, duplicate or\nunverified output records. No handset target claim is made.\n\nInitial verification stopped on the Journal greeting field. Read-only collection\nproved the first 41-character opening exactly matched the saved greeting;\na later 12-character system opening overwrote the derived field. ISS-100 fixes\nthe Journal parser to retain the initial greeting. Call audio was not altered,\nthe failed receipt is retained, and no redial was made. The parser fix is not\npart of deployed build 45b5564.\n'}}

p=journal/'issues.json'
assert sha(p)=='163abe64123f80039bec9f926dc527a17778276aea4e09bddfcc7ae535fd9bd7', 'Issue file changed; merge again'
existing=json.loads(p.read_text()); ids={i['id'] for i in existing['issues']}
assert not any(i['id'] in ids for i in payload['issues']), 'Issue IDs already exist'
for name,text in payload['retros'].items():
 assert name.startswith('out-') and '/' not in name and name.endswith('.md')
 assert not (journal/'retros'/name).exists(), 'Retro exists'
token=(Path.home()/'golive-desk/codex.token').read_text().strip()
me=get('http://127.0.0.1:8792/api/me',token);assert me.get('who')=='codex'
control=me.get('control')
state={'stage':'prepared','build':'45b5564','source_commit':'0c2ba18','previous_issues_sha256':sha(p),'calls':list(payload['retros'])}
atomic(receipt,(json.dumps(state,indent=2)+'\n').encode())
backup=root/'codex-latency/issues-before-activecall-0c2ba18.json'
with backup.open('xb') as f:f.write(p.read_bytes())
existing['issues'].extend(payload['issues'])
for i in existing['issues']:
 if i['id'] in {'ISS-091','ISS-092','ISS-093','ISS-094','ISS-095','ISS-096','ISS-097','ISS-098'}:
  i['verified']=i.get('verified','')+' Shared changes deployed on original-config Active Call as 45b5564; normal content p95 1200 ms across 31 samples fails strict below-1200 gate. Scripted cut-ins completed; actual handset verification remains pending.'
atomic(p,(json.dumps(existing,ensure_ascii=False,indent=1)+'\n').encode())
for name,text in payload['retros'].items():
 dest=journal/'retros'/name
 with dest.open('x') as f:f.write(text)
state['stage']='journal_files_written';atomic(receipt,(json.dumps(state,indent=2)+'\n').encode())
for name in payload['retros']:
 d=get('http://192.168.71.9:8790/api/calls/'+name[:-3])
 assert d['call']['id']==name[:-3] and d.get('retrospective'), 'Journal retro not visible'
stream='http://127.0.0.1:8792/api/logs/telephony-latency'
marker='activecall-rollout-0c2ba18'
prior=get(stream+'?limit=5000',token)
assert not any(marker in x.get('text','') for x in prior.get('lines',[])), 'Work log already published'
line=marker+' build=45b5564 baseline=out-mvxk9u0n-11lw normal_n=33 content_p95_ms=1157 candidate=out-mvxkn1v4-jeyp normal_n=31 content_p95_ms=1200 strict_gate=fail interrupt=out-mvxksnvy-4i3q scripted_completed=2 detection_to_cut_ms=154,169 handset_proven=false original_configs=preserved rollback=retained journal_greeting_fix=0c2ba18_not_deployed'
state['stage']='desk_post_requested';atomic(receipt,(json.dumps(state,indent=2)+'\n').encode())
r=get(stream,token,json.dumps({'lines':[line]}).encode());assert r.get('added')==1
new=get(stream+'?after='+str(prior['total']),token)
assert sum(marker in x.get('text','') for x in new.get('lines',[]))==1
assert get('http://127.0.0.1:8792/api/me',token).get('control')==control
state.update(stage='verified',journal_retro_count=3,desk_lines_added=1,issues_sha256=sha(p),desk_control_preserved=True)
atomic(receipt,(json.dumps(state,indent=2)+'\n').encode());print(json.dumps(state))
PUBLISH
