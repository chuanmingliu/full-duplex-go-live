"""Chinese audio experiment. No language gate, no live engine control.

Synthetic labels express intended conversational function, not independently
validated human prosody. Thresholds are fixed before evaluation. This is NOT
the upstream utterance-end policy: its VAD/backstop implementation is unavailable.
"""
import argparse
import asyncio
import base64
import hashlib
import json
from pathlib import Path
import time
import wave

import aiohttp
import numpy as np
from probe import ReplayBotState, distribution


RATE = 16000
FRAME = 320


def read_wav(path, channels):
    with wave.open(str(path), "rb") as w:
        if (w.getframerate(), w.getsampwidth(), w.getnchannels()) != (RATE, 2, channels):
            raise ValueError("expected_16k_pcm16")
        return np.frombuffer(w.readframes(w.getnframes()), dtype="<i2").reshape(-1, channels).copy()


def alaw_roundtrip(samples):
    # Test-only Python 3.12 dependency; production service does not use audioop.
    import audioop
    raw = samples.astype("<i2").tobytes()
    narrow, _ = audioop.ratecv(raw, 2, 1, RATE, 8000, None)
    restored = audioop.alaw2lin(audioop.lin2alaw(narrow, 2), 2)
    wide, _ = audioop.ratecv(restored, 2, 1, 8000, RATE, None)
    result = np.frombuffer(wide, dtype="<i2")
    return np.pad(result[:len(samples)], (0, max(0, len(samples) - len(result))))


def onset_end(samples):
    active = [i for i in range(0, len(samples)-FRAME+1, FRAME)
              if np.sqrt(np.mean(samples[i:i+FRAME].astype(np.float32)**2)) >= 300]
    return (active[0], active[-1]+FRAME) if active else (None, None)


def summarize_frames(rows, onset_ms, end_ms, expected):
    relevant = [r for r in rows if onset_ms <= r["available_ms"] <= end_ms + 1200]
    floor = next((r["available_ms"] for r in relevant if r["p_floor"] >= .7), None)
    # Diagnostic sustained crossing, not a live intervention rule.
    streak = 0
    sustained = None
    for r in relevant:
        streak = streak + 1 if r["p_floor"] >= .7 else 0
        if streak >= 3:
            sustained = r["available_ms"]
            break
    spoken = [r for r in relevant if r["frame_end_ms"] <= end_ms]
    tail = [r for r in relevant if end_ms <= r["frame_end_ms"] <= end_ms + 200]
    return {"expected": expected, "onset_ms": onset_ms, "end_ms": end_ms,
            "floor_crossing_delay_ms": None if floor is None else floor-onset_ms,
            "floor_three_frame_delay_ms": None if sustained is None else sustained-onset_ms,
            "floor_within_700ms": floor is not None and floor-onset_ms <= 700,
            "peak_floor_during_speech": max((r["p_floor"] for r in spoken), default=0),
            "peak_backchannel_during_speech": max((r["p_backchannel"] for r in spoken), default=0),
            "mean_backchannel_first_200ms_after_end": float(np.mean([r["p_backchannel"] for r in tail])) if tail else None}


async def replay(client, url, stereo, *, continuous_bot=False, paced=False):
    rows, costs = [], []
    state = ReplayBotState()
    start = time.perf_counter()
    async with client.ws_connect(url + "/v1/stream") as ws:
        await ws.send_json({"type":"start", "sample_rate":RATE, "language":"zh", "aec":False})
        ready = await ws.receive_json()
        assert ready["type"] == "ready" and ready["fallback"] == "legacy" and not ready["control_allowed"]
        for seq, offset in enumerate(range(0, len(stereo)-639, 640)):
            chunk = stereo[offset:offset+640]
            if paced:
                await asyncio.sleep(max(0, start + (seq+1)*.04 - time.perf_counter()))
            bot = []
            for i in (0, 320):
                if continuous_bot:
                    # Controlled scenario: bot speaking continuously from t=0.
                    bot.append([1, min(1, (offset+i)/RATE/5), 1, 1, 0, 0, 0, 0])
                else:
                    bot.append(state.frame(chunk[i:i+320, 1]))
            at = time.perf_counter()
            await ws.send_json({"type":"audio", "seq":seq,
                                "pcm_s16le":base64.b64encode(chunk[:,0].copy().tobytes()).decode(),
                                "bot_state":bot})
            result = await ws.receive_json()
            assert result["type"] == "prediction" and result["seq"] == seq
            assert not result["control_allowed"] and result["fallback"] == "legacy"
            costs.append((time.perf_counter()-at)*1000)
            for i, frame in enumerate(result["frames"]):
                rows.append({"frame_end_ms":(seq*2+i+1)*20,
                             # Both 20 ms frames become available after the 40 ms chunk.
                             "available_ms":(seq+1)*40, "p_floor":frame["p_floor"],
                             "p_backchannel":frame["p_backchannel"], "vad":frame["vad"]})
    return rows, distribution(costs)


