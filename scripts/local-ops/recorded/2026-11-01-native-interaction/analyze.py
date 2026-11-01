"""Extract numeric evidence from this private capture; never emit transcripts."""
import datetime
import json
import math
from pathlib import Path
import shlex
import sys


def stamp(value):
    return datetime.datetime.fromisoformat(value.replace('Z', '+00:00'))


def distribution(values):
    values = sorted(values)
    return dict(n=len(values), p50_ms=values[math.ceil(len(values) * .5) - 1],
                p95_ms=values[math.ceil(len(values) * .95) - 1], max_ms=max(values))


private = json.loads(Path(sys.argv[1]).read_text())
assert private['call']['id'] == '280fdab1-4078-4627-bf79-4eef8f627719'
rows = [dict(part.split('=', 1) for part in shlex.split(line) if '=' in part)
        for line in private['log'].splitlines() if 'msg=' in line]
clears = [e for e in private['events'] if e['type'] == 'playback_cleared']
used = set()
interruptions = []
opened = None
for row in rows:
    if row.get('msg') == 'listen: utterance opened':
        opened = row
    if row.get('msg') != 'barge-in: generation invalidated':
        continue
    assert opened is not None
    elapsed = (stamp(row['time']) - stamp(opened['time'])).total_seconds() * 1000
    assert 0 <= elapsed < 1000
    matching = [(i, e) for i, e in enumerate(clears) if i not in used and
                0 <= (stamp(e['at']) - stamp(row['time'])).total_seconds() < .002]
    assert len(matching) == 1
    i, event = matching[0]
    used.add(i)
    interruptions.append(dict(generation=int(row['generation']),
                              during_playback=opened['barge_in'] == 'true',
                              active_speech_at_detection_ms=int(opened['active_ms']),
                              detected_at=opened['time'], cancelled_at=row['time'],
                              detection_to_cancel_ms=round(elapsed),
                              cancel_to_adapter_clear_ms=round(
                                  (stamp(event['at']) - stamp(row['time'])).total_seconds() * 1000, 3)))
assert len(interruptions) == 8
playing = [r for r in interruptions if r['during_playback']]
assert len(playing) == 7
tasks = [dict(task=r['task'], observed_completion_ms=int(r['took_ms'])) for r in rows
         if r.get('msg') == 'tools: task done']
assert len(tasks) == 4
ends = [e for e in private['events'] if e['type'] == 'call_end']
assert len(ends) == 1 and not ends[0]['data']['failed']
result = dict(call_id=private['call']['id'], interruptions=interruptions,
              playback_detection_to_cancel=distribution([r['detection_to_cancel_ms'] for r in playing]),
              cancel_to_adapter_clear=distribution([r['cancel_to_adapter_clear_ms'] for r in interruptions]),
              quotation_tasks=tasks, adapter_end=ends[0],
              acoustic_stop_latency_verified=False,
              caveat='Detection follows accumulated speech; VAD start includes pre-roll. Queue clear is not handset stop.')
print(json.dumps(result, indent=2))
