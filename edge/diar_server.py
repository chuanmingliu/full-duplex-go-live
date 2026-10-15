"""Streaming speaker-activity service on Nemotron-3-Diarization, for golive's
edge gateway.

  GET  /health                          {"ok":true,"model":...,"mode":...,"lag_ms":...}
  WS   /diarize                         one session: audio in, labelled frames out

Wire (see golive internal/focus/wire.go): the client sends binary messages of
16 kHz PCM16 mono prefixed by an 8-byte little-endian session time in ms; the
server first sends {"type":"ready","lag_ms":..,"frame_ms":80,"model":..}, then
{"type":"frames","start_ms":..,"frame_ms":80,"p":[[p0,p1,..],..]} as the model
labels audio. Compute runs in a thread per session; the model is shared and
locked, so sessions are served one chunk at a time.

Nothing is written to disk; audio lives only in the session's buffer.
"""
import argparse, asyncio, json, os, struct, threading, time
import numpy as np, torch
from aiohttp import web
from transformers import AutoProcessor, AutoModelForAudioFrameClassification

ap = argparse.ArgumentParser()
ap.add_argument("--model", default=os.path.expanduser("~/edge/diar/model"))
ap.add_argument("--mode", default="ultra_low_latency")
ap.add_argument("--threads", type=int, default=4)
ap.add_argument("--host", default="127.0.0.1")
ap.add_argument("--port", type=int, default=8031)
ap.add_argument("--frame-ms", type=int, default=80, help="how finely the 10 ms output is reported")
args = ap.parse_args()

torch.set_num_threads(args.threads)
proc = AutoProcessor.from_pretrained(args.model)
proc.set_streaming_mode(args.mode)
model = AutoModelForAudioFrameClassification.from_pretrained(args.model).eval()
SR = proc.feature_extractor.sampling_rate
FIRST = proc.num_samples_first_audio_chunk
STEP = proc.num_samples_per_audio_chunk
MEL_STEP = proc.num_mel_frames_per_step
LAG_MS = int(round(STEP / SR * 1000))
lock = threading.Lock()
stats = {"sessions": 0, "chunks": 0, "compute_ms": []}


class Session:
    """One stream: buffers audio, runs the model chunk by chunk, emits frames."""

    def __init__(self):
        self.buf = np.zeros(0, dtype=np.float32)
        self.t0_ms = None          # session time of buf[0]
        self.consumed = 0          # samples handed to the model so far
        self.first = True
        self.mel_idx = MEL_STEP
        self.cache = None
        self.out_frames = 0        # 10 ms frames emitted so far

    def push(self, pcm: bytes, at_ms: int):
        x = np.frombuffer(pcm, dtype="<i2").astype(np.float32) / 32768.0
        if self.t0_ms is None:
            self.t0_ms = at_ms
        self.buf = np.concatenate([self.buf, x])

    def ready(self):
        need = FIRST if self.first else STEP
        if self.first:
            return len(self.buf) >= need
        start = proc.audio_chunk_start(self.mel_idx)
        return len(self.buf) >= start + STEP

    def step(self):
        if self.first:
            chunk, isfirst = self.buf[:FIRST], True
        else:
            start = proc.audio_chunk_start(self.mel_idx)
            chunk, isfirst = self.buf[start:start + STEP], False
        t = time.perf_counter()
        with lock, torch.inference_mode():
            inp = proc(chunk, sampling_rate=SR, is_streaming=True, is_first_audio_chunk=isfirst,
                       is_last_audio_chunk=False, return_tensors="pt")
            out = model(**inp, speaker_cache=self.cache)
            self.cache = out.speaker_cache
        ms = (time.perf_counter() - t) * 1000
        stats["chunks"] += 1
        stats["compute_ms"].append(ms)
        if len(stats["compute_ms"]) > 1000:
            del stats["compute_ms"][:500]
        if self.first:
            self.first = False
        else:
            self.mel_idx += MEL_STEP
        prob = torch.sigmoid(out.logits[0]).numpy()  # (n, 8) at 10 ms
        start_ms = self.t0_ms + self.out_frames * 10
        self.out_frames += len(prob)
        # report at frame_ms granularity: the mean over each group of 10 ms frames
        g = max(1, args.frame_ms // 10)
        n = (len(prob) // g) * g
        if n == 0:
            return None
        grouped = prob[:n].reshape(-1, g, prob.shape[1]).mean(1)
        # drop speakers never seen so far in this message; keep index order by padding to the last active
        last = int(np.max(np.nonzero(grouped.max(0) > 0.05)[0])) + 1 if (grouped.max(0) > 0.05).any() else 1
        return {"type": "frames", "start_ms": int(start_ms), "frame_ms": g * 10,
                "p": [[round(float(v), 3) for v in row[:last]] for row in grouped], "compute_ms": int(ms)}


async def health(_):
    c = stats["compute_ms"][-200:]
    return web.json_response({"ok": True, "model": "nemotron-3-diarization", "mode": args.mode, "lag_ms": LAG_MS,
                              "frame_ms": args.frame_ms, "threads": args.threads, "sessions": stats["sessions"],
                              "chunks": stats["chunks"],
                              "compute_ms_p50": int(np.percentile(c, 50)) if c else None,
                              "compute_ms_p90": int(np.percentile(c, 90)) if c else None})


async def diarize(request):
    ws = web.WebSocketResponse(max_msg_size=4 << 20)
    await ws.prepare(request)
    stats["sessions"] += 1
    await ws.send_json({"type": "ready", "lag_ms": LAG_MS, "frame_ms": args.frame_ms, "model": "nemotron-3-diarization", "mode": args.mode})
    s = Session()
    loop = asyncio.get_running_loop()
    q: asyncio.Queue = asyncio.Queue()

    async def worker():
        while True:
            item = await q.get()
            if item is None:
                return
            while s.ready():
                msg = await loop.run_in_executor(None, s.step)
                if msg and not ws.closed:
                    await ws.send_json(msg)
    w = asyncio.create_task(worker())
    try:
        async for msg in ws:
            if msg.type == web.WSMsgType.BINARY and len(msg.data) >= 8:
                (at_ms,) = struct.unpack("<Q", msg.data[:8])
                s.push(msg.data[8:], at_ms)
                if q.empty():
                    q.put_nowait(1)
            elif msg.type in (web.WSMsgType.CLOSE, web.WSMsgType.ERROR):
                break
    finally:
        q.put_nowait(None)
        await w
        stats["sessions"] -= 1
    return ws


app = web.Application()
app.add_routes([web.get("/health", health), web.get("/diarize", diarize)])
print(f"nemotron diarization: mode {args.mode}, lag {LAG_MS} ms, chunk {STEP} samples, threads {args.threads}, on {args.host}:{args.port}", flush=True)
web.run_app(app, host=args.host, port=args.port, print=None)
