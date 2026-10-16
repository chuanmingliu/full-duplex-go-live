"""Target-speaker service: is this the caller's voice? For golive's focus
side channel, in place of (or beside) the diarizer.

  GET  /health                 {"ok":true,"model":...,"lag_ms":...,"sessions":..}
  WS   /verify                 one session: audio in, labelled frames out

The diarizer answers "who are all the speakers" and runs at about real time on
a CPU, so continuous background speech (a television) puts its labels seconds
behind. The question golive needs answered is narrower — "is this window the
caller" — and a speaker-verification embedding answers it in a few tens of
milliseconds per window, so the labels stay a window behind the audio however
much the background talks.

Wire (see golive internal/focus/wire.go), the diarizer's exactly, so the
client, the tracker and every judgment stay the same:

  client → server   binary: 8-byte little-endian session ms, then 16 kHz
                    PCM16 mono
  client → server   text {"type":"enrol","from_ms":..,"to_ms":..,"save_as":"name"}:
                    the caller spoke in that span of session time; take their
                    voice from it (the server keeps the last keep_s seconds of
                    audio); with save_as, keep the embedding under that name
                    for the next call. {"type":"enrol","profile":"name"} enrols
                    from a kept profile at once, before the caller has spoken;
                    the reply says "found" or not.
  server → client   {"type":"ready","lag_ms":..,"frame_ms":100,"model":..}
  server → client   {"type":"enrolled","from_ms":..,"to_ms":..,"voiced_ms":..}
  server → client   {"type":"frames","start_ms":..,"frame_ms":100,
                     "p":[[p_caller, p_other],..], "sim":[..]}

Before enrolment every frame is [0,0]: nothing is known. After it, each
100 ms frame is scored on the window ending at it (--window-ms of audio):
voiced (energy over --voiced-db) and similar to the enrolment over
--threshold → [0.95, 0]; voiced and not → [0, 0.95]; not voiced → [0, 0].
Speaker 0 is therefore always the caller, and golive's tracker enrols it as
the primary on its own rule.

The embedding model is CAM++ (3D-Speaker, zh/en) as exported for sherpa-onnx;
the features are Kaldi-style 80-dim fbank with mean subtraction, computed here
in numpy so the only runtime is onnxruntime. Nothing is written to disk.
"""
import argparse, asyncio, json, os, struct, threading, time
import numpy as np
import onnxruntime as ort
from aiohttp import web

ap = argparse.ArgumentParser()
ap.add_argument("--model", default=os.path.expanduser("~/edge/tsv/campplus_zh_en.onnx"))
ap.add_argument("--threads", type=int, default=2)
ap.add_argument("--host", default="127.0.0.1")
ap.add_argument("--port", type=int, default=8032)
ap.add_argument("--frame-ms", type=int, default=100, help="how often a score is reported")
ap.add_argument("--window-ms", type=int, default=800, help="how much audio each score looks at")
ap.add_argument("--threshold", type=float, default=0.28, help="cosine similarity over which a window is the caller (measured on a phone line: the caller's own later utterances 0.45–0.68 against a 2 s enrolment, other voices under 0.25)")
ap.add_argument("--enrol-max-s", type=float, default=8.0, help="how much of the caller's voice the enrolment keeps; later enrolments extend it")
ap.add_argument("--voiced-db", type=float, default=-50.0, help="RMS dBFS under which a window is not voiced")
ap.add_argument("--keep-s", type=float, default=20.0, help="seconds of audio kept for a late enrolment")
ap.add_argument("--agree", type=float, default=0.4, help="similarity two enrolment candidates need to count as the same voice (see Session.do_enrol)")
ap.add_argument("--profiles", default=os.path.expanduser("~/edge/tsv/profiles"), help="where named voice profiles (embeddings, one .npy each) are kept across calls")
args = ap.parse_args()

SR = 16000
so = ort.SessionOptions()
so.intra_op_num_threads = args.threads
so.inter_op_num_threads = 1
sess = ort.InferenceSession(args.model, so, providers=["CPUExecutionProvider"])
IN_NAME = sess.get_inputs()[0].name
OUT_NAME = sess.get_outputs()[0].name
lock = threading.Lock()
stats = {"sessions": 0, "windows": 0, "enrolments": 0, "compute_ms": []}


