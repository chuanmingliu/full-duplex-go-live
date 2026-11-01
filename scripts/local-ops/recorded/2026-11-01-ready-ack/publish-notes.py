"""Publish only the reviewed issue and two simulator retrospectives."""
from pathlib import Path
import hashlib
import json
import re

payload = json.loads(Path('/tmp/ready-ack-notes-20261101.json').read_text())
assert payload['issue']['id'] == 'ISS-092'
assert len(payload['retros']) == 2
roots = {
    '/home/chuanming/golive-telephony/journal': 'eff51f37c3851eee7ae5b48bbfecc5aeedafb9c5755eeb08b4a4a3e9a47fdc07',
    '/home/chuanming/golive-sip-ab/tool-ack-focused-20261101/state/notes': '2ceb6550f6c63fbad71e504b90a075ab7fba1763fbc3b49dec79594f0a86f2b0',
}
pending = []
for root, expected in roots.items():
    root = Path(root)
    issue_file = root / 'issues.json'
    raw = issue_file.read_bytes()
    assert hashlib.sha256(raw).hexdigest() == expected, 'notes changed since review'
    issues = json.loads(raw)
    assert not any(i['id'] == 'ISS-092' for i in issues['issues'])
    issues['issues'].append(payload['issue'])
    for name, text in payload['retros'].items():
        assert re.fullmatch(r'sim-readyack-[a-f0-9-]+\.md', name)
        dst = root / 'retros' / name
        assert not dst.exists() or dst.read_text() == text, 'existing note differs'
    pending.append((root, issue_file, issues))
for root, issue_file, issues in pending:
    for name, text in payload['retros'].items():
        dst = root / 'retros' / name
        dst.write_text(text)
        assert dst.read_text() == text
    tmp = issue_file.with_suffix('.ready-ack.tmp')
    tmp.write_text(json.dumps(issues, ensure_ascii=False, indent=1) + '\n')
    tmp.replace(issue_file)
    assert json.loads(issue_file.read_text()) == issues
print(json.dumps({'published_roots': list(roots), 'issue': 'ISS-092', 'retros': list(payload['retros'])}))
