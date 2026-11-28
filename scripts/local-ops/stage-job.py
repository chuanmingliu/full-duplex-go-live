#!/usr/bin/env python3
"""Prepare an exact-byte shell body and short runner job; never queue or copy it."""
import argparse
import hashlib
import json
import os
from pathlib import Path, PurePosixPath
import re


def require(ok, message):
    if not ok:
        raise SystemExit(message)


def digest(raw):
    return hashlib.sha256(raw).hexdigest()


def prepare(source, wrapper, runner_home, remote_home, extras):
    require(source.is_file() and not source.is_symlink(), 'source must be regular')
    raw = source.read_bytes()
    text = raw.decode('utf-8')
    require(not re.search(r'__[A-Z][A-Z0-9_]+__', text), 'unfinalized job')
    require(re.fullmatch(r'[A-Za-z0-9][A-Za-z0-9._-]*\.job', wrapper.name), 'wrapper name invalid')
    require(not wrapper.exists() and not wrapper.is_symlink(), 'wrapper exists')
    require(re.fullmatch(r'/[A-Za-z0-9_./-]+', remote_home) and
            '..' not in PurePosixPath(remote_home).parts, 'remote home must be a safe absolute path')
    headers = []
    for line in text.splitlines():
        if line.startswith(('#timeout ', '#pull ')):
            headers.append(line)
        require(not line.startswith('#push '), 'source has #push: move payloads to --push arguments')
    require(sum(x.startswith('#timeout ') for x in headers) == 1, 'exactly one timeout required')
    body = wrapper.with_suffix('.body.sh')
    receipt = wrapper.with_suffix('.review.json')
    require(not body.exists() and not body.is_symlink() and
            not receipt.exists() and not receipt.is_symlink(), 'body or receipt exists')
    remote = str(PurePosixPath(remote_home) / body.name)
    pushes = ['#push ' + body.name + ' ' + remote]
    stamps = []
    names = {body.name}
    for name, destination in extras:
        path = Path(name)
        require(path.is_file() and not path.is_symlink(), 'extra must be regular')
        require(re.fullmatch(r'[A-Za-z0-9][A-Za-z0-9._-]*', path.name), 'extra basename invalid')
        require(destination == str(PurePosixPath(remote_home) / path.name), 'extra must keep its name in remote home')
        require(path.name not in names, 'duplicate staged basename')
        names.add(path.name)
        pushes.append('#push ' + path.name + ' ' + destination)
        stamps.append({'source': str(path), 'local_runner_source': str(runner_home / path.name),
                       'destination': destination, 'sha256': digest(path.read_bytes()), 'bytes': path.stat().st_size})
    # The remote interpreter reads and hashes one opened file, then executes
    # those same bytes. It never reopens the pathname after verification.
    script = '\n'.join(headers + pushes) + "\nset -eu\npython3 -B - <<'STAGED_BODY'\n"
    script += "import hashlib,os,stat,subprocess\np=" + repr(remote) + "\n"
    script += "fd=os.open(p,os.O_RDONLY|os.O_NOFOLLOW)\nwith os.fdopen(fd,'rb') as stream:\n"
    script += "    info=os.fstat(stream.fileno())\n    if not stat.S_ISREG(info.st_mode): raise SystemExit('staged body is not regular')\n    body=stream.read()\n"
    script += "if len(body)!=" + str(len(raw)) + " or hashlib.sha256(body).hexdigest()!=" + repr(digest(raw)) + ": raise SystemExit('staged body checksum mismatch')\n"
    script += "subprocess.run(['/bin/bash'],input=body,check=True)\nSTAGED_BODY\n"
    require(len(script.encode()) < 8192, 'wrapper exceeds conservative SSH limit')
    record = {'queued': False, 'source': str(source), 'source_sha256': digest(raw),
              'body': str(body), 'body_sha256': digest(raw), 'body_bytes': len(raw),
              'wrapper': str(wrapper), 'wrapper_sha256': digest(script.encode()), 'wrapper_bytes': len(script.encode()),
              'local_runner_body': str(runner_home / body.name), 'remote_body': remote, 'extra_pushes': stamps,
              'execution': 'hash exact bytes then feed those bytes to bash stdin; no body re-open'}
    for path, contents in ((body, raw), (wrapper, script.encode()),
                           (receipt, (json.dumps(record, indent=2) + '\n').encode())):
        fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
        with os.fdopen(fd, 'wb') as stream:
            stream.write(contents)
    return record


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('source', type=Path)
    parser.add_argument('wrapper', type=Path)
    parser.add_argument('--runner-home', type=Path,
                        default=Path(os.environ.get('GOLIVE_RUNNER_HOME', str(Path.home() / 'golive'))))
    parser.add_argument('--remote-home', required=True, help='Existing staging directory on the box')
    parser.add_argument('--push', nargs=2, action='append', default=[], metavar=('LOCAL_FILE', 'REMOTE_FILE'))
    args = parser.parse_args()
    print(json.dumps(prepare(args.source, args.wrapper, args.runner_home, args.remote_home, args.push), indent=2))


if __name__ == '__main__':
    main()
