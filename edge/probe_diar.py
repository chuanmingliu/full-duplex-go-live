"""Time Nemotron-3-Diarization's streaming mode on the CPU, on a real caller track.

usage: probe_diar.py MODEL_DIR WAV [mode] [threads] [mix.wav mix_db]
Prints numbers only: compute per chunk, speakers found, activity per speaker,
and - when the wav is stereo (caller left, assistant right) - how much of each
speaker's activity falls where the assistant was speaking.
"""
import sys, time, numpy as np, soundfile as sf, torch
from transformers import AutoProcessor, AutoModelForAudioFrameClassification

model_dir, wav = sys.argv[1], sys.argv[2]
mode = sys.argv[3] if len(sys.argv) > 3 else "ultra_low_latency"
threads = int(sys.argv[4]) if len(sys.argv) > 4 else 4
torch.set_num_threads(threads)
p = AutoProcessor.from_pretrained(model_dir)
p.set_streaming_mode(mode)
m = AutoModelForAudioFrameClassification.from_pretrained(model_dir).eval()
sr = p.feature_extractor.sampling_rate
audio, fs = sf.read(wav, dtype="float32", always_2d=True)
assistant = audio[:, 1] if audio.shape[1] > 1 else None
caller = audio[:, 0]
if fs != sr:
    import scipy.signal as s
    caller = s.resample_poly(caller, sr, fs).astype("float32")
    if assistant is not None: assistant = s.resample_poly(assistant, sr, fs).astype("float32")
if len(sys.argv) > 6:
    mix, mfs = sf.read(sys.argv[5], dtype="float32", always_2d=True); mix = mix[:, 0]
    if mfs != sr:
        import scipy.signal as s; mix = s.resample_poly(mix, sr, mfs).astype("float32")
    db = float(sys.argv[6]); off = len(caller) // 3
    seg = mix[: min(len(mix), len(caller) - off)]
    gain = 10 ** (db / 20) * (np.sqrt((caller**2).mean()) / max(np.sqrt((seg**2).mean()), 1e-6))
    caller = caller.copy(); caller[off:off + len(seg)] += gain * seg
    print("mixed", round(len(seg) / sr, 1), "s of a second voice at", db, "dB from", round(off / sr, 1), "s")
first = p.num_samples_first_audio_chunk
step = p.num_samples_per_audio_chunk
print("mode", mode, "threads", threads, "first_chunk_samples", first, "chunk_samples", step, "=", step / sr * 1000, "ms", "mel_per_step", p.num_mel_frames_per_step)
chunks = []
chunks.append((caller[:first], True, False))
idx = p.num_mel_frames_per_step
while True:
    start = p.audio_chunk_start(idx)
    if start + step > len(caller):
        chunks.append((caller[start:], False, True)); break
    chunks.append((caller[start:start + step], False, False)); idx += p.num_mel_frames_per_step
print("chunks", len(chunks), "audio", round(len(caller) / sr, 1), "s")
logits, times, cache = [], [], None
with torch.inference_mode():
    for i, (c, isfirst, islast) in enumerate(chunks):
        if len(c) == 0: continue
        t0 = time.perf_counter()
        inp = p(c, sampling_rate=sr, is_streaming=True, is_first_audio_chunk=isfirst, is_last_audio_chunk=islast, return_tensors="pt")
        out = m(**inp, speaker_cache=cache)
        cache = out.speaker_cache
        logits.append(out.logits)
        times.append((time.perf_counter() - t0) * 1000)
lg = torch.cat(logits, dim=1)[0]  # (frames, 8) at 10 ms
prob = torch.sigmoid(lg).numpy()
t = np.array(times[1:])  # skip the first chunk (warm-up)
print("compute per chunk ms: first", round(times[0]), " then p50", round(np.percentile(t, 50)), "p90", round(np.percentile(t, 90)), "max", round(t.max()), " of a", step / sr * 1000, "ms chunk  -> RTF", round(np.percentile(t, 50) / (step / sr * 1000), 2))
active = prob > 0.5
act_ms = active.sum(0) * 10
print("speakers with activity (ms):", {i: int(a) for i, a in enumerate(act_ms) if a > 0})
if assistant is not None:
    frame = 160
    n = min(len(prob), len(assistant) // frame)
    env = np.array([np.sqrt((assistant[i*frame:(i+1)*frame]**2).mean()) for i in range(n)])
    speaking = env > 0.01
    for s in range(8):
        a = active[:n, s]
        if a.sum() == 0: continue
        print(f"speaker {s}: {a.sum()*10} ms active, {int((a & speaking).sum()*100/a.sum())}% of it while the assistant spoke")
segs = p.extract_speaker_dict(lg.unsqueeze(0))[0]
print("segments", len(segs), "first five (start,end,speaker):", [(round(x['Start'],2), round(x['End'],2), x['Speaker']) for x in segs[:5]])
