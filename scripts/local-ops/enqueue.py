#!/usr/bin/env python3
"""Queue exact reviewed bytes for an already-running Mac runner."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import time


def require(ok, message):
    if not ok:
        raise SystemExit(message)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('job', type=Path)
    parser.add_argument('sha256', help='SHA-256 of the reviewed job bytes')
    parser.add_argument('--runner-home', type=Path,
                        default=Path(os.environ.get('GOLIVE_RUNNER_HOME', str(Path.home() / 'golive'))))
    args = parser.parse_args()
    job = args.job
    require(job.is_file() and not job.is_symlink(), 'job must be a regular file')
    require(re.fullmatch(r'[A-Za-z0-9][A-Za-z0-9._-]*\.job', job.name), 'use a safe, unique .job name')
    raw = job.read_bytes()
    require(hashlib.sha256(raw).hexdigest() == args.sha256, 'reviewed job changed')
    require(len(raw) < 8192, 'large job: use stage-job.py before enqueueing')
    runner = args.runner_home / '.runner'
    age = time.time() - float((runner / 'alive').read_text())
    require(0 <= age < 60, 'runner heartbeat stale or in the future')
    require(not list((runner / 'in').glob('*.job')), 'runner queue not empty')
    require(not any((runner / d / job.name).exists() for d in ('in', 'done')), 'job already queued/completed')
    require(not any((runner / 'out' / (job.stem + ext)).exists() for ext in ('.part', '.txt')), 'job already started')
    temporary = runner / 'in' / ('.' + job.name + '.tmp')
    fd = os.open(temporary, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    with os.fdopen(fd, 'wb') as stream:
        stream.write(raw)
        stream.flush()
        os.fsync(stream.fileno())
    os.replace(temporary, runner / 'in' / job.name)
    print(json.dumps({'queued': job.name, 'sha256': args.sha256}))


if __name__ == '__main__':
    main()
