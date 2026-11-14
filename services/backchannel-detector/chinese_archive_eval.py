"""Numeric model/legacy correlation on completed private original-GoLive archives."""
from datetime import datetime
import hashlib
import json
from pathlib import Path
import shlex

from chinese_eval import read_wav, replay


def parse(line):
    try:
        return json.loads(line) if line.lstrip().startswith('{') else dict(
            word.split('=', 1) for word in shlex.split(line) if '=' in word)
    except ValueError:
        return {}


def correlate(events, frames, created_at):
    origin = datetime.fromisoformat(created_at.replace('Z', '+00:00'))
    def ms(event):
        return round((datetime.fromisoformat(event['time'].replace('Z', '+00:00'))-origin).total_seconds()*1000)
    def yes(event, key):
        return event.get(key) in (True, 'true')
    def window(start, end):
        observed = [f for f in frames if start <= f['available_ms'] <= end]
        first = next((f['available_ms'] for f in observed if f['p_floor'] >= .7), None)
        streak, sustained = 0, None
        for f in observed:
            streak = streak+1 if f['p_floor'] >= .7 else 0
            if streak >= 3:
                sustained = f['available_ms']
                break
        return {'first_floor_after_open_ms': None if first is None else first-start,
                'sustained_floor_after_open_ms': None if sustained is None else sustained-start,
                'peak_floor': max((f['p_floor'] for f in observed), default=None),
                'peak_backchannel': max((f['p_backchannel'] for f in observed), default=None)}
    pending, last_open = None, None
    cuts, keeps, unpaired = [], [], 0
    for event in events:
        msg = event.get('msg')
        if msg == 'listen: utterance opened':
            last_open = event
            pending = event if yes(event, 'barge_in') else None
        elif msg == 'new query while answering: cutting the answer' and yes(event, 'barge_in') and yes(event, 'speaking'):
            if pending is None:
                unpaired += 1
                continue
            start, end = ms(pending), ms(event)
            info = window(start, end)
            cuts.append({'open_recording_ms': start, 'cut_recording_ms': end,
                         'legacy_open_to_cut_ms': end-start, **info,
                         'raw_signal_lead_ms': None if info['first_floor_after_open_ms'] is None
                         else end-start-info['first_floor_after_open_ms']})
            pending = None
        elif msg in ('listen: backchannel, not an interruption; keeping the floor',
                     'listen: a hesitation over the answer; keeping the floor'):
            if last_open is not None:
                keeps.append({'open_recording_ms': ms(last_open), 'keep_recording_ms': ms(event),
                              **window(ms(last_open), ms(event))})
    return {'cuts': cuts, 'kept_backchannels': keeps, 'unpaired_cuts': unpaired}


async def evaluate_archive(client, url, directory, output_dir):
    directory = Path(directory)
    manifest = json.loads((directory/'manifest.json').read_text())
    assert manifest['build'] == 'f1fd9a6' and manifest['audio']
    for name in ('audio.wav', 'golive.log', 'session.json'):
        data = (directory/name).read_bytes()
        assert len(data) == manifest['files'][name]['bytes']
        assert hashlib.sha256(data).hexdigest() == manifest['files'][name]['sha256']
    meta = json.loads((directory/'session.json').read_text())
    assert meta['id'] == manifest['session'] and meta.get('closed_at') and meta.get('input_on_clock')
    stereo = read_wav(directory/'audio.wav', 2)
    frames, cost = await replay(client, url, stereo)
    events = [parse(line) for line in (directory/'golive.log').read_text().splitlines()]
    events = [r for r in events if r.get('session') == meta['id'] and r.get('time')]
    # Per-frame traces stay in the private working directory, never in Git.
    (Path(output_dir)/(manifest['id']+'-frames.json')).write_text(json.dumps(frames))
    return {'call':manifest['id'], 'audio_seconds':len(stereo)/16000,
            'input_dropped_ms':meta.get('input_dropped_ms',0), 'roundtrip':cost,
            'bot_state':'output-energy approximation', 'aec_verified':False,
            **correlate(events, frames, meta['created_at'])}
