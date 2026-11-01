"""Publish only the reviewed issue and two simulator retrospectives."""
from pathlib import Path
import hashlib
import json
import re

payload = json.loads(Path('/tmp/silent-ack-notes-20261101.json').read_text())
assert payload['issue']['id'] == 'ISS-092'
assert len(payload['retros']) == 2
roots = {
    '/home/chuanming/golive-telephony/journal': 'f0ba9ca341caf9de0f130588ce7bd1fe196e2836bbb2d3011f2fdbf7f79682f0',
    '/home/chuanming/golive-sip-ab/tool-ack-focused-20261101/state/notes': 'bacd102972fa5d1598b7715f25eaf40cff4463badf39c03127c07b120f560876',
}
pending = []
for root, expected in roots.items():
    root = Path(root)
    issue_file = root / 'issues.json'
    raw = issue_file.read_bytes()
    assert hashlib.sha256(raw).hexdigest() == expected, 'notes changed since review'
    issues = json.loads(raw)
    matches = [n for n, i in enumerate(issues['issues']) if i['id'] == 'ISS-092']
    assert len(matches) == 1
    issues['issues'][matches[0]] = payload['issue']
    for name, text in payload['retros'].items():
        assert re.fullmatch(r'sim-silentack-[a-f0-9-]+\.md', name)
        dst = root / 'retros' / name
        assert not dst.exists() or dst.read_text() == text, 'existing note differs'
    pending.append((root, issue_file, issues))
for root, issue_file, issues in pending:
    for name, text in payload['retros'].items():
        dst = root / 'retros' / name
        dst.write_text(text)
        assert dst.read_text() == text
    tmp = issue_file.with_suffix('.silent-ack.tmp')
    tmp.write_text(json.dumps(issues, ensure_ascii=False, indent=1) + '\n')
    tmp.replace(issue_file)
    assert json.loads(issue_file.read_text()) == issues
print(json.dumps({'published_roots': list(roots), 'issue': 'ISS-092', 'retros': list(payload['retros'])}))