async def evaluate(args):
    cases = json.loads(Path(__file__).with_name("chinese_cases.json").read_text())
    clips = sorted(Path(args.clips).glob("spoken_*.wav"))
    assert len(clips) == len(cases), "all fixture clips required; do not silently renumber failed synthesis"
    output = {"language":"zh", "controller":"legacy", "control_actions":0,
              "policy":"raw floor >=0.7 crossing; three consecutive frames separately",
              "pace":"realtime" if args.paced else "unpaced",
              "handset_latency_measured":False, "synthetic_cases":[], "recorded_calls":[]}
    async with aiohttp.ClientSession(timeout=aiohttp.ClientTimeout(total=1800, sock_read=5)) as client:
        async with client.get(args.url + "/healthz") as r:
            output["service_before"] = await r.json()
        for case, path in zip(cases, clips):
            mono = read_wav(path, 1)[:,0]
            onset, end = onset_end(mono)
            assert onset is not None, "silent fixture"
            for codec in ("pcm16", "g711_alaw_8k_roundtrip"):
                user = alaw_roundtrip(mono) if codec != "pcm16" else mono
                user = np.pad(user, (RATE*2, RATE*2))
                stereo = np.column_stack((user, np.zeros(len(user), dtype=np.int16)))
                rows, cost = await replay(client, args.url, stereo, continuous_bot=True, paced=args.paced)
                summary = summarize_frames(rows, 2000+onset//16, 2000+end//16, case["expected"])
                output["synthetic_cases"].append({"id":case["id"], "codec":codec,
                    "fixture_sha256":hashlib.sha256(path.read_bytes()).hexdigest(), **summary, "roundtrip":cost})
        # Synthetic residual echo and low-volume bystander stress; the audio-only
        # classifier is not a caller identity detector. No claim of AEC fidelity.
        base = read_wav(clips[5], 1)[:,0]
        for name, gain in (("silence", 0), ("echo_like_residual", .126), ("quiet_bystander", .178)):
            user = np.pad((base*gain).astype(np.int16), (RATE*2, RATE*2))
            rows, cost = await replay(client, args.url, np.column_stack((user,user)), continuous_bot=True, paced=args.paced)
            output["synthetic_cases"].append({"id":name,"codec":"pcm16",
                **summarize_frames(rows, 2000, 2000+len(base)//16, "no_cut_control"), "roundtrip":cost})
        for archive in args.archive:
            directory = Path(archive)
            manifest = json.loads((directory/"manifest.json").read_text())
            wav = directory/"audio.wav"
            assert hashlib.sha256(wav.read_bytes()).hexdigest() == manifest["files"]["audio.wav"]["sha256"]
            stereo = read_wav(wav, 2)
            rows, cost = await replay(client, args.url, stereo, paced=False)
            # Private trace enables correlation without exporting speech/audio.
            trace = Path(args.output).parent/(manifest["id"]+"-frames.json")
            trace.write_text(json.dumps(rows))
            output["recorded_calls"].append({"call":manifest["id"],"frames":len(rows),
                "audio_seconds":len(stereo)/RATE,"roundtrip":cost,"aec_verified":False,
                "bot_state_source":"recording_energy_approximation", "pace":"unpaced",
                "ground_truth_labels":False})
        async with client.get(args.url + "/healthz") as r:
            output["service_after"] = await r.json()
    Path(args.output).write_text(json.dumps(output, indent=2)+"\n")
    print(json.dumps({"completed":True,"synthetic_cases":len(output["synthetic_cases"]),
                      "recorded_calls":len(output["recorded_calls"]),"control_actions":0}))


if __name__ == "__main__":
    p = argparse.ArgumentParser()
    p.add_argument("--url", default="http://127.0.0.1:18087")
    p.add_argument("--clips", required=True)
    p.add_argument("--archive", action="append", default=[])
    p.add_argument("--paced", action="store_true")
    p.add_argument("--output", required=True)
    asyncio.run(evaluate(p.parse_args()))
