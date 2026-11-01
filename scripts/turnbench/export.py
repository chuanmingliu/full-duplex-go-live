#!/usr/bin/env python3
"""Export TurnBench's dev split to what cmd/goliveturn reads: one directory
per conversation with speaker_1.wav and speaker_2.wav (16 kHz mono PCM16)
and an index.json of ids and durations. Run inside the turnbench checkout's
environment, which has the dataset loader and a Hugging Face login:

    cd ~/turnbench && uv run python ~/golive-telephony/scripts/turnbench/export.py ~/golive-turnbench/dev [--limit N]

The dataset is gated (request access on its Hugging Face page, then
`huggingface-cli login`). The audio stays on this machine; its licence is
non-commercial and forbids voice cloning.
"""
import argparse, io, json, sys
from pathlib import Path

import numpy as np
import pyarrow.parquet as pq
import soundfile as sf
import librosa
from huggingface_hub import snapshot_download

sys.path.insert(0, str(Path.cwd()))
from turnbench.data import DEV_DATASET, PINNED_REVISIONS  # noqa: E402

RATE = 16000

def shards(source):
    if Path(source).is_dir():
        return sorted(str(p) for p in Path(source).glob("*.parquet"))
    snap = snapshot_download(source, repo_type="dataset", revision=PINNED_REVISIONS.get(source), allow_patterns="*.parquet")
    return sorted(str(p) for p in Path(snap).rglob("*.parquet"))

def pcm16(samples, sr):
    if samples.ndim > 1:
        samples = samples.mean(axis=1)
    if sr != RATE:
        samples = librosa.resample(samples.astype(np.float32), orig_sr=sr, target_sr=RATE)
    return np.clip(samples, -1.0, 1.0)

def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("out")
    ap.add_argument("--dataset", default=DEV_DATASET)
    ap.add_argument("--limit", type=int, default=0)
    a = ap.parse_args()
    out = Path(a.out); out.mkdir(parents=True, exist_ok=True)
    index = []
    for shard in shards(a.dataset):
        pf = pq.ParquetFile(shard)
        for rg in range(pf.metadata.num_row_groups):
            t = pf.read_row_group(rg, columns=["conversation_id", "speaker_1_audio", "speaker_2_audio"])
            cid = str(t["conversation_id"][0].as_py())
            d = out / cid; d.mkdir(exist_ok=True)
            dur = 0.0
            for k in (1, 2):
                cell = t[f"speaker_{k}_audio"][0].as_py()
                data = cell["bytes"] if isinstance(cell, dict) else cell
                samples, sr = sf.read(io.BytesIO(data), dtype="float32", always_2d=False)
                dur = max(dur, len(samples) / sr)
                sf.write(d / f"speaker_{k}.wav", pcm16(samples, sr), RATE, subtype="PCM_16")
            index.append({"conversation_id": cid, "duration_s": round(dur, 3)})
            print(cid, f"{dur:.1f}s", flush=True)
            if a.limit and len(index) >= a.limit:
                break
        if a.limit and len(index) >= a.limit:
            break
    (out / "index.json").write_text(json.dumps(index, indent=1) + "\n")
    print("wrote", out / "index.json", len(index), "conversations")

if __name__ == "__main__":
    main()
