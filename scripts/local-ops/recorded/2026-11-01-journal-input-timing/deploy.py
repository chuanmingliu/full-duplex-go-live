"""Upgrade only the idle experiment Journal, retaining its arguments and environment."""
import fcntl
import hashlib
import json
import os
from pathlib import Path
import shutil
import signal
import subprocess
import time
import urllib.request

os.umask(0o077)
root = Path('/home/chuanming/golive-sip-ab/tool-ack-focused-20261101')
binary = root / 'journal'
staged = Path('/tmp/golive-journal-302267b-linux')
old_sha = 'f6fb27f2866243337ffa482d6503e9efef80ca34a8dfd5c7a4bd285c65712968'
new_sha = 'ca3e4d8adfa306c6e3533668f8c6fd993c2826ba93aa7703153f7c81883a02b4'
pid = 1122450
proc = Path('/proc') / str(pid)
release = root / 'journal-releases/input-timing-302267b-v2'
assert not release.exists(), 'release exists; inspect receipt instead of replaying'
lock = (root / '.journal-release.lock').open('a')
fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)

def sha(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()

def get(url):
    with urllib.request.urlopen(url, timeout=15) as response:
        return json.load(response)

for port in [8765, 9875, 9885, 9895]:
    assert get(f'http://127.0.0.1:{port}/v1/pool')['in_use'] == 0
for name in ['conversation-ab-20261101', 'new-caller-7fc4237']:
    assert json.loads((root / name / 'attempt.json').read_text()).get('completed_at')
    assert (root / name / 'verification.json').is_file()
assert sha(staged) == new_sha and sha(binary) == old_sha
assert proc.joinpath('exe').resolve() == binary and sha(proc / 'exe') == old_sha
args = [x.decode() for x in (proc / 'cmdline').read_bytes().split(b'\0') if x]
env = dict(x.decode().split('=', 1) for x in (proc / 'environ').read_bytes().split(b'\0') if b'=' in x)
cwd = proc.joinpath('cwd').resolve()
assert args[1:3] == ['-addr', '192.168.71.9:9916']
assert args[args.index('-retention-hours') + 1] == '8760'
assert args[args.index('-target') + 1] == '1200'
assert args[args.index('-archive') + 1] == str(root / 'state/archive')
identity = (proc / 'stat').read_text().split(') ', 1)[1].split()[19]
originals = json.loads((root / 'original-hashes.json').read_text())
assert all(sha(Path(p)) == h for p, h in originals.items())
active = subprocess.check_output(['docker', 'inspect', '--format', '{{.Id}} {{.State.StartedAt}}', 'active-call'], text=True)
base = 'http://192.168.71.9:9916'
before = {c['id'] for c in get(base + '/api/overview')['calls']}
evidence_call = 'sim-inputtiming-96bb8a33-8c9b-4bb5-ac65-69dd84ed95af'
assert evidence_call in before
# Archive.Put intentionally re-derives call.json and updates its manifest on
# startup. Protect raw evidence and transcript bytes, and validate all new
# manifest hashes rather than assuming derived summaries are immutable.
manifests = {}
raw_files = {}
for path in (root / 'state/archive').rglob('manifest.json'):
    data = json.loads(path.read_text())
    manifests[str(path)] = data
    for name, info in data['files'].items():
        file = path.parent / name
        assert file.stat().st_size == info['bytes'] and sha(file) == info['sha256']
        if name != 'call.json':
            raw_files[str(file)] = info['sha256']
release.mkdir(parents=True)
shutil.copy2(binary, release / 'journal.previous')
(release / 'argv.json').write_text(json.dumps(args))
metadata = release / 'derived-backup'
metadata.mkdir()
for path in manifests:
    src = Path(path)
    dst = metadata / src.parent.name
    dst.mkdir()
    for name in ['call.json', 'manifest.json']:
        shutil.copy2(src.parent / name, dst / name)
receipt = dict(stage='prepared', old_pid=pid, old_sha256=old_sha, new_sha256=new_sha,
               source='302267b0e67501b0324a09a3b434bec41ba673bc', ci_run=41280951451)

def save():
    (release / 'receipt.json').write_text(json.dumps(receipt, indent=2) + '\n')

def stop(p):
    os.kill(p, signal.SIGTERM)
    for _ in range(100):
        path = Path('/proc', str(p), 'stat')
        if not path.exists() or path.read_text().split(') ', 1)[1].split()[0] == 'Z':
            return
        time.sleep(.1)
    raise RuntimeError('Journal did not stop; no forced kill attempted')

def start():
    with (root / 'journal.log').open('ab') as log:
        return subprocess.Popen(args, cwd=cwd, env=env, stdin=subprocess.DEVNULL,
                                stdout=log, stderr=log, start_new_session=True)

save()
assert (proc / 'stat').read_text().split(') ', 1)[1].split()[19] == identity
stop(pid)
child = None
try:
    temporary = binary.with_suffix('.input-timing-new')
    shutil.copy2(staged, temporary)
    temporary.chmod(0o700)
    temporary.replace(binary)
    child = start()
    receipt.update(stage='started', new_pid=child.pid)
    save()
    for _ in range(100):
        assert child.poll() is None, 'new Journal exited'
        try:
            overview = get(base + '/api/overview')
            if before <= {c['id'] for c in overview['calls']}:
                break
        except OSError:
            pass
        time.sleep(.3)
    else:
        raise RuntimeError('Journal history did not become ready')
    assert overview['retention_hours'] == 8760
    detail = get(base + '/api/calls/' + evidence_call)
    evidence = detail['call']['latency']
    assert 'conversational_first_content' in evidence and evidence['quote_only_responses'] == 1
    assert any(r.get('origin_evidence') == 'caller_input_v1' for r in evidence['tool_result_rows'])
    assert all(sha(Path(p)) == h for p, h in raw_files.items())
    derived_updated = 0
    for path, prior in manifests.items():
        current = json.loads(Path(path).read_text())
        assert current['id'] == prior['id'] and current['session'] == prior['session']
        assert current['build'] == prior['build'] and current.get('prompt_sha') == prior.get('prompt_sha')
        assert set(current['files']) == set(prior['files'])
        for name, info in current['files'].items():
            file = Path(path).parent / name
            assert file.stat().st_size == info['bytes'] and sha(file) == info['sha256']
        derived_updated += current['files']['call.json'] != prior['files']['call.json']
    assert all(sha(Path(p)) == h for p, h in originals.items())
    assert subprocess.check_output(['docker', 'inspect', '--format', '{{.Id}} {{.State.StartedAt}}', 'active-call'], text=True) == active
    receipt.update(stage='verified', calls=len(before), raw_files_unchanged=len(raw_files), manifests_verified=len(manifests), derived_summaries_updated=derived_updated,
                   latency=evidence, original_configs_unchanged=True, active_call_unchanged=True)
    save()
    print(json.dumps(receipt))
except BaseException:
    if child is not None and child.poll() is None:
        stop(child.pid)
    shutil.copy2(release / 'journal.previous', binary.with_suffix('.rollback'))
    binary.with_suffix('.rollback').replace(binary)
    for path in manifests:
        dst = Path(path).parent
        for name in ['call.json', 'manifest.json']:
            shutil.copy2(metadata / dst.name / name, dst / name)
    restored = start()
    receipt.update(stage='rolled_back', rollback_pid=restored.pid)
    save()
    raise
