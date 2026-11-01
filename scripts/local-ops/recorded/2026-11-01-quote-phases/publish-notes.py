"""Publish only the reviewed issue and two simulator retrospectives."""
from pathlib import Path
import hashlib
import json
import re

payload = json.loads(Path('/tmp/quote-phase-notes-20261101.json').read_text())
assert payload['issue']['id'] == 'ISS-091'
assert len(payload['retros']) == 1
roots = {
    '/home/chuanming/golive-telephony/journal': 'e0270293631aa657b79628d6fdd4ef3f34b2e8e6543557e21411d3909a3aa45e',
    '/home/chuanming/golive-sip-ab/tool-ack-focused-20261101/state/notes': '7ff4110cd7a28aaa8bbe1e0f5dd9a838277977452d166eb5da9e3c78bc385b97',
}
pending = []
for root, expected in roots.items():
    root = Path(root)
    issue_file = root / 'issues.json'
    raw = issue_file.read_bytes()
    assert hashlib.sha256(raw).hexdigest() == expected, 'notes changed since review'
    issues = json.loads(raw)
    matches = [n for n, i in enumerate(issues['issues']) if i['id'] == 'ISS-091']
    assert len(matches) <= 1
    if matches:
        issues['issues'][matches[0]] = payload['issue']
    else:
        issues['issues'].append(payload['issue'])
    for name, text in payload['retros'].items():
        assert re.fullmatch(r'sim-quotephases-[a-f0-9-]+\.md', name)
        dst = root / 'retros' / name
        assert not dst.exists() or dst.read_text() == text, 'existing note differs'
    pending.append((root, issue_file, issues))
for root, issue_file, issues in pending:
    for name, text in payload['retros'].items():
        dst = root / 'retros' / name
        dst.write_text(text)
        assert dst.read_text() == text
    tmp = issue_file.with_suffix('.quote-phases.tmp')
    tmp.write_text(json.dumps(issues, ensure_ascii=False, indent=1) + '\n')
    tmp.replace(issue_file)
    assert json.loads(issue_file.read_text()) == issues
print(json.dumps({'published_roots': list(roots), 'issue': 'ISS-091', 'retros': list(payload['retros'])}))
