#!/usr/bin/env python3
"""Check reviewed handset acoustic boundaries against their original recording.

Usage: handset_latency.py recording.wav capture.json annotations.json [--json]

The recorder sidecar uses schema ``handset-acoustic-capture/v1``. Annotations:

    {
      "schema": "handset-acoustic-annotations/v1",
      "call_id": "out-example", "wav_sha256": "<64 lowercase hex characters>",
      "sample_rate": 48000, "frames": 480000,
      "review": {"method": "manual_acoustic", "complete_call": true,
                 "recording_covers_complete_call": true,
                 "omitted_turns": 0, "unresolved_turns": 0},
      "turns": [{"turn_id": "turn_1", "reviewed": true, "status": "answered",
                 "caller_start_sample": 1000, "caller_end_sample": 2000,
                 "first_audio_sample": 2600, "first_content_sample": 3000,
                 "quote_requested": false, "quote_result_sample": null}]
    }

Indices refer to PCM frames in the WAV, starting at zero. Caller start and
assistant starts are inclusive; caller end is exclusive (the first frame
after the caller's last audible speech). Each row represents one caller
response, in chronological order. Include every caller turn; interrupted,
unanswered, omitted, unresolved or unreviewed turns cannot pass this gate.
For a quotation request, set quote_requested=true and mark the first audible
quotation result separately from a generic lead-in in quote_result_sample.

The tool verifies file/provenance consistency and arithmetic. It cannot
independently verify speaker identity, placement, completeness attestations,
or the semantic accuracy of manual boundaries. A passing numeric gate is not
an independent acoustic review, and synthetic fixtures are not handset proof.
No server timestamp, assumed silence, or estimated network delay is used.
"""

import argparse
from datetime import datetime
import hashlib
import json
import math
from pathlib import Path
import re
import statistics
import sys
import wave


SCOPE = "handset_acoustic_sample_clock"
MAX_JSON_BYTES = 2 * 1024 * 1024
MAX_WAV_BYTES = 512 * 1024 * 1024


class EvidenceError(ValueError):
    """An evidence failure safe to report without echoing private input."""


def require(condition, message):
    if not condition:
        raise EvidenceError(message)


def exact_int(value):
    return isinstance(value, int) and not isinstance(value, bool)


def unique_object(pairs):
    result = {}
    for key, value in pairs:
        require(key not in result, "JSON contains duplicate object keys")
        result[key] = value
    return result


def read_json(path):
    try:
        require(path.stat().st_size <= MAX_JSON_BYTES, "JSON evidence exceeds size limit")
        value = json.loads(path.read_text(encoding="utf-8"), object_pairs_hook=unique_object,
                           parse_constant=lambda _: require(False, "JSON contains non-finite numbers"))
    except (OSError, UnicodeError, json.JSONDecodeError) as exc:
        raise EvidenceError("Cannot read valid JSON evidence") from exc
    require(isinstance(value, dict), "JSON evidence must be an object")
    return value


def inspect_wav(path):
    try:
        require(44 <= path.stat().st_size <= MAX_WAV_BYTES, "WAV size is invalid")
        digest = hashlib.sha256()
        with path.open("rb") as source:
            for chunk in iter(lambda: source.read(1024 * 1024), b""):
                digest.update(chunk)
        with wave.open(str(path), "rb") as source:
            require(source.getnchannels() == 1 and source.getsampwidth() == 2 and
                    source.getcomptype() == "NONE", "WAV must be mono PCM16")
            rate, frames = source.getframerate(), source.getnframes()
            require(8000 <= rate <= 384000 and frames > 0, "WAV sample clock is invalid")
            byte_count, signal = 0, False
            for chunk in iter(lambda: source.readframes(65536), b""):
                byte_count += len(chunk)
                signal = signal or any(chunk)
            require(byte_count == frames * 2, "WAV sample data is truncated")
            require(signal, "WAV contains no nonzero audio signal")
    except (OSError, EOFError, wave.Error) as exc:
        raise EvidenceError("Cannot read valid WAV evidence") from exc
    return {"wav_sha256": digest.hexdigest(), "sample_rate": rate, "frames": frames}


def timestamp(value):
    require(isinstance(value, str), "Capture timestamps are missing")
    try:
        parsed = datetime.fromisoformat(value.replace("Z", "+00:00"))
    except ValueError as exc:
        raise EvidenceError("Capture timestamps are invalid") from exc
    require(parsed.tzinfo is not None and parsed.utcoffset() is not None,
            "Capture timestamps must include a timezone")
    return parsed


