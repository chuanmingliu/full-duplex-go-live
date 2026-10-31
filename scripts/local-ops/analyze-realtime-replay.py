#!/usr/bin/env python3
"""Estimate frame-send to packet-arrival gaps in a private PCM Router replay.

These timings exclude paced playback, SIP, and the handset. They are not
semantic-response or strict end-to-end latency measurements.
"""

import argparse
import base64
import json
import math
from pathlib import Path
import struct


def read_rows(path):
    return [json.loads(line) for line in path.read_text().splitlines()]


def has_signal(encoded):
    values = [value[0] for value in struct.iter_unpack('<h', base64.b64decode(encoded))]
    for offset in range(0, len(values), 480):
        frame = values[offset:offset + 480]
        if frame and math.sqrt(sum(value * value for value in frame) / len(frame)) >= 300:
            return True
    return False


def analyze(directory):
    metadata = json.loads((directory / 'summary.json').read_text())
    if metadata.get('encoding') != 'pcm_s16le' or metadata.get('sample_rate') != 24000:
        raise ValueError('requires a 24 kHz PCM Router replay')
    inputs = read_rows(directory / 'input.jsonl')
    outputs = read_rows(directory / 'output.jsonl')
    first, starts, statuses = {}, {}, {}
    stops = []
    for row in outputs:
        event, at = row['event'], row['t_ms']
        kind = event['type']
        if kind == 'input_audio_buffer.speech_stopped':
            stops.append(at)
        elif kind == 'response.created':
            starts[event['response']['id']] = at
        elif kind == 'response.done':
            statuses[event['response']['id']] = event['response']['status']
        elif kind == 'response.output_audio.delta':
            response = event['response_id']
            if response not in first and has_signal(event['delta']):
                first[response] = at

    pairs = []
    previous_stop = 0
    for index, stop in enumerate(stops):
        voiced = [row['t_ms'] for row in inputs
                  if previous_stop < row['t_ms'] <= stop and row['rms'] >= 300]
        previous_stop = stop
        next_stop = stops[index + 1] if index + 1 < len(stops) else math.inf
        candidates = [(at, key) for key, at in starts.items()
                      if stop <= at < next_stop and key in first]
        if not voiced or not candidates:
            pairs.append({'vad_stop_ms': round(stop), 'valid': False})
            continue
        _, response = min(candidates)
        if first[response] >= next_stop:
            pairs.append({'vad_stop_ms': round(stop), 'valid': False})
            continue
        end = max(voiced)
        pairs.append({'caller_signal_end_ms': round(end), 'vad_stop_ms': round(stop),
                      'first_packet_with_signal_ms': round(first[response]),
                      'gap_ms': round(first[response] - end),
                      'from_vad_notice_ms': round(first[response] - stop)})
    return {'boundary': 'caller frame sent to first reply packet with signal; not playout',
            'seconds_sent': len(inputs) / 50,
            'first_response_signal_ms': round(min(first.values())) if first else None,
            'responses': len(starts), 'statuses': statuses, 'pairs': pairs}


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('capture_directory', type=Path)
    args = parser.parse_args()
    print(json.dumps(analyze(args.capture_directory), indent=2))
