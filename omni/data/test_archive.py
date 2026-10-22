import json
import math
import struct
import wave
from pathlib import Path

from omni.data import archive as arch
from omni.data.build import build

RATE = 16000


def tone(start_s, end_s, total_s, hz):
    n = int(total_s * RATE)
    out = [0] * n
    for i in range(int(start_s * RATE), min(n, int(end_s * RATE))):
        out[i] = int(8000 * math.sin(2 * math.pi * hz * i / RATE))
    return out


def synthetic_call(root: Path, cid="out-test-0001"):
    d = root / "calls" / "2026" / "10" / f"2026-10-22_120000_{cid}"
    d.mkdir(parents=True)
    dur = 20.0
    # caller speaks 1.0-3.0 (turn 1), murmurs 5.5-5.8 over the answer (hold),
    # speaks 9.0-11.0 (turn 2), then barges in at 13.0-14.0 and cuts the answer.
    caller = [a + b + c + e for a, b, c, e in zip(
        tone(1.0, 3.0, dur, 300), tone(5.5, 5.8, dur, 300), tone(9.0, 11.0, dur, 300), tone(13.0, 14.0, dur, 300))]
    assistant = [a + b for a, b in zip(tone(3.9, 7.0, dur, 500), tone(11.9, 13.2, dur, 500))]
    with wave.open(str(d / "audio.wav"), "wb") as w:
        w.setnchannels(2); w.setsampwidth(2); w.setframerate(RATE)
        inter = [v for pair in zip(caller, assistant) for v in pair]
        w.writeframes(struct.pack(f"<{len(inter)}h", *inter))
    call = {
        "id": cid, "build": "test", "duration_s": dur, "prompt_sha": "abc123",
        "turns": [
            {"n": 1, "last_sound_s": 3.0, "audio_out_s": 3.9, "total_ms": 900, "interrupted": False, "cache_hit": False},
            {"n": 2, "last_sound_s": 11.0, "audio_out_s": 11.9, "total_ms": 900, "interrupted": True, "cache_hit": True},
        ],
        "transcript": [
            {"t_s": 0.5, "who": "assistant", "text": "greeting", "kind": "greeting"},
            {"t_s": 3.0, "start_s": 1.0, "who": "caller", "text": "q one", "turn": 1},
            {"t_s": 3.9, "who": "assistant", "text": "a one", "kind": "answer", "turn": 1},
            {"t_s": 5.8, "start_s": 5.5, "who": "caller", "text": "嗯"},
            {"t_s": 11.0, "start_s": 9.0, "who": "caller", "text": "q two", "turn": 2},
            {"t_s": 11.9, "who": "assistant", "text": "a two, cut", "kind": "answer", "turn": 2, "interrupted": True},
            {"t_s": 14.0, "start_s": 13.0, "who": "caller", "text": "wait", "turn": 0},
        ],
        "utterances": [
            {"start_s": 1.0, "end_s": 3.0, "close": "silence"},
            {"start_s": 5.5, "end_s": 5.8, "barge_in": True, "close": "silence"},
            {"start_s": 9.0, "end_s": 11.0, "close": "silence"},
            {"start_s": 13.0, "end_s": 14.0, "barge_in": True, "close": "silence"},
        ],
    }
    (d / "call.json").write_text(json.dumps(call))
    (root / "prompts").mkdir(exist_ok=True)
    (root / "prompts" / "abc123.txt").write_text("you are a test")
    return cid


def label_at(labels, t_s):
    return labels[int(t_s * 1000 / arch.FRAME_MS)]


def test_turn_records_and_floor(tmp_path):
    cid = synthetic_call(tmp_path / "archive")
    call, audio = arch.load_call(next(arch.iter_calls(tmp_path / "archive")))
    recs, dropped = arch.turn_records(call, sim=True, scenario="unit")
    assert [r.turn for r in recs] == [1, 2] and not dropped
    r1, r2 = recs
    assert (r1.caller.start_s, r1.caller.end_s) == (1.0, 3.0)
    assert (r1.assistant.start_s, r1.assistant.end_s) == (3.9, 9.0)  # until the caller's next start
    assert r2.assistant.interrupted and r2.assistant.end_s == 13.0
    assert [h["who"] for h in r2.history] == ["assistant", "caller", "assistant", "caller"]

    labels = arch.floor_labels(call, audio.seconds)
    assert label_at(labels, 0.2) == arch.SILENCE
    assert label_at(labels, 2.0) == arch.LISTEN
    assert label_at(labels, 3.5) == arch.WAIT
    assert label_at(labels, 4.5) == arch.SPEAK
    assert label_at(labels, 5.6) == arch.HOLD    # murmur, answer carried on
    assert label_at(labels, 13.1) == arch.YIELD  # barge-in, answer cut
    assert label_at(labels, 16.0) == arch.SILENCE

    out = tmp_path / "v1"
    m = build(tmp_path / "archive", out, sim_ids={cid}, include_real=False, scenarios={cid: "unit"})
    assert m["calls"] == 1 and m["turns"] == 2
    lines = [json.loads(l) for l in (out / "turns.jsonl").read_text().splitlines()]
    assert lines[0]["scenario"] == "unit" and lines[0]["prompt_sha"] == "abc123"
    cw = arch.read_wav(out / lines[0]["caller"]["wav"])
    assert abs(cw.seconds - 2.0) < 0.01 and max(cw.caller) > 5000
    aw = arch.read_wav(out / lines[1]["assistant"]["wav"])
    assert abs(aw.seconds - 1.1) < 0.01
    assert (out / "prompts" / "abc123.txt").exists()
    assert set(m["files"]) >= {"turns.jsonl", "floor.jsonl", lines[0]["caller"]["wav"]}


def test_real_calls_excluded_by_default(tmp_path):
    synthetic_call(tmp_path / "archive")
    m = build(tmp_path / "archive", tmp_path / "v1", sim_ids=set(), include_real=False, scenarios={})
    assert m["calls"] == 0 and m["dropped"] == {"real call excluded": 1}


def test_realign_drops_repeated_chunks():
    rate, chunk = 16000, 320
    a = [1] * chunk + [2] * chunk + [2] * chunk + [3] * chunk  # one repeated chunk
    audio = arch.Audio(rate, a, [0] * len(a))
    marks = [{"t_s": 0.0, "frames": 0, "bytes": 640},
             {"t_s": 3 * chunk / rate, "frames": 4, "bytes": 640}]  # 4 chunks arrived in 3 chunks' time
    fixed = arch.realign(audio, marks)
    assert len(fixed.caller) == 3 * chunk and fixed.caller[chunk] == 2 and fixed.caller[2 * chunk] == 3
