"""One isolated, simulator-only comparison; reuse the existing Journal reader."""
import datetime
import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import socket
import subprocess
import time
import urllib.request
import uuid

os.umask(0o077)
root = Path('/home/chuanming/golive-sip-ab/tool-ack-focused-20261101')
attempt = root / 'receipt-ab-2891b52-v3'
assert not attempt.exists(), 'attempt exists; inspect its live handles instead of retrying'
assert json.loads((root / 'new-caller-7fc4237/attempt.json').read_text()).get('completed_at')

def envfile(path):
    return {k: v.strip().strip('\"\'') for line in path.read_text().splitlines()
            if line and not line.startswith('#') and '=' in line
            for k, v in [line.split('=', 1)]}

def request(url, body=None, token=None, key=None):
    headers = {'Authorization': 'Bearer ' + token} if token else {}
    if body is not None:
        headers['Content-Type'] = 'application/json'
    if key:
        headers['Idempotency-Key'] = key
    req = urllib.request.Request(url, data=json.dumps(body).encode() if body is not None else None, headers=headers)
    with urllib.request.urlopen(req, timeout=40) as response:
        return json.load(response)

token = envfile(Path('/home/chuanming/golive-sim/tokens.env'))['SIM_CONTROL_TOKEN']
def sim(path, body=None, key=None):
    return request('http://127.0.0.1:18865' + path, body, token, key)

def save(path, value):
    path.write_text(json.dumps(value, indent=2) + '\n')

def idle():
    for port in [8765, 9875, 9885, 9895]:
        assert request(f'http://127.0.0.1:{port}/v1/pool')['in_use'] == 0, 'real engine busy'

def ready(url, process, seconds=40):
    end = time.monotonic() + seconds
    while time.monotonic() < end:
        assert process.poll() is None, 'isolated process exited'
        try:
            return request(url)
        except OSError:
            time.sleep(.25)
    raise RuntimeError('isolated service did not become ready')

idle()
terminal = {'completed', 'failed', 'cancelled', 'expired', 'rejected', 'aborted'}
runs = sim('/sim/v1/runs')
assert all(row['phase'] in terminal for row in (runs.get('runs', []) if isinstance(runs, dict) else runs))
for port in [9915, 9917]:
    with socket.socket() as sock:
        sock.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
        sock.bind(('0.0.0.0', port))
with socket.socket(socket.AF_INET, socket.SOCK_DGRAM) as sock:
    sock.bind(('0.0.0.0', 15068))
assert request('http://192.168.71.9:9916/api/overview')['retention_hours'] == 8760
originals = json.loads((root / 'original-hashes.json').read_text())
assert all(hashlib.sha256(Path(p).read_bytes()).hexdigest() == h for p, h in originals.items())
active = subprocess.check_output(['docker', 'inspect', '--format', '{{.Id}} {{.State.StartedAt}}', 'active-call'], text=True)
crm_pid = int(Path('/home/chuanming/golive-telephony/.phone-brain.crm.pid').read_text())
crm_env = dict(item.decode().split('=', 1) for item in Path(f'/proc/{crm_pid}/environ').read_bytes().split(b'\0') if b'=' in item)
env = envfile(root / 'private/engine.env')
env = {k: v.replace('127.0.0.1:8791', '127.0.0.1:9917') for k, v in env.items()}
env.update(GOLIVE_ADDR='127.0.0.1:9915', GOLIVE_LAST_CALL_FILE=str(root / 'state/last-call.json'),
           GOLIVE_STORE_DIR=str(root / 'state/store'), GOLIVE_STORE_TTL_HOURS='8760', GOLIVE_REALTIME_STORE_CALLS='1')
