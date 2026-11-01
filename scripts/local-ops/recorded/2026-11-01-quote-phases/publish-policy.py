"""Publish only the reviewed issue and two simulator retrospectives."""
from pathlib import Path
import hashlib
import json
import re

payload = json.loads(Path('/tmp/quote-policy-notes-20261101.json').read_text())
assert payload['issue']['id'] == 'ISS-091'
assert len(payload['retros']) == 1
roots = {
    '/home/chuanming/golive-telephony/journal': '21d555bbd5099f6bfc57cbb62eabe63df0da3dcd45394bcf287b08bf0a1a7143',
    '/home/chuanming/golive-sip-ab/tool-ack-focused-20261101/state/notes': '3f49ee1b1f14d390b75119ec75e78f76ea6450b1eee056ae2f8b515ac645e906',
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
        assert dst.exists() and hashlib.sha256(dst.read_bytes()).hexdigest() == 'e830a6a89a8a79f7f2d58032c958b1d78020c6b44466bcf15dbb87ca15bffa9f', 'existing note changed'
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

import urllib.request
token = Path('/home/chuanming/golive-desk/codex.token').read_text().strip()
text = 'Owner decision: preserve original 2500 ms + up to 2000 ms quote-service delay; target conversational server p95 <1150 ms with assumed 50 ms handset overhead, and report quote completion separately. Analyzer --quote-sla separate retains minimum 20 conversational content samples and evidence checks. Latest archived diagnostic remains FAIL: 7 conversational samples, p95 1016 ms, one missing response and two unverified links. No handset qualification.'
r = urllib.request.Request('http://127.0.0.1:8792/api/logs/telephony-latency', data=text.encode(), headers={'Authorization':'Bearer '+token,'Content-Type':'text/plain; charset=utf-8'}, method='POST')
with urllib.request.urlopen(r, timeout=15) as response:
    assert response.status == 200
