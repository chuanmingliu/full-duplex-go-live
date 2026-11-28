"""Read only: numeric provider phases in the two pinned slow-turn windows.

The provider records lack turn IDs. Grouping by time is correlation, not an
explicit request-to-turn join. No request bodies, text or credentials are read
into the output.
"""
import json
import shlex
from pathlib import Path

windows = {
    'item_4': ('2026-11-01T16:32:12.',),
    'item_36': ('2026-11-01T16:35:38.', '2026-11-01T16:35:39.'),
}
allowed = {
    'time', 'msg', 'request_id', 'lane', 'key', 'copy', 'of', 'on',
    'http_status', 'outcome', 'finish_reason', 'data_frames', 'ms',
    'prompt_tokens', 'cached_prompt_tokens', 'completion_tokens',
    'account', 'why',
}
messages = {
    'llm: stream phases', 'llm hedge: a copy answered first', 'llm: usage',
    'tts hedge: asking both connections at once',
    'tts hedge: asking the second connection too',
    'tts hedge: a copy produced audio first',
    'tts fallback: asking the second vendor too',
}
found = {turn: [] for turn in windows}
for line in (Path.home() / 'golive-telephony/golive-telephony.log').open():
    matched = [turn for turn, prefixes in windows.items() if any(s in line for s in prefixes)]
    if not matched:
        continue
    try:
        row = json.loads(line)
    except ValueError:
        try:
            row = dict(field.split('=', 1) for field in shlex.split(line) if '=' in field)
        except ValueError:
            continue
    if row.get('msg') not in messages:
        continue
    safe = {k: v for k, v in row.items() if k in allowed or k.endswith('_ms')}
    for turn in matched:
        found[turn].append(safe)
for turn, rows in found.items():
    assert sum(r['msg'] == 'llm: stream phases' for r in rows) == 8, turn
    assert sum(r['msg'] == 'llm hedge: a copy answered first' for r in rows) == 1, turn
print(json.dumps({'call': 'out-mvxkn1v4-jeyp', 'scope': 'Time-window-correlated numeric provider phases; not a direct turn/request join or handset timing.', 'windows': found}, indent=2))
