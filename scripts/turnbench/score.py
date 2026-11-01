#!/usr/bin/env python3
"""Score a goliveturn predictions file with turnbench's own scorer and write
the journal's L1 record (journal/benchmarks/<date>-<build>-l1.json, the
shape internal/benchrec writes). Run inside the turnbench checkout:

    cd ~/turnbench && uv run python ~/golive-telephony/scripts/turnbench/score.py predictions.json \
        --build 4b2f9db --record ~/golive-telephony/journal/benchmarks [--dataset <hf repo|dir>]
"""
import argparse, datetime, json, sys
from pathlib import Path

sys.path.insert(0, str(Path.cwd()))
from turnbench.data import resolve_dataset  # noqa: E402
from turnbench.score import score_submission  # noqa: E402
from turnbench.submission import load_submission  # noqa: E402

def cell(task):
    lat = task.latency()
    return {"recall": task.recall, "fpr": task.fp_rate, "lat_p50": lat.p50, "tp": task.tp, "fn": task.fn, "fp": task.fp, "tn": task.tn}

def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("predictions")
    ap.add_argument("--dataset", default=None)
    ap.add_argument("--build", required=True)
    ap.add_argument("--source", default="box")
    ap.add_argument("--record", default=None, help="directory for the L1 record")
    a = ap.parse_args()
    dataset = resolve_dataset(a.dataset, skip_audio=True) if a.dataset else resolve_dataset(skip_audio=True)
    scores = score_submission(load_submission(a.predictions), dataset)
    eot, intr = cell(scores.task_eot), cell(scores.task_int)
    print(f"EOT recall {eot['recall']:.3f} fpr {eot['fpr']:.3f} latency p50 {eot['lat_p50']:.0f} ms (tp {eot['tp']} fn {eot['fn']} fp {eot['fp']} tn {eot['tn']})")
    print(f"INT recall {intr['recall']:.3f} fpr {intr['fpr']:.3f} latency p50 {intr['lat_p50']:.0f} ms (tp {intr['tp']} fn {intr['fn']} fp {intr['fp']} tn {intr['tn']})")
    if a.record:
        now = datetime.datetime.now().astimezone()
        rec = {
            "layer": "L1", "build": a.build, "at": now.isoformat(), "source": a.source,
            "metrics": {"eot_recall": eot["recall"], "eot_fpr": eot["fpr"], "eot_latency_ms": eot["lat_p50"],
                        "int_recall": intr["recall"], "int_fpr": intr["fpr"], "int_latency_ms": intr["lat_p50"]},
            "counts": {"n_eot_recall": eot["tp"] + eot["fn"], "n_eot_fpr": eot["fp"] + eot["tn"],
                       "n_int_recall": intr["tp"] + intr["fn"], "n_int_fpr": intr["fp"] + intr["tn"]},
            "thresholds": {"eot_fpr": "<=0.15", "int_fpr": "<=0.15"},
            "report": str(Path(a.predictions).resolve()),
            "note": "TurnBench dev, golive detector-only on each channel, the floor rule in cmd/goliveturn",
        }
        d = Path(a.record); d.mkdir(parents=True, exist_ok=True)
        p = d / f"{now.strftime('%Y-%m-%dT%H%M%S')}-{a.build}-l1.json"
        p.write_text(json.dumps(rec, indent=2) + "\n")
        print("recorded", p)

if __name__ == "__main__":
    main()
