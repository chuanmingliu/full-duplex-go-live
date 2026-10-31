#!/usr/bin/env python3
"""Summarize receiver-boundary latency from private transport A/B reports.

Accept LABEL=REPORT.json arguments. Reports and transcripts remain private;
only aggregate timing, validity counts and run identifiers are printed.
This is a simulator receiver measurement, never a handset or content gate.
"""
import argparse
from collections import Counter
import json
import math
from pathlib import Path
import statistics


def summarize(values):
    ordered = sorted(values)
    return {
        "samples": len(ordered),
        "median_ms": statistics.median(ordered) if ordered else None,
        "p95_ms": ordered[math.ceil(.95 * len(ordered)) - 1] if ordered else None,
        "max_ms": max(ordered) if ordered else None,
        "under_1200": sum(value < 1200 for value in ordered),
    }


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("reports", nargs="+", metavar="LABEL=REPORT.json")
    args = parser.parse_args()
    lanes, runs = {}, []
    for spec in args.reports:
        label, separator, filename = spec.partition("=")
        if not separator or not label:
            parser.error("each argument must be LABEL=REPORT.json")
        data = json.loads(Path(filename).read_text())
        values, excluded = [], Counter()
        for gap in data.get("assistant_response_gaps", []):
            value = gap.get("value_ms")
            if gap.get("valid") is not True:
                excluded["invalid"] += 1
            elif gap.get("evidence") != "application_audio_boundary":
                excluded["other_boundary"] += 1
            elif type(value) not in (int, float) or not math.isfinite(value) or value < 0:
                excluded["invalid_value"] += 1
            else:
                values.append(value)
        lanes.setdefault(label, []).extend(values)
        runs.append({"label": label, "run_id": data.get("run_id"),
                     "scenario_id": data.get("scenario_id"),
                     "termination_reason": data.get("termination_reason"),
                     "excluded": dict(excluded), **summarize(values)})
    print(json.dumps({
        "boundary": "simulator application audio; includes any fillers; not handset or semantic content",
        "runs": runs,
        "lanes": {label: summarize(values) for label, values in lanes.items()},
        "caution": "Live model conversations are not paired identical turns. Compare configuration and failures separately.",
    }, indent=2))


if __name__ == "__main__":
    main()
