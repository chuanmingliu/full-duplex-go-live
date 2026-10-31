#!/usr/bin/env python3
"""Replay private caller audio through the local Speko bridge or Router; never dial SIP.

Requires websocket-client. The output directory contains private audio and
transcripts. Timings are client observations, not acoustic handset latency.
"""

import argparse
import base64
import hashlib
import json
import math
import os
from pathlib import Path
import re
import struct
import threading
import time
import urllib.request
import uuid
import wave

import websocket


def alaw(value):
    sign = 128 if value >= 0 else 0
    value = min(value if value >= 0 else -value - 1, 32767)
    if value < 256:
        compressed = value >> 4
    else:
        exponent = max(1, value.bit_length() - 8)
        compressed = (exponent << 4) | ((value >> (exponent + 3)) & 15)
    return (compressed | sign) ^ 0x55


def rms_alaw(data):
    total = 0
    for value in data:
        value ^= 0x55
        segment = (value & 112) >> 4
        sample = (value & 15) << 4
        sample = sample + 8 if segment == 0 else (sample + 264) << (segment - 1)
        total += sample * sample
    return math.sqrt(total / len(data)) if data else 0


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--call-template', type=Path, required=True)
    parser.add_argument('--input-wav', type=Path, required=True)
    parser.add_argument('--output-dir', type=Path, required=True)
    parser.add_argument('--env', type=Path, default=Path.home() / '.config/golive/speko.env')
    parser.add_argument('--port', type=int, default=8095)
    parser.add_argument('--vad-silence-ms', type=int, default=300, help='Router server VAD silence threshold, 100-1000 ms')
    parser.add_argument('--listen-first', action='store_true', help='Router only: omit the greeting request and wait for caller audio')
    parser.add_argument('--router-model', help='Use the Speko Router Realtime endpoint directly, without SIP or the local bridge')
    parser.add_argument('--input-offset', type=float, default=0)
    parser.add_argument('--seconds', type=float, default=90)
    parser.add_argument('--ring-seconds', type=float, default=8)
    parser.add_argument('--instructions-suffix', type=Path)
    args = parser.parse_args()
    if not (0 < args.seconds <= 600 and 0 <= args.ring_seconds <= 30 and args.input_offset >= 0):
        parser.error('invalid replay duration, ringing duration, or audio offset')
    if not 100 <= args.vad_silence_ms <= 1000:
        parser.error('VAD silence must be 100-1000 ms')
    if args.vad_silence_ms != 300 and not args.router_model:
        parser.error('--vad-silence-ms requires --router-model')
    if args.listen_first and not args.router_model:
        parser.error('--listen-first requires --router-model')
    template = json.loads(args.call_template.read_text())
    instructions = template['instructions']
    if args.instructions_suffix:
        instructions += '\n' + args.instructions_suffix.read_text()
    values = {}
    for line in args.env.read_text().splitlines():
        match = re.fullmatch(r'\s*(?:export\s+)?([A-Z0-9_]+)\s*=\s*(.*?)\s*', line)
        if match:
            values[match[1]] = match[2].strip('\"\'')
    headers = {'Authorization': 'Bearer ' + values['SPEKO_API_KEY' if args.router_model else 'GOLIVE_AUTH_TOKEN']}
    if args.router_model:
        if not re.fullmatch(r'[a-zA-Z0-9._-]{1,100}', args.router_model):
            parser.error('invalid model name')
        headers['Idempotency-Key'] = uuid.uuid4().hex
    base = f'http://127.0.0.1:{args.port}'
    if not args.router_model:
        with urllib.request.urlopen(urllib.request.Request(base + '/v1/pool', headers=headers), timeout=5) as response:
            if json.load(response)['in_use'] != 0:
                raise RuntimeError('bridge is busy')
    with wave.open(str(args.input_wav), 'rb') as source:
        if (source.getnchannels(), source.getsampwidth(), source.getframerate()) != (1, 2, 8000):
            raise ValueError('input must be mono 8 kHz PCM16 WAV')
        source.setpos(round(args.input_offset * 8000))
        pcm = source.readframes(round(args.seconds * 8000))
    caller = bytes(alaw(value[0]) for value in struct.iter_unpack('<h', pcm))
    frame_bytes, quiet = 160, b'\xd5'
    if args.router_model:
        # Router's Realtime route advertises 24 kHz PCM, not G.711.
        samples = [v[0] for v in struct.iter_unpack('<h', pcm)]
        expanded = []
        for index, value in enumerate(samples):
            following = samples[min(index + 1, len(samples) - 1)]
            expanded.extend((value, round((2 * value + following) / 3), round((value + 2 * following) / 3)))
        caller = struct.pack('<' + 'h' * len(expanded), *expanded)
        frame_bytes, quiet = 960, b'\x00'

    def level(data):
        if not args.router_model:
            return rms_alaw(data)
        samples = [v[0] for v in struct.iter_unpack('<h', data)]
        return math.sqrt(sum(v * v for v in samples) / len(samples)) if samples else 0

    args.output_dir.mkdir(mode=0o700, parents=False, exist_ok=False)
    call_id = 'replay-' + uuid.uuid4().hex[:16]
    summary = dict(call_id=call_id, boundary=('Router WebSocket arrival; not playout or handset' if args.router_model else 'local bridge client; not handset'),
                   model=args.router_model or 'gpt-live-1', listen_first=args.listen_first,
                   encoding='pcm_s16le' if args.router_model else 'g711_alaw',
                   sample_rate=24000 if args.router_model else 8000,
                   vad_silence_ms=args.vad_silence_ms if args.router_model else None,
                   instructions_sha256=hashlib.sha256(instructions.encode()).hexdigest(),
                   input_sha256=hashlib.sha256(pcm).hexdigest(), seconds=args.seconds,
                   input_offset=args.input_offset, ring_seconds=args.ring_seconds,
                   first_signal_ms=None, clears_ms=[], completed=False)
    stop = threading.Event()
    send_failed = threading.Event()
    writer = None
    conn = None
    try:
        endpoint = (f'wss://router.speko.dev/v1/realtime?model={args.router_model}' if args.router_model else
                    base.replace('http:', 'ws:') + '/v1/realtime?call=' + call_id)
        conn = websocket.create_connection(endpoint, header=headers, suppress_origin=True, timeout=15)
        created = json.loads(conn.recv())
        if created['type'] != 'session.created':
            raise RuntimeError('session creation failed')
        if args.router_model:
            summary['actual_model'] = created.get('session', {}).get('model')
            if summary['actual_model'] != args.router_model:
                raise RuntimeError('provider returned a different model')
            session = {'type': 'realtime', 'instructions': instructions, 'output_modalities': ['audio'],
                       'audio': {'input': {'format': {'type': 'audio/pcm', 'rate': 24000},
                                           'transcription': {'model': 'gpt-4o-mini-transcribe', 'language': 'zh'},
                                           'turn_detection': {'type': 'server_vad', 'threshold': 0.5,
                                                              'prefix_padding_ms': 300, 'silence_duration_ms': args.vad_silence_ms,
                                                              'create_response': True, 'interrupt_response': True}},
                                 'output': {'format': {'type': 'audio/pcm', 'rate': 24000}, 'voice': 'marin'}}}
        else:
            session = {'instructions': instructions, 'voice': 'marin',
                       'input_audio_format': 'g711_alaw', 'output_audio_format': 'g711_alaw'}
        conn.send(json.dumps({'type': 'session.update', 'session': session}))
        for _ in range(8):
            updated = json.loads(conn.recv())
            if updated['type'] == 'error':
                raise RuntimeError('session update failed')
            if updated['type'] == 'session.updated':
                # Router emits an unsolicited initial update; it is not our ack.
                if not args.router_model or updated.get('session', {}).get('instructions') == instructions:
                    break
        else:
            raise RuntimeError('session update was not acknowledged')
        if args.router_model:
            accepted = updated['session']['audio']
            configured = accepted['input']['turn_detection']['silence_duration_ms']
            if configured != args.vad_silence_ms:
                raise RuntimeError('provider did not accept requested VAD silence')
            for direction in ('input', 'output'):
                if accepted[direction]['format'] != {'type': 'audio/pcm', 'rate': 24000}:
                    raise RuntimeError('provider did not accept PCM24')
            summary['accepted_vad_silence_ms'] = configured
        time.sleep(args.ring_seconds)
        released = time.monotonic()
        if args.router_model and not args.listen_first:
            conn.send(json.dumps({'type': 'response.create', 'response': {
                'instructions': 'Say the following greeting to begin the call: ' + template['greeting']}}))
        elif not args.router_model:
            request = urllib.request.Request(base + '/v1/opening', headers={**headers, 'Content-Type': 'application/json'},
                                             data=json.dumps({'callId': call_id, 'greeting': template['greeting']}).encode())
            with urllib.request.urlopen(request, timeout=10):
                pass
        summary['feed_started_ms'] = round((time.monotonic() - released) * 1000, 3)

        def feed():
            started = time.monotonic()
            with (args.output_dir / 'input.jsonl').open('w') as record:
                for index in range(math.ceil(args.seconds * 50)):
                    if stop.wait(max(0, started + (index + 1) * .02 - time.monotonic())):
                        return
                    chunk = caller[index * frame_bytes:(index + 1) * frame_bytes].ljust(frame_bytes, quiet)
                    encoded = base64.b64encode(chunk).decode()
                    try:
                        conn.send(json.dumps({'type': 'input_audio_buffer.append', 'audio': encoded}))
                    except Exception:
                        send_failed.set()
                        return
                    record.write(json.dumps({'t_ms': (time.monotonic() - released) * 1000,
                                             'rms': level(chunk), 'audio': encoded}) + '\n')

        writer = threading.Thread(target=feed, daemon=True)
        writer.start()
        conn.settimeout(1)
        with (args.output_dir / 'output.jsonl').open('w') as record:
            while time.monotonic() - released < args.seconds:
                if send_failed.is_set():
                    raise RuntimeError('input sender failed')
                try:
                    event = json.loads(conn.recv())
                except websocket.WebSocketTimeoutException:
                    continue
                elapsed = (time.monotonic() - released) * 1000
                record.write(json.dumps({'t_ms': elapsed, 'event': event}) + '\n')
                if event['type'] == 'error':
                    raise RuntimeError('bridge returned an error; inspect private capture')
                if event['type'] == 'output_audio_buffer.cleared':
                    summary['clears_ms'].append(round(elapsed, 3))
                if event['type'] in ('response.audio.delta', 'response.output_audio.delta') and summary['first_signal_ms'] is None:
                    if level(base64.b64decode(event['delta'])) >= 300:
                        summary['first_signal_ms'] = round(elapsed, 3)
        summary['completed'] = summary['first_signal_ms'] is not None and not send_failed.is_set()
        if not summary['completed']:
            raise RuntimeError('replay did not complete with audible output')
    finally:
        stop.set()
        if conn:
            conn.close()
        if writer:
            writer.join(timeout=16)
        (args.output_dir / 'summary.json').write_text(json.dumps(summary, indent=2) + '\n')
        print(json.dumps(summary))


if __name__ == '__main__':
    # Output recordings are private even under a permissive caller umask.
    os.umask(0o077)
    try:
        main()
    except Exception as error:
        # Dependency/network exceptions can embed headers: never echo them.
        raise SystemExit('Replay failed (' + type(error).__name__ + '); inspect private capture.') from None