# --- Kaldi-style fbank in numpy (torchaudio.compliance.kaldi.fbank defaults
# as 3D-Speaker uses them: 25 ms hamming window, 10 ms shift, 80 mels,
# dither 0, preemphasis 0.97, DC removed, log mel energies; then mean
# subtraction over time) ---
_WIN = 400
_HOP = 160
_NFFT = 512
_NMEL = 80


def _mel_banks(nfft=_NFFT, nmel=_NMEL, sr=SR, low=20.0, high=0.0):
    high = high if high > 0 else sr / 2 + high if high < 0 else sr / 2
    mel = lambda f: 1127.0 * np.log(1.0 + f / 700.0)
    imel = lambda m: 700.0 * (np.exp(m / 1127.0) - 1.0)
    nbins = nfft // 2 + 1
    fft_freqs = np.arange(nbins) * sr / nfft
    mlo, mhi = mel(low), mel(high)
    centers = mlo + np.arange(nmel + 2) * (mhi - mlo) / (nmel + 1)
    banks = np.zeros((nmel, nbins), dtype=np.float32)
    for i in range(nmel):
        left, center, right = imel(centers[i]), imel(centers[i + 1]), imel(centers[i + 2])
        up = (fft_freqs - left) / (center - left)
        down = (right - fft_freqs) / (right - center)
        banks[i] = np.clip(np.minimum(up, down), 0, None)
    return banks


_BANKS = _mel_banks()
_HAMMING = np.hamming(_WIN).astype(np.float32)


def fbank(x: np.ndarray) -> np.ndarray:
    """x: float32 samples in [-1, 1] at 16 kHz → (T, 80) float32, mean-subtracted."""
    x = x * 32768.0  # kaldi works on int16-scaled samples
    n = 1 + (len(x) - _WIN) // _HOP if len(x) >= _WIN else 0
    if n <= 0:
        return np.zeros((0, _NMEL), dtype=np.float32)
    idx = np.arange(_WIN)[None, :] + _HOP * np.arange(n)[:, None]
    frames = x[idx]
    frames = frames - frames.mean(axis=1, keepdims=True)                       # remove DC
    frames = np.concatenate([frames[:, :1] * (1 - 0.97), frames[:, 1:] - 0.97 * frames[:, :-1]], axis=1)  # preemphasis
    frames = frames * _HAMMING
    spec = np.abs(np.fft.rfft(frames, n=_NFFT, axis=1)) ** 2
    mel = spec @ _BANKS.T
    feats = np.log(np.maximum(mel, 1.1921e-07)).astype(np.float32)
    return feats - feats.mean(axis=0, keepdims=True)


def embed(x: np.ndarray) -> np.ndarray | None:
    f = fbank(x)
    if f.shape[0] < 20:
        return None
    t = time.perf_counter()
    with lock:
        out = sess.run([OUT_NAME], {IN_NAME: f[None]})[0][0]
    ms = (time.perf_counter() - t) * 1000
    stats["windows"] += 1
    stats["compute_ms"].append(ms)
    if len(stats["compute_ms"]) > 1000:
        del stats["compute_ms"][:500]
    e = out.astype(np.float32)
    return e / (np.linalg.norm(e) + 1e-8)


def profile_path(name):
    """The file a named profile lives in; None for a name that is not a
    plain token (nothing from the wire may name a path)."""
    if not name or not all(c.isalnum() or c in "-_" for c in name) or len(name) > 64:
        return None
    return os.path.join(args.profiles, name + ".npy")


def dbfs(x: np.ndarray) -> float:
    r = float(np.sqrt(np.mean(x * x) + 1e-12))
    return 20 * np.log10(r + 1e-12)