def verify_capture(meta, wav):
    require(meta.get("schema") == "handset-acoustic-capture/v1", "Unknown capture schema")
    call_id = meta.get("call_id")
    require(isinstance(call_id, str) and re.fullmatch(r"out-[A-Za-z0-9-]{1,80}", call_id),
            "Capture call ID is invalid")
    require(meta.get("source") == "acoustic_speakerphone_microphone",
            "Capture is not identified as acoustic handset evidence")
    require(meta.get("placement_confirmed") is True, "Physical microphone placement is unconfirmed")
    require(meta.get("capture_complete") is True and meta.get("issues") == [],
            "Capture is incomplete or contains discontinuities")
    require(meta.get("stop_reason") == "user", "Capture did not finish with an explicit user stop")
    require(meta.get("channels") == 1 and exact_int(meta.get("channels")) and
            meta.get("sample_format") == "pcm16le", "Capture format is invalid")
    for field in ("wav_sha256", "sample_rate", "frames"):
        require(meta.get(field) == wav[field] and
                (field == "wav_sha256" or exact_int(meta.get(field))),
                "Capture does not match the WAV " + field)
    first, final, maximum = (meta.get(field) for field in
                             ("first_audio_context_frame", "final_audio_context_frame", "max_frames"))
    require(all(exact_int(value) for value in (first, final, maximum)) and first >= 0 and
            final - first == wav["frames"] and 0 < wav["frames"] <= maximum,
            "Capture frame span is inconsistent")
    for field in ("audio_settings", "audio_settings_at_end"):
        settings = meta.get(field)
        require(isinstance(settings, dict) and all(settings.get(key) is False for key in
                ("echoCancellation", "noiseSuppression", "autoGainControl")),
                "Actual microphone processing settings are not confirmed disabled at start and end")
    require(timestamp(meta.get("ended_at")) >= timestamp(meta.get("started_at")),
            "Capture timestamps are reversed")
    return call_id


def verify_annotations(annotation, meta, wav):
    require(annotation.get("schema") == "handset-acoustic-annotations/v1",
            "Unknown annotation schema")
    for field in ("call_id", "wav_sha256", "sample_rate", "frames"):
        require(annotation.get(field) == meta[field] and
                (field not in ("sample_rate", "frames") or exact_int(annotation.get(field))),
                "Annotations do not match capture " + field)
    review = annotation.get("review")
    require(isinstance(review, dict) and review.get("method") == "manual_acoustic" and
            review.get("complete_call") is True and review.get("recording_covers_complete_call") is True,
            "A complete-call manual acoustic review is required")
    require(all(exact_int(review.get(key)) and review[key] == 0 for key in
                ("omitted_turns", "unresolved_turns")),
            "Review contains omitted or unresolved caller turns")
    turns = annotation.get("turns")
    require(isinstance(turns, list), "Annotation turns must be an array")
    seen, previous, rows = set(), None, []
    for turn in turns:
        require(isinstance(turn, dict), "Turn annotation must be an object")
        identifier = turn.get("turn_id")
        require(isinstance(identifier, str) and re.fullmatch(r"turn_[A-Za-z0-9_-]{1,64}", identifier),
                "Turn ID is invalid")
        require(identifier not in seen, "Duplicate caller turn ID")
        seen.add(identifier)
        require(turn.get("reviewed") is True, "Caller turn has not been acoustically reviewed")
        require(turn.get("status") == "answered", "Interrupted or unanswered caller turn cannot pass")
        names = ("caller_start_sample", "caller_end_sample", "first_audio_sample", "first_content_sample")
        values = [turn.get(key) for key in names]
        require(all(exact_int(value) for value in values), "Caller turn has missing or invalid boundaries")
        start, end, audio, content = values
        require(0 <= start < end <= audio <= content < wav["frames"],
                "Caller or assistant sample range is invalid")
        quote = turn.get("quote_requested")
        require(isinstance(quote, bool), "Quotation intent must be explicitly reviewed")
        result = turn.get("quote_result_sample")
        if quote:
            require(exact_int(result) and content <= result < wav["frames"],
                    "Quotation result boundary is missing or invalid")
        else:
            require("quote_result_sample" in turn and result is None,
                    "Non-quotation turn must have a null quotation boundary")
        if previous is not None:
            require(start >= previous["caller_end_sample"],
                    "Caller turns overlap or are out of chronological order")
            last_response = previous["quote_result_sample"] or previous["first_content_sample"]
            require(last_response < start,
                    "A response boundary crosses into the next caller turn")
        row = {"turn_id": identifier, **dict(zip(names, values)), "quote_result_sample": result}
        rows.append(row)
        previous = row
    return rows


