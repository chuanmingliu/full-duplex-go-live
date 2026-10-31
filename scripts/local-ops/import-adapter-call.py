#!/usr/bin/env python3
"""Import a completed external-engine SIP recording into its test Call Journal.

Run on the ThinkPad. Never dials. Raw adapter evidence is retained unchanged.
Transcript timestamps are event receipt times, not measured handset playback.
"""
import argparse
import datetime as dt
import hashlib
import json
import os
from pathlib import Path
import re
import subprocess
import wave


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--stack', required=True, type=Path)
    parser.add_argument('--call', required=True)
    parser.add_argument('--restart-journal', action='store_true')
    args = parser.parse_args()
    if not re.fullmatch(r'[A-Za-z0-9_-]{1,64}', args.call):
        parser.error('Invalid call ID')
    os.umask(0o077)
    root = args.stack.resolve()
    source = root / 'state/adapter' / args.call
    if not (source / 'summary.json').is_file():
        parser.error('Adapter recording is not closed')
    events = [json.loads(line) for line in (source / 'events.jsonl').read_text().splitlines()]
    ended = next(e for e in reversed(events) if e['type'] == 'call_end')
    answered = next((e for e in events if e['type'] == 'answered'), None)
    start = dt.datetime.fromisoformat(events[0]['at'].replace('Z', '+00:00')) - dt.timedelta(microseconds=events[0]['elapsed_us'])
    session = 'speko_' + args.call
    compose = json.loads((root / 'compose.json').read_text())
    command = compose['services']['journal']['command']
    data = command[command.index('-data') + 1]
    if not data.startswith('/data/') or '..' in Path(data).parts:
        parser.error('Unexpected journal data directory')
    kept = root / 'state' / data[len('/data/'):] / 'calls' / (args.call + '.json')
    if kept.exists():
        parser.error('Call is already imported; original import preserved')
    store = root / 'state/store' / session
    store.mkdir(mode=0o700)
    for name, dest in [('inbound.wav', 'input.pcm'), ('outbound.wav', 'output.pcm')]:
        with wave.open(str(source / name), 'rb') as reader:
            if (reader.getnchannels(), reader.getsampwidth(), reader.getframerate()) != (1, 2, 8000):
                parser.error('Unexpected adapter WAV format')
            (store / dest).write_bytes(reader.readframes(reader.getnframes()))
    metadata = dict(id=session, rate=8000, created_at=start.isoformat(), closed_at=ended['at'],
                    input_on_clock=True, source='SIP adapter capture imported for external Speko engine')
    (store / 'session.json').write_text(json.dumps(metadata, indent=2))
    transcript = []
    assistant = {}
    for event in events:
        if event['type'] not in ('transcript', 'transcript_delta'):
            continue
        data = event.get('data') or {}
        text = data.get('text', '')
        if not text:
            continue
        caller = 'input_audio_transcription' in data.get('event', '')
        line = dict(t_s=event['elapsed_us'] / 1e6, who='caller' if caller else 'assistant',
                    text=text, kind='fragment')
        if caller:
            transcript.append(line)
        else:
            key = data.get('response_id') or data.get('item_id')
            if key and key in assistant:
                prior = assistant[key]
                if event['type'] == 'transcript':
                    prior['text'] = text
                else:
                    prior['text'] += text
            else:
                transcript.append(line)
                if key:
                    assistant[key] = line
    template = json.loads((root / 'state/calls' / (args.call + '.json')).read_text())
    instructions = template['instructions']
    warning = 'Imported SIP recording. Transcript fragments use event receipt times; they are not finalized caller turns or handset latency measurements.'
    warnings = [dict(t_s=0, level='WARN', text=warning, count=1)]
    if ended['data'].get('failed'):
        warnings.append(dict(t_s=ended['elapsed_us'] / 1e6, level='ERROR', count=1,
                             text='Adapter ended: ' + ended['data'].get('reason', 'unknown')))
    record = dict(id=args.call, session=session, started_at=start.isoformat(), ended_at=ended['at'],
                  duration_s=ended['elapsed_us'] / 1e6, build='speko-gpt-live-1', stored=True,
                  prompt_chars=len(instructions), prompt_sha=hashlib.sha256(instructions.encode()).hexdigest(),
                  summary=dict(turns=0), stage_medians=[], turns=[], transcript=transcript,
                  markers=[], events={}, warnings=warnings)
    if answered:
        record['answered_at'] = answered['at']
    kept.parent.mkdir(parents=True, exist_ok=True)
    with kept.open('x') as out:
        json.dump(record, out, indent=2)
    notes = root / 'state/notes/retros'
    notes.mkdir(parents=True, exist_ok=True)
    (notes / (args.call + '.md')).write_text('# Speko adapter recording\n\n' + warning + '\n\nRaw source: state/adapter/' + args.call + '/. Audio and event files are preserved unchanged. No latency metrics are inferred from transcript fragments.\n')
    if args.restart_journal:
        subprocess.run(['docker', 'compose', '-f', 'compose.json', 'restart', 'journal'], cwd=root, check=True)
    print(json.dumps(dict(call=args.call, imported=True, transcript_lines=len(transcript),
                          journal_reload_required=not args.restart_journal)))


if __name__ == '__main__':
    main()