saved = json.loads((root / 'private/saved-call.json').read_text())
scenario = json.loads(Path('/home/chuanming/golive-sip-ab/native-ab-20261031/private/scenario.json').read_text())
scenario.update(json.loads(Path('/tmp/receipt-ab-v3-overrides.json').read_text()))
adapter_config = json.loads((root / '01-baseline/adapter.json').read_text())
attempt.mkdir()
state = {'started_at': datetime.datetime.now(datetime.timezone.utc).isoformat(), 'runs': []}
save(attempt / 'attempt.json', state)
save(attempt / 'scenario.json', scenario)
for idx, version in enumerate(['7fc4237', '2891b52'], 1):
    idle()
    out = attempt / f'{idx:02d}-{version}'
    out.mkdir()
    shutil.copytree(root / 'crm-snapshot', out / 'crm')
    engine_path = Path('/tmp/golive-new-caller-7fc4237-linux') if version == '7fc4237' else Path('/tmp/golive-receipt-2891b52-linux')
    expected_engine = {'7fc4237': '7d0d1e61713e85b6f903bfaa2aff46eebd3e834c8c5660b7d4a6bc93396ac65d', '2891b52': '82152bb45d6ef50a57370e9dd8c6d26caf60db6c4be566015586dd8dd8fa7472'}
    assert hashlib.sha256(engine_path.read_bytes()).hexdigest() == expected_engine[version]
    row = {'version': version, 'engine_sha256': hashlib.sha256(engine_path.read_bytes()).hexdigest(), 'stage': 'starting', 'crm_version': '3201ec4', 'crm_sha256': hashlib.sha256(Path('/tmp/golive-crm-3201ec4-linux').read_bytes()).hexdigest()}
    state['runs'].append(row)
    save(attempt / 'attempt.json', state)
    engine = adapter = crm = None
    with (out / 'crm.log').open('ab') as crm_log, (root / 'state/golive.log').open('ab') as log, (out / 'adapter.log').open('ab') as adapter_log:
        try:
            crm = subprocess.Popen(['/tmp/golive-crm-3201ec4-linux', '-dir', str(out / 'crm'), '-addr', '127.0.0.1:9917', '-originator', 'http://127.0.0.1:1', '-golive', 'http://127.0.0.1:9915'], env=crm_env, stdout=crm_log, stderr=crm_log)
            ready('http://127.0.0.1:9917/healthz', crm)
            engine = subprocess.Popen([str(engine_path), '-profile', str(root / 'private/profile.json'), '-env', str(root / 'private/providers.env'), '-addr', '127.0.0.1:9915'], env=env, cwd='/home/chuanming/golive-telephony', stdout=log, stderr=log)
            row.update(engine_pid=engine.pid, crm_pid=crm.pid)
            save(attempt / 'attempt.json', state)
            assert ready('http://127.0.0.1:9915/healthz', engine)['version'] == version
            for _ in range(120):
                pool = request('http://127.0.0.1:9915/v1/pool')
                warm = request('http://127.0.0.1:9915/v1/opening', {'greeting': saved['greeting'], 'warm': True})
                assert not warm.get('kicked')
                if pool.get('ok') and pool['in_use'] == 0 and warm.get('cached') and all(x.get('ready') for x in warm['cached']):
                    break
                time.sleep(.3)
            else:
                raise RuntimeError('warmup did not finish')
            reservation = sim('/sim/v1/runs', scenario, 'receipt-ab-2891b52-v3-' + str(idx))
            run = reservation['run_id']
            assert re.fullmatch(r'sim-[A-Za-z0-9_-]+', run)
            cid = 'sim-receipt-'  + str(uuid.uuid4())
            row.update(run=run, call=cid, stage='reserved')
            save(out / 'reservation.json', reservation)
            save(attempt / 'attempt.json', state)
            cfg = dict(adapter_config, allowed_uri='sip:' + run + '@192.168.71.9')
            assert re.fullmatch(r'sip:sim-[A-Za-z0-9_-]+@192\.168\.71\.9', cfg['allowed_uri'])
            save(out / 'adapter.json', cfg)
            save(out / 'call.json', dict(call_id=cid, **{k: saved.get(k, '') for k in ['instructions', 'greeting', 'voice']}))
            row['stage'] = 'dial_requested'
            save(attempt / 'attempt.json', state)
            adapter = subprocess.Popen([str(root / 'adapter'), '-config', str(out / 'adapter.json'), '-call', str(out / 'call.json'), '-dial'], env=dict(os.environ, **envfile(root / 'private/adapter.env')), stdout=adapter_log, stderr=adapter_log)
            row.update(adapter_pid=adapter.pid, stage='dialed')
            save(attempt / 'attempt.json', state)
            print(json.dumps(row), flush=True)
            deadline = time.monotonic() + 185
            while time.monotonic() < deadline:
                current = sim('/sim/v1/runs/' + run)
                if current['phase'] in terminal:
                    break
                time.sleep(2)
            else:
                raise RuntimeError('simulator timeout; inspect, do not repeat')
            report = sim('/sim/v1/runs/' + run + '/report')
            save(out / 'receiver-report.json', report)
            adapter.wait(timeout=10)
            assert adapter.returncode == 0
            row.update(stage='ended', phase=current['phase'], adapter_exit=adapter.returncode,
                gaps_ms=sorted(x['value_ms'] for x in report.get('assistant_response_gaps', []) if x.get('valid') is True and x.get('evidence') == 'application_audio_boundary' and x.get('value_ms', -1) >= 0),
                termination_reason=report.get('termination_reason'))
            save(attempt / 'attempt.json', state)
            print(json.dumps(row), flush=True)
        finally:
            for process, timeout in [(adapter, 10), (engine, 45), (crm, 120)]:
                if process is not None and process.poll() is None:
                    process.terminate()
                    process.wait(timeout=timeout)
    idle()
assert subprocess.check_output(['docker', 'inspect', '--format', '{{.Id}} {{.State.StartedAt}}', 'active-call'], text=True) == active
assert all(hashlib.sha256(Path(p).read_bytes()).hexdigest() == h for p, h in originals.items())
state['completed_at'] = datetime.datetime.now(datetime.timezone.utc).isoformat()
save(attempt / 'attempt.json', state)
print(json.dumps({'complete': True, 'originals_unchanged': True, 'active_call_unchanged': True}), flush=True)
