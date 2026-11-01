#!/usr/bin/env python3
"""Journal-only release on the box; argv: staged binary, source JSON, expected SHA256.
Preserves and restarts exactly the running Journal arguments. Never dials.
"""
import hashlib
import json
import os
from pathlib import Path
import shutil
import signal
import subprocess
import sys
import time
import urllib.request

root = Path('/home/chuanming/golive-telephony')
binary = root / 'bin/journal-linux-amd64'
pidfile = root / '.phone-brain.journal.pid'
config = root / 'journal/experiments.json'
staged, sources, digest = Path(sys.argv[1]), Path(sys.argv[2]), sys.argv[3]
assert hashlib.sha256(staged.read_bytes()).hexdigest() == digest
json.loads(sources.read_text())
pid = int(pidfile.read_text())
proc = Path('/proc') / str(pid)
assert proc.joinpath('cwd').resolve() == root
assert proc.joinpath('exe').resolve() == binary
args = [a.decode() for a in proc.joinpath('cmdline').read_bytes().split(b'\0') if a]
assert args[1:3] == ['-addr', '192.168.71.9:8790']
assert args[args.index('-notes') + 1] == 'journal'
backup = root / 'journal-releases' / time.strftime('%Y%m%d-%H%M%S')
backup.mkdir(parents=True, exist_ok=False)
shutil.copy2(binary, backup / 'journal.previous')
if config.exists():
    shutil.copy2(config, backup / 'experiments.previous.json')
(backup / 'argv.json').write_text(json.dumps(args))
(backup / 'receipt.json').write_text(json.dumps({'old_pid': pid, 'new_sha256': digest, 'stage': 'prepared'}))
base = 'http://192.168.71.9:8790'
def get(path):
    with urllib.request.urlopen(base + path, timeout=15) as response:
        return json.load(response)
before = {c['id'] for c in get('/api/overview')['calls']}
def stop(p):
    os.kill(p, signal.SIGTERM)
    for _ in range(50):
        if not Path('/proc', str(p)).exists():
            return
        time.sleep(.1)
    raise RuntimeError('Journal did not stop; no forced kill attempted')
def start():
    with (root / 'journal.log').open('ab') as log:
        child = subprocess.Popen(args, cwd=root, stdin=subprocess.DEVNULL,
                                 stdout=log, stderr=log, start_new_session=True)
    pidfile.write_text(str(child.pid) + '\n')
    return child
stop(pid)
child = None
try:
    shutil.copy2(staged, binary.with_suffix('.new'))
    binary.with_suffix('.new').chmod(0o755)
    os.replace(binary.with_suffix('.new'), binary)
    shutil.copy2(sources, config.with_suffix('.new'))
    os.replace(config.with_suffix('.new'), config)
    child = start()
    for _ in range(30):
        assert child.poll() is None, 'New Journal exited'
        try:
            listing = get('/api/experiments')
            if len(listing) == 3:
                break
        except Exception:
            pass
        time.sleep(.3)
    else:
        raise RuntimeError('New Journal did not become ready')
    assert before <= {c['id'] for c in get('/api/overview')['calls']}
    counts = {}
    for source in listing:
        rows = get('/experiments/' + source['id'] + '/api/overview')['calls']
        counts[source['id']] = len(rows)
        assert rows
    (backup / 'receipt.json').write_text(json.dumps({'old_pid':pid,'new_pid':child.pid,'new_sha256':digest,'stage':'verified','calls':counts}, indent=2))
    print(json.dumps({'backup':str(backup),'pid':child.pid,'calls':counts}))
except BaseException:
    if child is not None and child.poll() is None:
        stop(child.pid)
    shutil.copy2(backup / 'journal.previous', binary.with_suffix('.rollback'))
    os.replace(binary.with_suffix('.rollback'), binary)
    if (backup / 'experiments.previous.json').exists():
        shutil.copy2(backup / 'experiments.previous.json', config)
    else:
        config.unlink(missing_ok=True)
    start()
    raise
