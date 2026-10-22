"""M1: the base Qwen3-Omni thinker on the Mac, zero-shot, on archived turns.

    python -m omni.eval.thinker_baseline --data ~/golive/omni/data/v1 \
        --model mlx-community/Qwen3-Omni-30B-A3B-Instruct-4bit --n 50 --out logs/m1.json

Two tasks on each turn's caller wav:
  asr  — "transcribe" → compared to the Tencent transcript (CER, Chinese)
  qa   — caller audio + the call's prompt + history → compared to the
         cascade's answer (not a score, a sample: the answers are read by a person)
Prints numbers; writes the texts to --out only (stays on the Mac).
"""
from __future__ import annotations

import argparse
import json
import statistics
import time
from pathlib import Path


def cer(ref: str, hyp: str) -> float:
    ref, hyp = ref.replace(" ", ""), hyp.replace(" ", "")
    if not ref:
        return 0.0 if not hyp else 1.0
    d = list(range(len(hyp) + 1))
    for i, r in enumerate(ref, 1):
        prev, d[0] = d[0], i
        for j, h in enumerate(hyp, 1):
            prev, d[j] = d[j], min(d[j] + 1, d[j - 1] + 1, prev + (r != h))
    return d[-1] / len(ref)


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--data", required=True, type=Path)
    ap.add_argument("--model", default="mlx-community/Qwen3-Omni-30B-A3B-Instruct-4bit")
    ap.add_argument("--n", type=int, default=50)
    ap.add_argument("--task", choices=["asr", "qa", "both"], default="both")
    ap.add_argument("--max-tokens", type=int, default=120)
    ap.add_argument("--out", type=Path, default=Path("logs/m1.json"))
    a = ap.parse_args()

    from mlx_vlm import load, generate
    from mlx_vlm.prompt_utils import apply_chat_template
    from mlx_vlm.utils import load_config

    t0 = time.time()
    model, processor = load(a.model)
    config = load_config(a.model)
    print(json.dumps({"loaded_s": round(time.time() - t0, 1), "model": a.model}))

    turns = [json.loads(l) for l in (a.data / "turns.jsonl").read_text().splitlines()][: a.n]
    prompts = {}
    rows, cers, asr_ms, qa_ms = [], [], [], []
    for t in turns:
        wav = str(a.data / t["caller"]["wav"])
        row = {"call_id": t["call_id"], "turn": t["turn"], "ref": t["caller"]["text"]}
        if a.task in ("asr", "both"):
            prompt = apply_chat_template(processor, config, "请把这段语音逐字转写成中文，只输出文字。", num_audios=1)
            s = time.time()
            hyp = generate(model, processor, prompt, audio=[wav], max_tokens=a.max_tokens, temperature=0.0, verbose=False)
            hyp = getattr(hyp, "text", hyp)
            asr_ms.append(int((time.time() - s) * 1000))
            row["asr"] = hyp
            row["cer"] = cer(t["caller"]["text"], hyp)
            cers.append(row["cer"])
        if a.task in ("qa", "both"):
            sha = t.get("prompt_sha", "")
            if sha and sha not in prompts:
                p = a.data / "prompts" / f"{sha}.txt"
                prompts[sha] = p.read_text() if p.exists() else ""
            system = prompts.get(sha, "")
            hist = "\n".join(f"{h['who']}: {h['text']}" for h in t.get("history", []))
            user = (f"[之前的对话]\n{hist}\n\n" if hist else "") + "[客户刚刚说的话在音频里。请直接回复，口语，简短。]"
            msgs = ([{"role": "system", "content": system}] if system else []) + [{"role": "user", "content": user}]
            prompt = apply_chat_template(processor, config, msgs, num_audios=1)
            s = time.time()
            ans = generate(model, processor, prompt, audio=[wav], max_tokens=a.max_tokens, temperature=0.0, verbose=False)
            qa_ms.append(int((time.time() - s) * 1000))
            row["qa"] = getattr(ans, "text", ans)
            row["cascade"] = t["assistant"]["text"]
        rows.append(row)
        print(json.dumps({k: row[k] for k in row if k in ("call_id", "turn", "cer")}))

    def q(v, p):
        return int(statistics.quantiles(v, n=100)[p - 1]) if len(v) >= 2 else (v[0] if v else 0)

    summary = {
        "turns": len(rows),
        "cer_mean": round(statistics.mean(cers), 3) if cers else None,
        "cer_median": round(statistics.median(cers), 3) if cers else None,
        "asr_ms_p50": q(asr_ms, 50) if asr_ms else None, "asr_ms_p95": q(asr_ms, 95) if asr_ms else None,
        "qa_ms_p50": q(qa_ms, 50) if qa_ms else None, "qa_ms_p95": q(qa_ms, 95) if qa_ms else None,
        "prompt_chars": {k: len(v) for k, v in prompts.items()},
    }
    a.out.parent.mkdir(parents=True, exist_ok=True)
    a.out.write_text(json.dumps({"summary": summary, "rows": rows}, ensure_ascii=False, indent=1))
    print(json.dumps(summary))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
