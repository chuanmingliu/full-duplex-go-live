"""Collect numeric simulator evidence after the retained attempt completes."""
import array
import collections
import hashlib
import json
import os
from pathlib import Path
import shlex
import subprocess
import time
import urllib.request
import wave

os.umask(0o077)
root = Path('/home/chuanming/golive-sip-ab/tool-ack-focused-20261101')
attempt = root / 'new-caller-7fc4237'
state = json.loads((attempt / 'attempt.json').read_text())
assert state.get('completed_at'), 'simulation is not verified terminal'
summaries = []
for idx, run in enumerate(state['runs'], 1):
    assert run['stage'] == 'ended' and run['adapter_exit'] == 0
    out = attempt / f"{idx:02d}-{run['version']}"
    for _ in range(30):
        with urllib.request.urlopen('http://192.168.71.9:9916/api/calls/' + run['call'], timeout=15) as response:
            detail = json.load(response)
        if detail.get('archive', {}).get('audio'):
            break
        time.sleep(2)
    else:
        raise RuntimeError('archive audio not ready')
    archive = Path(detail['archive']['dir']).resolve()
    assert archive.is_relative_to((root / 'state/archive').resolve())
    manifest = json.loads((archive / 'manifest.json').read_text())
    assert manifest['id'] == run['call']
    assert {'audio.wav', 'session.json', 'call.json', 'transcript.txt', 'golive.log'} <= manifest['files'].keys()
    for name, info in manifest['files'].items():
        path = (archive / name).resolve()
        assert path.is_relative_to(archive)
        assert path.stat().st_size == info['bytes']
        assert hashlib.sha256(path.read_bytes()).hexdigest() == info['sha256']
    call = json.loads((archive / 'call.json').read_text())
    session = json.loads((archive / 'session.json').read_text())
    assert call['session'] == session['id'] == manifest['session']
    assert session.get('closed_at') and not session['closed_at'].startswith('0001-')
    speakers = collections.Counter(x['who'] for x in call['transcript'] if x.get('text'))
    with wave.open(str(archive / 'audio.wav')) as wav:
        assert wav.getnchannels() == 2 and wav.getsampwidth() == 2
        seconds = wav.getnframes() / wav.getframerate()
        pcm = array.array('h', wav.readframes(wav.getnframes()))
        signals = sum(bool(any(pcm[side::2])) for side in [0, 1])
    gate = subprocess.run(['python3', '/tmp/new-caller-call-latency.py', str(archive / 'golive.log'), '--json', '--gate', '--target-ms', '1150', '--min-turns', '20', '--metric', 'both', '--quote-sla', 'separate'], capture_output=True, text=True)
    assert gate.returncode in [0, 1], 'latency analyzer failed'
    report = json.loads(gate.stdout)
    (out / 'latency.json').write_text(gate.stdout)
    timings = []
    protected = 0
    releases = 0
    retired = 0
    input_origins = 0
    unrooted_deliveries = 0
    release_reasons = collections.Counter()
    for line in (archive / 'golive.log').read_text().splitlines():
        try:
            row = dict(x.split('=', 1) for x in shlex.split(line) if '=' in x)
        except ValueError:
            continue
        if row.get('msg') in ['tools: task done', "tools: delivering a task's result"]:
            timings.append({k: row[k] for k in ['msg', 'task', 'took_ms', 'waited_for_gap_ms', 'origin_to_result_start_ms', 'memory_changed'] if k in row})
        if row.get('msg') == "tools: ready result no longer waits for its completed calling turn's ack":
            release_reasons[row.get('reason', 'playback_completed')] += 1
        if row.get('msg') in ["tools: delivering a task's result", 'tools: delivering a coalesced task result']:
            if row.get('origin_turn'):
                assert row.get('origin_timing_version') == '1', 'direct origin missing captured input provenance'
                assert int(row['origin_speech_end_ms']) >= 0 and int(row['origin_end_silence_ms']) >= 0
                input_origins += 1
            else:
                # Chained tasks can originate in a result turn. Preserve their
                # unverified analyzer status; never fabricate a caller origin.
                unrooted_deliveries += 1
        protected += row.get('msg') == 'tools: ready result yields to a newer caller answer'
        retired += row.get('msg') == 'tools: retired a delivered coalesced continuation'
        releases += row.get('msg') == "tools: ready result no longer waits for its completed calling turn's ack"
    phase_rows = []
    for line in (out / 'crm.log').read_text().splitlines():
        try:
            row = dict(x.split('=', 1) for x in shlex.split(line) if '=' in x)
        except ValueError:
            continue
        if row.get('msg') in ['crm: quote gate acquired', 'crm: quote phases']:
            phase_rows.append({k: row[k] for k in ['time', 'msg', 'memory_version', 'ahead', 'waited_ms', 'total_ms', 'before_ms', 'submit_ms', 'poll_http_ms', 'poll_wait_ms', 'polls', 'poll_errors', 'stale_results', 'unpriced_results', 'before_failed', 'phase', 'failed'] if k in row})
    receiver = json.loads((out / 'receiver-report.json').read_text())
    summary = dict(crm_version=run['crm_version'], crm_phases=phase_rows, call=run['call'], version=run['version'], run=run['run'],
        archive_files_verified=len(manifest['files']), audio_seconds=round(seconds, 3),
        speakers=dict(speakers), audio_channels_with_signal=signals,
        conversation_complete=bool(speakers['caller'] and speakers['assistant'] and signals == 2),
        receiver_gaps_ms=run['gaps_ms'], receiver_failure_count=len(receiver.get('failures', [])),
        gate_exit=gate.returncode, input_origin_links=input_origins, unrooted_deliveries=unrooted_deliveries, new_caller_protections=protected, ready_ack_releases=releases, retired_coalesced_continuations=retired, release_reasons=dict(release_reasons), tasks=timings,
        metrics={k: report.get(k) for k in ['counts', 'first_audio', 'first_content', 'conversational_first_content', 'tool_result', 'quote_readout', 'tool_result_rows', 'quote_readout_rows', 'gate']},
        physical_handset_capture=False, assumed_handset_overhead_ms=50, server_target_ms=1150)
    summaries.append(summary)
(attempt / 'verification.json').write_text(json.dumps(summaries, indent=2) + '\n')
token = Path('/home/chuanming/golive-desk/codex.token').read_text().strip()
note = 'ISS-094: newer-caller scheduling validation with engine 7fc4237 and CRM 3201ec4 complete; ' + '; '.join(
    row['version'] + ' call=' + row['call'] + ' server_gate=' + ('pass' if row['gate_exit'] == 0 else 'fail')
    for row in summaries) + '. Original services/configs preserved. No handset measurement or production promotion.'
req = urllib.request.Request('http://127.0.0.1:8792/api/logs/telephony-latency', data=note.encode(),
    headers={'Authorization': 'Bearer ' + token, 'Content-Type': 'text/plain; charset=utf-8'}, method='POST')
with urllib.request.urlopen(req, timeout=15) as response:
    assert response.status == 200
print(json.dumps(summaries), flush=True)