def milliseconds(samples, rate):
    return round(samples * 1000 / rate, 6)


def distribution(samples, rate, target):
    values = sorted(samples)
    if not values:
        return {"n": 0, "p50_ms": None, "p95_ms": None, "max_ms": None,
                "p95_samples": None, "at_or_above_target": 0}
    p95 = values[math.ceil(len(values) * .95) - 1]
    return {"n": len(values), "p50_ms": milliseconds(statistics.median(values), rate),
            "p95_ms": milliseconds(p95, rate), "max_ms": milliseconds(values[-1], rate),
            "p95_samples": p95,
            "at_or_above_target": sum(value * 1000 >= target * rate for value in values)}


def analyse(wav_path, meta, annotation, target=1200, minimum=20):
    require(exact_int(target) and 0 < target <= 1200, "Target must be an integer from 1 through 1200 ms")
    require(exact_int(minimum) and minimum >= 20, "Minimum caller sample count must be at least 20")
    wav = inspect_wav(wav_path)
    call_id = verify_capture(meta, wav)
    rows = verify_annotations(annotation, meta, wav)
    rate, reasons = wav["sample_rate"], []
    fields = (("first_audio", "first_audio_sample"), ("first_content", "first_content_sample"),
              ("quote_result", "quote_result_sample"))
    report = {"scope": SCOPE, "assessment": "provided_manual_acoustic_boundaries",
              "independent_acoustic_review_required": True,
              "call_id": call_id, **wav, "target_ms": target, "percentile": "nearest_rank",
              "caller_turns": len(rows)}
    for name, field in fields:
        samples = [row[field] - row["caller_end_sample"] for row in rows if row[field] is not None]
        stats = distribution(samples, rate, target)
        report[name] = stats
        if name != "quote_result" and stats["n"] < minimum:
            reasons.append(name + ": insufficient reviewed caller samples")
        if stats["p95_samples"] is not None and stats["p95_samples"] * 1000 >= target * rate:
            reasons.append(name + ": p95 is not strictly below target")
    report["turns"] = [{**row, **{name + "_ms": None if row[field] is None else
                         milliseconds(row[field] - row["caller_end_sample"], rate)
                         for name, field in fields}} for row in rows]
    report["gate"] = {"passed": not reasons, "min_turns": minimum, "reasons": reasons}
    return report


def print_report(report):
    print("Scope: reviewed handset acoustic boundaries on the WAV sample clock")
    print("This verifies supplied annotations and arithmetic, not their independent acoustic accuracy.")
    for name in ("first_audio", "first_content", "quote_result"):
        stats = report[name]
        print(f"{name}: n={stats['n']} p50={stats['p50_ms']} p95={stats['p95_ms']} "
              f"max={stats['max_ms']} ms; at/above target={stats['at_or_above_target']}")
    print("First content can be a lead-in; quote_result separately marks the audible quotation result.")
    print("Gate: " + ("PASS for supplied reviewed boundaries" if report["gate"]["passed"] else "FAIL"))
    for reason in report["gate"]["reasons"]:
        print("- " + reason)


def main():
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("wav", type=Path)
    parser.add_argument("capture", type=Path)
    parser.add_argument("annotations", type=Path)
    parser.add_argument("--target-ms", type=int, default=1200, help="strict target; may tighten below 1200")
    parser.add_argument("--min-turns", type=int, default=20, help="minimum; may increase above 20")
    parser.add_argument("--json", action="store_true", help="numeric results and bounded record IDs only")
    args = parser.parse_args()
    try:
        report = analyse(args.wav, read_json(args.capture), read_json(args.annotations),
                         args.target_ms, args.min_turns)
    except EvidenceError as exc:
        report = {"scope": SCOPE, "gate": {"passed": False, "reasons": [str(exc)]}}
        if args.json:
            print(json.dumps(report, indent=2))
        else:
            print("Evidence invalid: " + str(exc), file=sys.stderr)
        return 2
    if args.json:
        print(json.dumps(report, indent=2))
    else:
        print_report(report)
    return 0 if report["gate"]["passed"] else 1


if __name__ == "__main__":
    sys.exit(main())
