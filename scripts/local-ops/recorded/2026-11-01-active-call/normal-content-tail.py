"""Read-only timing extraction for the two pinned original-config simulations."""
import importlib.util
import json
import urllib.request
from pathlib import Path

spec = importlib.util.spec_from_file_location('latency', Path.home() / 'golive/call-latency-45b5564.py')
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)
results = []
for call in ('out-mvxk9u0n-11lw', 'out-mvxkn1v4-jeyp'):
    with urllib.request.urlopen('http://192.168.71.9:8790/api/calls/' + call, timeout=15) as response:
        data = json.load(response)
    assert data['call']['id'] == call and data['archive']['archived']
    report, rows = module.analyse((Path(data['archive']['dir']) / 'golive.log').read_text().splitlines(), 380, 1200)
    tail = []
    for row, audio, content, pause in rows:
        if content is None or row.get('_quote_result_joined'):
            continue
        timing = {key: row.get(key) for key in ('turn', 'asr_final_ms', 'llm_first_token_ms', 'first_segment_ms', 'tts_first_audio_ms', 'opener_ms', 'truncated')}
        timing.update(total_content_ms=content + pause, end_silence_ms=pause, content_after_endpoint_ms=content)
        tail.append(timing)
    results.append({'call': call, 'normal_content_tail': sorted(tail, key=lambda row: row['total_content_ms'], reverse=True)[:6]})
print(json.dumps({'scope': 'Server timing, excluding verified quotation-only readouts. Segment/TTS stamps can refer to the cached opener, not the main content stage; no handset inference.', 'calls': results}, indent=2))
