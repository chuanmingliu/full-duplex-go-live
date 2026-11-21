"""Exercise the service; optionally replay local stereo audio without exporting it."""
import argparse
import asyncio
import base64
import json
import math
import statistics
import time
import wave

import aiohttp
import numpy as np


def distribution(values):
    ordered = sorted(values)
    return {"n": len(ordered), "p50_ms": round(statistics.median(ordered), 3),
            "p95_ms": round(ordered[math.ceil(.95 * len(ordered)) - 1], 3),
            "max_ms": round(ordered[-1], 3)}


class ReplayBotState:
    """Approximate bot state from recorded output energy, not ground-truth labels."""
    def __init__(self):
        self.time = 0.0
        self.onset = self.stop = self.pause = -5.0
        self.speaking = False
        self.pause_marked = True

    def frame(self, pcm):
        speaking = float(np.sqrt(np.mean(pcm.astype(np.float32) ** 2))) >= 300
        if speaking and not self.speaking:
            self.onset = self.time
        if not speaking and self.speaking:
            self.stop = self.time
            self.pause_marked = False
        if not speaking and not self.pause_marked and self.time - self.stop >= .2:
            self.pause = self.time
            self.pause_marked = True
        value = [int(speaking)] + [min(5., max(0., self.time - t)) / 5
                                  for t in (self.onset, self.pause, self.stop)] + [0, 0, 0, 0]
        self.speaking = speaking
        self.time += .02
        return value


async def probe(args):
    wav = wave.open(args.wav, "rb") if args.wav else None
    if wav:
        assert (wav.getframerate(), wav.getsampwidth(), wav.getnchannels()) == (16000, 2, 2)
    state = ReplayBotState()
    infer, queue, roundtrip = [], [], []
    floor_frames = backchannel_frames = bot_frames = 0
    started = time.perf_counter()
    try:
        async with aiohttp.ClientSession(timeout=aiohttp.ClientTimeout(total=300)) as client:
            async with client.get(args.url + "/healthz") as response:
                response.raise_for_status()
                health = await response.json()
            assert health["mode"] == "shadow" and health["default_controller"] == "legacy"
            async with client.ws_connect(args.url + "/v1/stream") as ws:
                await ws.send_json({"type": "start", "sample_rate": 16000,
                                    "language": "zh", "aec": False})
                ready = await ws.receive_json()
                assert ready["type"] == "ready" and not ready["control_allowed"]
                for seq in range(int(args.seconds * 25)):
                    raw = wav.readframes(640) if wav else bytes(640 * 4)
                    if len(raw) < 640 * 4:
                        break  # Do not invent a partial final model chunk.
                    stereo = np.frombuffer(raw, dtype="<i2").reshape(640, 2)
                    bot = [state.frame(stereo[i:i+320, 1]) for i in (0, 320)]
                    packet = {"type": "audio", "seq": seq,
                              "pcm_s16le": base64.b64encode(stereo[:, 0].copy().tobytes()).decode(),
                              "bot_state": bot}
                    at = time.perf_counter()
                    await ws.send_json(packet)
                    prediction = await ws.receive_json()
                    roundtrip.append((time.perf_counter() - at) * 1000)
                    assert prediction["type"] == "prediction" and prediction["seq"] == seq
                    assert not prediction["control_allowed"] and prediction["fallback"] == "legacy"
                    infer.append(prediction["inference_ms"]); queue.append(prediction["queue_ms"])
                    for metadata, frame in zip(bot, prediction["frames"]):
                        if metadata[0]:
                            bot_frames += 1
                            floor_frames += frame["p_floor"] >= .7
                            backchannel_frames += frame["p_backchannel"] >= .7
            # A subsequent session must remain available after replay cleanup.
            async with client.get(args.url + "/healthz") as response:
                final = await response.json()
    finally:
        if wav:
            wav.close()
    assert infer, "no complete chunks"
    return {"source": "local_private_stereo_replay" if args.wav else "synthetic_silence",
            "mode": "shadow", "controller": "legacy", "control_actions": 0,
            "model_revision": health["model_revision"], "chunks": len(infer),
            "audio_seconds": len(infer) * .04, "elapsed_seconds": round(time.perf_counter() - started, 3),
            "inference": distribution(infer), "queue": distribution(queue),
            "roundtrip": distribution(roundtrip), "estimated_bot_speaking_frames": bot_frames,
            "raw_floor_probability_ge_0_7_frames": floor_frames,
            "raw_backchannel_probability_ge_0_7_frames": backchannel_frames,
            "accuracy_measured": False, "latency_saving_measured": False,
            "aec_verified": False, "language_supported": False,
            "bot_state_source": "recording_energy_approximation; filler flag unavailable",
            "service_errors_after": final["errors"]}


if __name__ == "__main__":
    parser = argparse.ArgumentParser()
    parser.add_argument("--url", default="http://127.0.0.1:18087")
    parser.add_argument("--wav")
    parser.add_argument("--seconds", type=float, default=4)
    args = parser.parse_args()
    if not 0 < args.seconds <= 1800:
        parser.error("seconds must be in (0, 1800]")
    print(json.dumps(asyncio.run(probe(args)), indent=2))
