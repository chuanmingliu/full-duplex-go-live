#!/usr/bin/env python3
"""A toy two-channel conversation for a smoke test of cmd/goliveturn on the
mocks (configs/turnbench-sim.json + .env). Speech is the keyed tone the mock
recognizer hears as a phrase: pitch 220 Hz is phrase 0 (a sentence), 260 Hz
phrase 1 ("yeah"). Writes DIR/toy/speaker_{1,2}.wav and DIR/index.json, plus
DIR/toy/gold.json saying what the rule should find.

    python3 scripts/turnbench/synth.py DIR
"""
import json, math, struct, sys
from pathlib import Path

RATE = 16000

def tone(ms, hz):
    n = RATE * ms // 1000
    out = []
    for i in range(n):
        t = i / RATE
        env = 0.6 + 0.4 * math.sin(2 * math.pi * 4.5 * t)
        edge = 0.01 * RATE
        fade = min(1.0, i / edge, (n - 1 - i) / edge)
        out.append(0.3 * env * fade * math.sin(2 * math.pi * hz * t))
    return out

def wav(path, samples):
    data = b"".join(struct.pack("<h", int(max(-1, min(1, s)) * 32767)) for s in samples)
    hdr = b"RIFF" + struct.pack("<I", 36 + len(data)) + b"WAVE" + b"fmt " + struct.pack("<IHHIIHH", 16, 1, 1, RATE, RATE * 2, 2, 16) + b"data" + struct.pack("<I", len(data))
    path.write_bytes(hdr + data)

def place(track, at_s, samples):
    i = int(at_s * RATE)
    for k, s in enumerate(samples):
        if i + k < len(track):
            track[i + k] += s

dur = 16.0
s1 = [0.0] * int(dur * RATE)
s2 = [0.0] * int(dur * RATE)
# Speaker 1: a sentence 0.5–3.0, a mid-turn pause, 3.5–5.0; turn over at 5.0.
place(s1, 0.5, tone(2500, 220)); place(s1, 3.5, tone(1500, 220))
# Speaker 2: "yeah" at 2.0 over speaker 1 (a backchannel: no INT, no EOT).
place(s2, 2.0, tone(300, 260))
# Speaker 2: a turn 6.0–8.5 into silence (EOT ~8.5+).
place(s2, 6.0, tone(2500, 220))
# Speaker 1 cuts in at 7.5 with a sentence while 2 holds the floor (INT), to 9.5.
place(s1, 7.5, tone(2000, 220))
# Speaker 2 a turn 11–13 (EOT), speaker 1 nothing.
place(s2, 11.0, tone(2000, 220))

root = Path(sys.argv[1]); d = root / "toy"; d.mkdir(parents=True, exist_ok=True)
wav(d / "speaker_1.wav", s1); wav(d / "speaker_2.wav", s2)
(root / "index.json").write_text(json.dumps([{"conversation_id": "toy", "duration_s": dur}]) + "\n")
(d / "gold.json").write_text(json.dumps({"speaker_1": {"eot": [5.0, 9.5], "interruption": [7.5]}, "speaker_2": {"eot": [8.5, 13.0], "interruption": []}}, indent=1) + "\n")
print("wrote", d)
