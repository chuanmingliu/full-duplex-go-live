"""Offline model diagnostic on an authorized completed call; no server or dial."""
import ast
import hashlib
import json
from pathlib import Path
import shlex
import sys
import urllib.request

root = Path('/home/chuanming/golive-sip-ab/native-real-fb11e2c')
for port in (8765, 9875, 9885, 9895):
    with urllib.request.urlopen(f'http://127.0.0.1:{port}/v1/pool', timeout=5) as response:
        assert json.load(response)['in_use'] == 0, 'real call active; no compute probe'
with urllib.request.urlopen('http://127.0.0.1:8032/health', timeout=5) as response:
    live = json.load(response)
assert live['sessions'] == 0 and live['window_ms'] == 800 and live['threshold'] == .28
source = Path('/home/chuanming/edge/tsv/tsv_server.py')
raw = source.read_bytes()
assert hashlib.sha256(raw).hexdigest() == '98574d847eb0df4a6bc223bfc49dd8e1a6b5cb625ad55bfb1284351ccd13b4b2'
# Load the deployed scoring code, stopping before app construction/binding.
tree = ast.parse(raw)
body = []
for node in tree.body:
    if isinstance(node, ast.Assign) and any(isinstance(t, ast.Name) and t.id == 'app' for t in node.targets):
        break
    body.append(node)
assert len(body) < len(tree.body)
sys.argv = [str(source), '--threads', '1']
module = {'__name__': 'offline_focus_probe', '__file__': str(source)}
exec(compile(ast.Module(body=body, type_ignores=[]), str(source), 'exec'), module)
np = module['np']
rows = []
for line in (root/'state/golive.log').read_text().splitlines():
    if not line.startswith('time=2026-11-01T07:1'):
        continue
    row = dict(x.split('=', 1) for x in shlex.split(line) if '=' in x)
    if '2026-11-01T07:12:23' <= row.get('time', '') <= '2026-11-01T07:15:30':
        rows.append(row)
enrols = [r for r in rows if r.get('msg') == "focus: offering the caller's voice"]
assert len(enrols) == 6
opens = [r for r in rows if r.get('msg') == 'listen: utterance opened' and r.get('barge_in') == 'true']
assert len(opens) == 8  # seven interruptions plus the final farewell
pcm = (root/'state/store/sess_809c18cc09a0448ca2a07980/input.pcm').read_bytes()
audio = np.frombuffer(pcm, dtype='<i2').astype(np.float32) / 32768
results = []
for offset in (5132, 5152, 5172):
    session = module['Session']()
    session.buf = audio[offset * 16:]
    session.t0_ms = 0
    offered = 0
    snapshots = []
    for opened in opens:
        while offered < len(enrols) and enrols[offered]['time'] < opened['time']:
            e = enrols[offered]
            reply = session.do_enrol(int(e['from_ms']), int(e['to_ms']), save_as=None)
            assert reply is not None and not reply.get('saved'), 'enrolment failed or wrote profile'
            snapshots.append({k: reply[k] for k in ['voiced_ms', 'candidates', 'agreeing']})
            offered += 1
        assert session.enrol is not None
        start = int(opened['start_ms'])
        for window in (800, 600, 400):
            primary = other = 0
            accepted = None
            labels = []
            # Approximate input alignment only: recorder can remove repeated frames.
            for end in range((start//100+1)*100, start+1801, 100):
                x = session.slice(end-window, end)
                voiced = module['dbfs'](x[-1600:]) >= module['args'].voiced_db
                similarity = None
                if voiced and len(x) >= 4800:
                    embedding = module['embed'](x)
                    if embedding is not None:
                        similarity = float(np.dot(embedding, session.enrol))
                        duration = min(100, end-start)
                        if similarity >= .28: primary += duration
                        else: other += duration
                labels.append(dict(end_ms=end, similarity=None if similarity is None else round(similarity, 3)))
                if accepted is None and (primary >= 400 or (primary >= 160 and primary >= other)):
                    accepted = end
            results.append(dict(recorded_open_at=opened['time'], utterance_start_ms=start,
                                recorder_offset_ms=offset, window_ms=window,
                                acceptance_input_ms=accepted, labels=labels))
    print(json.dumps({'progress':'offset_complete', 'offset_ms':offset, 'enrolments':snapshots}), flush=True)
report = dict(scope='offline_model_window_sensitivity_not_live_or_handset_latency',
              call_id='280fdab1-4078-4627-bf79-4eef8f627719',
              source_sha256=hashlib.sha256(raw).hexdigest(),
              input_sha256=hashlib.sha256(pcm).hexdigest(),
              source_health=live, results=results,
              limitations=['Recorder-to-input-clock offset approximated at 5152 ms plus/minus 20 ms.',
                           'No network or ASR timing, tracker echo attribution, or live model queue replayed.',
                           'No independent caller/background ground-truth labels; shorter windows need negative controls.',
                           'One-thread offline compute; production has two threads. No runtime settings changed.'])
out = Path('/tmp/focus-window-replay-20261101.json')
with out.open('x') as stream: json.dump(report, stream, indent=2)
print(json.dumps({'completed':True,'report':str(out),'conditions':len(results)}))
