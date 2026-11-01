"""Publish the reviewed simulator evidence without replacing unrelated notes."""
from pathlib import Path
import hashlib
import json
import os
import re
import urllib.request

os.umask(0o077)
payload = json.loads(Path('/tmp/conversation-ab-notes-20261101.json').read_text())
assert {i['id'] for i in payload['issues']} == {'ISS-091', 'ISS-093', 'ISS-094'}
assert len(payload['retros']) == 3
roots = {
    '/home/chuanming/golive-telephony/journal': '2ef25b9edbce8351f9129ab9a8be53de1f8794d56a54f754cd1e095a7aeeb886',
    '/home/chuanming/golive-sip-ab/tool-ack-focused-20261101/state/notes': 'ff21f82c38b78a9b617222865521fa17fcf9ae490238b0184c4b644218e71dd9',
}
pending = []
for name, digest in roots.items():
    root = Path(name)
    file = root / 'issues.json'
    raw = file.read_bytes()
    assert hashlib.sha256(raw).hexdigest() == digest, 'notes changed since review'
    value = json.loads(raw)
    for issue in payload['issues']:
        matches = [n for n, old in enumerate(value['issues']) if old['id'] == issue['id']]
        assert len(matches) <= 1
        if matches:
            value['issues'][matches[0]] = issue
        else:
            value['issues'].append(issue)
    for filename in payload['retros']:
        assert re.fullmatch(r'sim-(conversation|newcaller)-[a-f0-9-]+\.md', filename)
        assert not (root / 'retros' / filename).exists(), 'retrospective exists: do not replay'
    pending.append((root, file, value))
for root, file, value in pending:
    for filename, body in payload['retros'].items():
        (root / 'retros' / filename).write_text(body)
    temporary = file.with_suffix('.conversation-ab.tmp')
    temporary.write_text(json.dumps(value, ensure_ascii=False, indent=1) + '\n')
    temporary.replace(file)
    assert json.loads(file.read_text()) == value
token = Path('/home/chuanming/golive-desk/codex.token').read_text().strip()
body = ('Original GoLive conversational SIP A/B complete: 9547d22 p95 1017 ms n=19 '
        'fails count and one unverified link; 302267b conversational p95 942 ms n=28 '
        'passes strict server gate, estimated 992 ms with assumed 50 ms overhead. '
        'Candidate conversation invokes no result tasks; quote-heavy evidence remains separate. '
        '7fc4237 quote-heavy validation exercises new caller protection five times: '
        'conversation p95 988 ms n=6, quote readout p95 6341 ms n=5; gate fails '
        'for sample count and one unverified chained result/readout. All three calls '
        'retain verified chat/audio; zero receiver warnings. Journal 9916 upgraded '
        'to CI-passed 302267b; 12 manifests valid and 48 raw/transcript files unchanged. '
        'Original quote delay/configs and Active Call preserved. Native engine remains '
        'f12b6f5; 7fc4237 has not been promoted. No real call or physical handset qualification.')
request = urllib.request.Request('http://127.0.0.1:8792/api/logs/telephony-latency',
    data=body.encode(), headers={'Authorization': 'Bearer ' + token,
    'Content-Type': 'text/plain; charset=utf-8'}, method='POST')
with urllib.request.urlopen(request, timeout=15) as response:
    assert response.status == 200
print(json.dumps({'issues': [i['id'] for i in payload['issues']],
                  'retrospectives': list(payload['retros']), 'desk_updated': True}))
