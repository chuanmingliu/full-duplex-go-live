"""Build a dataset version from the archive.

    PYTHONPATH=. python3 -P -m omni.data.build --archive ~/golive-archive --out ~/golive-omni/v1 \
        --sim-ids ~/golive-sim/dials.ids [--include-real] [--scenarios scenarios.json]

Prints counts only.
"""
from __future__ import annotations

import argparse
import hashlib
import json
import shutil
import sys
from collections import Counter
from pathlib import Path

from . import archive as arch


def sha256(path: Path) -> str:
    h = hashlib.sha256()
    with path.open("rb") as f:
        for chunk in iter(lambda: f.read(1 << 20), b""):
            h.update(chunk)
    return h.hexdigest()


def build(archive: Path, out: Path, *, sim_ids: set[str], include_real: bool,
          scenarios: dict[str, str]) -> dict:
    out.mkdir(parents=True, exist_ok=True)
    turns_f = (out / "turns.jsonl").open("w")
    floor_f = (out / "floor.jsonl").open("w")
    files: dict[str, dict] = {}
    drops: Counter[str] = Counter()
    calls = turns = 0
    label_counts: Counter[str] = Counter()
    prompts: set[str] = set()

    for call_dir in arch.iter_calls(archive):
        call, audio = arch.load_call(call_dir)
        cid = call["id"]
        sim = cid in sim_ids
        if not sim and not include_real:
            drops["real call excluded"] += 1
            continue
        if audio is None:
            drops["no recording"] += 1
            continue
        records, dropped = arch.turn_records(call, sim=sim, scenario=scenarios.get(cid, ""), audio=audio)
        for d in dropped:
            drops[d.reason] += 1
        calls += 1
        for r in records:
            rel = Path("audio") / cid
            cw = rel / f"{r.turn:02d}.caller.wav"
            aw = rel / f"{r.turn:02d}.assistant.wav"
            arch.write_wav(out / cw, audio.rate, audio.cut(audio.caller, r.caller.start_s, r.caller.end_s))
            arch.write_wav(out / aw, audio.rate, audio.cut(audio.assistant, r.assistant.start_s, r.assistant.end_s))
            r.caller.wav, r.assistant.wav = str(cw), str(aw)
            for p in (cw, aw):
                files[str(p)] = {"bytes": (out / p).stat().st_size, "sha256": sha256(out / p)}
            turns_f.write(json.dumps(r.to_json(), ensure_ascii=False) + "\n")
            turns += 1
        labels = arch.floor_labels(call, audio)
        label_counts.update(labels)
        floor_f.write(json.dumps({"call_id": cid, "frame_ms": arch.FRAME_MS, "labels": labels}) + "\n")
        if call.get("prompt_sha"):
            prompts.add(call["prompt_sha"])

    turns_f.close()
    floor_f.close()
    (out / "prompts").mkdir(exist_ok=True)
    for sha in prompts:
        src = archive / "prompts" / f"{sha}.txt"
        if src.exists():
            shutil.copy(src, out / "prompts" / f"{sha}.txt")
            files[f"prompts/{sha}.txt"] = {"bytes": src.stat().st_size, "sha256": sha256(src)}
    for name in ("turns.jsonl", "floor.jsonl"):
        files[name] = {"bytes": (out / name).stat().st_size, "sha256": sha256(out / name)}
    manifest = {
        "version": 1, "calls": calls, "turns": turns, "dropped": dict(drops),
        "floor_labels": dict(label_counts), "frame_ms": arch.FRAME_MS,
        "prompts": sorted(prompts), "files": files,
        "bytes": sum(f["bytes"] for f in files.values()),
    }
    (out / "manifest.json").write_text(json.dumps(manifest, indent=1))
    return manifest


def main(argv: list[str] | None = None) -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--archive", required=True, type=Path)
    ap.add_argument("--out", required=True, type=Path)
    ap.add_argument("--sim-ids", type=Path, help="one simulated call id per line")
    ap.add_argument("--include-real", action="store_true")
    ap.add_argument("--scenarios", type=Path, help='json {"<call id>": "<scenario>"}')
    a = ap.parse_args(argv)
    sim_ids = set(a.sim_ids.read_text().split()) if a.sim_ids else set()
    scenarios = json.loads(a.scenarios.read_text()) if a.scenarios else {}
    m = build(a.archive, a.out, sim_ids=sim_ids, include_real=a.include_real, scenarios=scenarios)
    print(json.dumps({k: m[k] for k in ("calls", "turns", "dropped", "floor_labels", "bytes")}))
    return 0


if __name__ == "__main__":
    sys.exit(main())
