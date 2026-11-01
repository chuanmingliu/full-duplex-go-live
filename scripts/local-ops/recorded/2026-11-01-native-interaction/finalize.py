"""Verify the completed historical call and update its private receipt; never dial."""
import array
import collections
import datetime
import hashlib
import json
import os
import subprocess
import urllib.request
import wave
from pathlib import Path

os.umask(0o077)
root = Path('/home/chuanming/golive-sip-ab/native-real-fb11e2c')
call_id = '280fdab1-4078-4627-bf79-4eef8f627719'
url = 'http://192.168.71.9:9896/api/calls/' + call_id
with urllib.request.urlopen(url, timeout=15) as response:
    detail = json.load(response)
assert detail['call']['id'] == call_id
assert detail['archive']['archived'] and detail['archive']['audio']
relative = Path(detail['archive']['dir']).relative_to('/data/archive')
archive_root = (root / 'state/archive').resolve()
archive = (archive_root / relative).resolve()
assert archive.is_relative_to(archive_root)
manifest = json.loads((archive / 'manifest.json').read_text())
assert manifest['id'] == call_id
required = {'audio.wav', 'call.json', 'golive.log', 'ringback.json',
            'ringback.wav', 'session.json', 'transcript.txt'}
assert required <= manifest['files'].keys()
for name in required:
    path = archive / name
    entry = manifest['files'][name]
    assert path.stat().st_size == entry['bytes'] > 0, name
    assert hashlib.sha256(path.read_bytes()).hexdigest() == entry['sha256'], name
saved = json.loads((archive / 'call.json').read_text())
session = json.loads((archive / 'session.json').read_text())
assert saved['session'] == session['id'] == manifest['session']
assert session.get('closed_at') and not session['closed_at'].startswith('0001-')
speakers = collections.Counter(x['who'] for x in saved['transcript'] if x.get('text'))
assert speakers['caller'] and speakers['assistant']
with wave.open(str(archive / 'audio.wav'), 'rb') as wav:
    assert wav.getnchannels() == 2 and wav.getsampwidth() == 2
    seconds = wav.getnframes() / wav.getframerate()
    samples = array.array('h', wav.readframes(wav.getnframes()))
    assert any(samples[::2]) and any(samples[1::2])
with wave.open(str(archive / 'ringback.wav'), 'rb') as wav:
    assert wav.getsampwidth() == 2
    ringback_seconds = wav.getnframes() / wav.getframerate()
    assert any(array.array('h', wav.readframes(wav.getnframes())))
events = [json.loads(line) for line in
          (root / 'state/adapter' / call_id / 'events.jsonl').read_text().splitlines()]
ended = [event for event in events if event['type'] == 'call_end']
assert len(ended) == 1 and not ended[0]['data']['failed']
assert any(event['type'] == 'sip_bye' and event['data']['transaction_ok']
           for event in events)
assert any(event['type'] == 'agent_hangup' for event in events)
container = json.loads(subprocess.check_output(
    ['docker', 'inspect', 'golive-native-sip-call-280fdab1-4078-4627-bf79-4eef8f627719'], text=True))[0]
assert not container['State']['Running'] and container['State']['ExitCode'] == 0
assert subprocess.check_output(
    ['docker', 'inspect', '--format', '{{.State.Running}}', 'active-call'],
    text=True).strip() == 'true'
assert detail.get('retrospective'), 'publish notes before finalizing'
receipt_path = root / 'state' / ('receipt-' + call_id + '.json')
receipt = json.loads(receipt_path.read_text())
assert receipt['call_id'] == call_id
receipt.update(stage='archived', ended_at=ended[0]['at'], adapter_exit_code=0,
               hangup_initiator='assistant', archive_files_verified=len(required),
               archive_verified_at=datetime.datetime.now(datetime.timezone.utc).isoformat(),
               physical_handset_capture_verified=False)
temporary = receipt_path.with_suffix('.json.tmp')
temporary.write_text(json.dumps(receipt, indent=2) + '\n')
temporary.replace(receipt_path)
print(json.dumps(dict(call_id=call_id, stage='archived', verified_files=len(required),
                     audio_seconds=round(seconds, 3), ringback_seconds=ringback_seconds,
                     speakers=dict(speakers), retrospective=True,
                     active_call_preserved=True)))