class Session:
    def __init__(self):
        self.buf = np.zeros(0, dtype=np.float32)
        self.t0_ms = None      # session time of buf[0]
        self.scored_to = None  # session ms up to which frames were reported
        self.enrol = None      # the caller's embedding
        self.enrol_audio = np.zeros(0, dtype=np.float32)  # the voice it was taken from
        self.candidates = []   # (embedding, voiced audio) of each utterance offered
        self.from_profile = False
        self.keep = int(args.keep_s * SR)

    def push(self, pcm: bytes, at_ms: int):
        x = np.frombuffer(pcm, dtype="<i2").astype(np.float32) / 32768.0
        if self.t0_ms is None:
            self.t0_ms = at_ms
            self.scored_to = at_ms
        self.buf = np.concatenate([self.buf, x])
        if len(self.buf) > self.keep:
            cut = len(self.buf) - self.keep
            self.buf = self.buf[cut:]
            self.t0_ms += cut * 1000 // SR

    def end_ms(self):
        return self.t0_ms + len(self.buf) * 1000 // SR

    def slice(self, from_ms, to_ms):
        a = max(0, (from_ms - self.t0_ms) * SR // 1000)
        b = min(len(self.buf), (to_ms - self.t0_ms) * SR // 1000)
        return self.buf[a:b] if b > a else self.buf[:0]

    def enrol_profile(self, name):
        path = profile_path(name)
        if path is None or not os.path.exists(path):
            return {"type": "enrolled", "profile": name, "found": False}
        e = np.load(path).astype(np.float32)
        self.enrol = e / (np.linalg.norm(e) + 1e-8)
        self.from_profile = True
        stats["enrolments"] += 1
        return {"type": "enrolled", "profile": name, "found": True, "voiced_ms": 0}

    def do_enrol(self, from_ms, to_ms, save_as=None):
        """Offer an utterance as the caller's voice. Whoever picked up is the
        caller, learned from the call itself; one window is not a reliable
        sample of a voice (on a quiet call the caller's later utterances
        scored 0.07–0.25 against a 1.3 s first window), so the enrolment is
        the largest group of offered utterances that agree with each other
        (pairwise similarity >= --agree). One alone enrols tentatively; the
        next can confirm, extend or outvote it. Once a group of two or more
        stands, a further utterance joins only if it agrees with it, so a
        voice in the background cannot creep in. A kept profile, when one
        was loaded, is extended the same way and never outvoted."""
        x = self.slice(from_ms, to_ms)
        hop = SR // 10
        parts = [x[i:i + hop] for i in range(0, len(x) - hop + 1, hop) if dbfs(x[i:i + hop]) > args.voiced_db]
        voiced = np.concatenate(parts) if parts else x
        if len(voiced) < SR * 0.4:
            return None
        e = embed(voiced)
        if e is None:
            return None
        if self.from_profile:
            if float(np.dot(e, self.enrol)) < args.agree:
                return {"type": "enrolled", "from_ms": int(from_ms), "to_ms": int(to_ms), "voiced_ms": int(len(voiced) * 1000 // SR),
                        "candidates": len(self.candidates), "agreeing": 0, "tentative": False, "rejected": True}
            self.candidates.append((e, voiced))
            group = list(range(len(self.candidates)))
        else:
            self.candidates.append((e, voiced))
            n = len(self.candidates)
            # The largest group of mutually agreeing candidates; ties go to
            # the one holding the latest utterance.
            best = []
            for i in range(n):
                group = [i] + [j for j in range(n) if j != i and float(np.dot(self.candidates[i][0], self.candidates[j][0])) >= args.agree]
                if len(group) > len(best) or (len(group) == len(best) and n - 1 in group):
                    best = group
            group = sorted(best)
        pool = np.concatenate([self.candidates[i][1] for i in group])
        if len(pool) > int(args.enrol_max_s * SR):
            pool = pool[-int(args.enrol_max_s * SR):]
        if self.from_profile:
            pool = np.concatenate([self.enrol_audio, pool])[-int(args.enrol_max_s * SR):]
        pe = embed(pool)
        if pe is None:
            return None
        self.enrol, self.enrol_audio = pe, pool
        stats["enrolments"] += 1
        tentative = len(group) < 2 and not self.from_profile
        saved = False
        if save_as and profile_path(save_as) and not tentative:
            os.makedirs(args.profiles, exist_ok=True)
            np.save(profile_path(save_as), pe)
            saved = True
        return {"type": "enrolled", "from_ms": int(from_ms), "to_ms": int(to_ms),
                "voiced_ms": int(len(voiced) * 1000 // SR), "total_ms": int(len(pool) * 1000 // SR),
                "candidates": len(self.candidates), "agreeing": len(group), "tentative": tentative, "saved": saved}

    def score(self):
        """Report every frame_ms up to the end of the buffer."""
        if self.t0_ms is None:
            return None
        end = self.end_ms()
        rows, sims = [], []
        start = self.scored_to
        t = start
        while t + args.frame_ms <= end:
            fe = t + args.frame_ms
            if self.enrol is None:
                rows.append([0.0, 0.0]); sims.append(0.0)
            else:
                x = self.slice(fe - args.window_ms, fe)
                if len(x) < SR * 0.3 or dbfs(x[-SR * args.frame_ms // 1000:]) < args.voiced_db:
                    rows.append([0.0, 0.0]); sims.append(0.0)
                else:
                    e = embed(x)
                    sim = float(np.dot(e, self.enrol)) if e is not None else 0.0
                    sims.append(round(sim, 3))
                    rows.append([0.95, 0.0] if sim >= args.threshold else [0.0, 0.95])
            t = fe
        if not rows:
            return None
        self.scored_to = t
        return {"type": "frames", "start_ms": int(start), "frame_ms": args.frame_ms, "p": rows, "sim": sims}


async def health(_):
    c = stats["compute_ms"][-200:]
    return web.json_response({"ok": True, "model": os.path.basename(args.model), "lag_ms": args.frame_ms,
                              "frame_ms": args.frame_ms, "window_ms": args.window_ms, "threshold": args.threshold,
                              "threads": args.threads, "sessions": stats["sessions"], "windows": stats["windows"],
                              "enrolments": stats["enrolments"],
                              "compute_ms_p50": int(np.percentile(c, 50)) if c else None,
                              "compute_ms_p90": int(np.percentile(c, 90)) if c else None})


async def verify(request):
    ws = web.WebSocketResponse(max_msg_size=4 << 20)
    await ws.prepare(request)
    stats["sessions"] += 1
    await ws.send_json({"type": "ready", "lag_ms": args.frame_ms, "frame_ms": args.frame_ms, "model": "campplus-tsv"})
    s = Session()
    loop = asyncio.get_running_loop()
    q: asyncio.Queue = asyncio.Queue()

    async def worker():
        while True:
            item = await q.get()
            if item is None:
                return
            if isinstance(item, dict):
                if item.get("profile"):
                    msg = await loop.run_in_executor(None, s.enrol_profile, item["profile"])
                else:
                    msg = await loop.run_in_executor(None, s.do_enrol, item["from_ms"], item["to_ms"], item.get("save_as"))
                if msg is None:
                    msg = {"type": "error", "message": "enrolment span had too little voice"}
                if not ws.closed:
                    await ws.send_json(msg)
                continue
            msg = await loop.run_in_executor(None, s.score)
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
            elif msg.type == web.WSMsgType.TEXT:
                try:
                    m = json.loads(msg.data)
                except ValueError:
                    continue
                if m.get("type") == "enrol":
                    q.put_nowait({"from_ms": int(m.get("from_ms", 0)), "to_ms": int(m.get("to_ms", 0)),
                                  "save_as": m.get("save_as"), "profile": m.get("profile")})
            elif msg.type in (web.WSMsgType.CLOSE, web.WSMsgType.ERROR):
                break
    finally:
        q.put_nowait(None)
        await w
        stats["sessions"] -= 1
    return ws


app = web.Application()
app.add_routes([web.get("/health", health), web.get("/verify", verify)])
print(f"target-speaker verification: {os.path.basename(args.model)}, window {args.window_ms} ms every {args.frame_ms} ms, "
      f"threshold {args.threshold}, threads {args.threads}, on {args.host}:{args.port}", flush=True)
web.run_app(app, host=args.host, port=args.port, print=None)
