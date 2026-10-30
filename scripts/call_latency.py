#!/usr/bin/env python3
"""Read golive's numeric turn logs; never output transcripts or raw log lines."""
import argparse
from collections import Counter
import json
import math
from pathlib import Path
import re
import shlex
import statistics
import sys


MESSAGE = "turn: response latency"
TOOL_DELIVERY = "tools: delivering a task's result"


def integer(row, key):
    value = row.get(key)
    if isinstance(value, bool):
        return None
    if isinstance(value, int):
        return value
    if isinstance(value, str) and re.fullmatch(r"-?\d+", value):
        return int(value)
    return None


def boolean(row, key):
    value = row.get(key)
    if value is True or value == "true":
        return True
    if value is False or value == "false":
        return False
    return None


def parse(line):
    if line.lstrip().startswith("{"):
        row = json.loads(line)
        if not isinstance(row, dict):
            raise ValueError("not an object")
    else:
        row = dict(word.split("=", 1) for word in shlex.split(line) if "=" in word)
    return row if row.get("msg") in (MESSAGE, TOOL_DELIVERY) else None


def join_tool_results(records, links, counts):
    """Fill missing caller content only with an explicit monotonic handoff.

    The offset starts at the originating turn's close, not its last sound.
    The ordinary analyzer adds that caller's end silence once afterwards.
    Wall-clock timestamps and adjacency are deliberately never consulted.
    """
    by_turn, by_task, by_origin, by_result = {}, Counter(), Counter(), Counter()
    for row in records:
        key = (str(row.get("session", "")), str(row.get("turn", "")))
        by_turn.setdefault(key, []).append(row)
    for link in links:
        session = str(link.get("session", ""))
        by_task[(session, str(link.get("task", "")))] += 1
        by_origin[(session, str(link.get("origin_turn", "")))] += 1
        by_result[(session, str(link.get("turn", "")))] += 1

    def invalid():
        counts["invalid_tool_links"] += 1
        counts["unverified"] += 1

    for link in links:
        counts["tool_result_links"] += 1
        if any(not isinstance(link.get(key), str) or not link[key]
               for key in ("session", "task", "turn", "origin_turn")):
            invalid()
            continue
        session, task = link["session"], link["task"]
        origin_key, result_key = (session, link["origin_turn"]), (session, link["turn"])
        origins, children = by_turn.get(origin_key, []), by_turn.get(result_key, [])
        if len(origins) != 1 or origin_key == result_key:
            invalid()
            continue
        origin = origins[0]
        offset = integer(link, "origin_to_result_start_ms")
        if (offset is None or offset < 0 or len(children) != 1 or
                by_task[(session, task)] != 1 or by_origin[origin_key] != 1 or
                by_result[result_key] != 1):
            invalid()
            continue
        child = children[0]
        origin_audio = integer(origin, "first_audio_out_ms")
        pause = integer(origin, "end_silence_ms")
        child_audio = integer(child, "first_audio_out_ms")
        child_content = integer(child, "first_content_ms")
        original_content = boolean(origin, "first_content_present")
        # An opener canceled before content remains a backchannel. A later
        # result must not silently turn it back into an answered caller turn.
        cancelled_origin = original_content is False and boolean(origin, "truncated") is True
        if (boolean(origin, "response_to_speech") is not True or
                original_content is None or cancelled_origin or
                boolean(origin, "first_audio_present") is not True or
                boolean(origin, "opened") is None or
                boolean(origin, "truncated") is None or
                origin_audio is None or pause is None or pause < 0 or
                origin_audio + pause < 0 or
                boolean(child, "response_to_speech") is not False or
                boolean(child, "first_audio_present") is not True or
                boolean(child, "first_content_present") is not True or
                boolean(child, "opened") is None or boolean(child, "truncated") is None or
                integer(child, "end_silence_ms") != 0 or
                child_audio is None or child_content is None or
                child_audio < 0 or child_content < child_audio or
                offset + child_content < origin_audio):
            invalid()
            continue
        # A caller that already received content needs no replacement timestamp.
        if original_content is True:
            continue
        origin["first_content_ms"] = offset + child_content
        origin["first_content_present"] = True
        origin["_tool_result_interrupted"] = boolean(child, "truncated")
        counts["tool_results_joined"] += 1


def distribution(values, target):
    values = sorted(values)
    if not values:
        return dict(n=0, p50_ms=None, p90_ms=None, p95_ms=None, max_ms=None, at_or_above_target=0)
    percentile = lambda p: values[math.ceil(p * len(values)) - 1]
    return dict(n=len(values), p50_ms=statistics.median(values),
                p90_ms=percentile(.9), p95_ms=percentile(.95), max_ms=values[-1],
                at_or_above_target=sum(value >= target for value in values))


def analyse(lines, pause, target):
    counts = Counter({key: 0 for key in (
        "records", "answered", "non_speech", "cancelled", "interrupted_answers",
        "missing", "unverified", "duplicates", "malformed", "content_fallbacks", "assumed_pause",
        "tool_result_links", "tool_results_joined", "invalid_tool_links")})
    seen, rows, audio, content, reply = set(), [], [], [], []
    records, links = [], []
    for line in lines:
        if MESSAGE not in line and TOOL_DELIVERY not in line:
            continue
        try:
            row = parse(line)
        except (ValueError, TypeError):
            counts["malformed"] += 1
            continue
        if row is None:
            continue
        (records if row["msg"] == MESSAGE else links).append(row)
    join_tool_results(records, links, counts)
    for row in records:
        counts["records"] += 1
        identity = (str(row.get("session", "")), str(row.get("turn", "")))
        if identity[1] and identity in seen:
            counts["duplicates"] += 1
            continue
        seen.add(identity)
        spoken = boolean(row, "response_to_speech")
        asr, llm = integer(row, "asr_final_ms"), integer(row, "llm_first_token_ms")
        if spoken is False:
            counts["non_speech"] += 1
            continue
        if spoken is None and asr == 0 and llm == 0:
            # Preserve the old report's greeting heuristic, but it cannot prove
            # this was not an unanswered or zero-ms caller response for a gate.
            counts["non_speech"] += 1
            counts["unverified"] += 1
            continue
        audio_present = boolean(row, "first_audio_present")
        content_present = boolean(row, "first_content_present")
        opened = boolean(row, "opened")
        opener_ms = integer(row, "opener_ms")
        legacy = spoken is None or audio_present is None or content_present is None or opened is None
        if opened is None:
            opened = opener_ms is not None and opener_ms > 0
        truncated = boolean(row, "truncated") is True or row.get("_tool_result_interrupted") is True
        if truncated and (audio_present is False or content_present is False):
            # An opener cut off before any answer is a backchannel, not an answer.
            counts["cancelled"] += 1
            continue
        silence = integer(row, "end_silence_ms")
        if silence is None or silence < 0:
            silence = pause
            counts["assumed_pause"] += 1
            legacy = True
        # Older logs used zero for an absent pause; retain the old report's default.
        elif silence == 0 and spoken is None:
            silence = pause
            counts["assumed_pause"] += 1
            legacy = True
        if legacy or not identity[1]:
            counts["unverified"] += 1
        first_audio = integer(row, "first_audio_out_ms")
        first_content = integer(row, "first_content_ms")
        if audio_present is False:
            first_audio = None
        # Legacy zero is ambiguous unless a nonzero opener proves audio existed.
        if first_audio == 0 and audio_present is None and not opened:
            first_audio = None
        if content_present is False:
            first_content = None
        elif "first_content_ms" not in row and not opened and first_audio is not None:
            # Before opener instrumentation, first audio WAS the answer; the same
            # identity holds for explicit modern non-opener turns.
            first_content = first_audio
            counts["content_fallbacks"] += 1
        if first_content == 0 and content_present is None and opened:
            first_content = None
        if truncated and opened and content_present is None:
            # Old emitMetrics fabricated a content timestamp from the opener's
            # length even when no answer followed it. A truncated legacy opener
            # therefore cannot establish an observed first content.
            first_content = None
        if first_audio is not None and first_audio + silence < 0:
            first_audio = None
        if first_content is not None and (first_content + silence < 0 or
                                         (first_audio is not None and first_content < first_audio)):
            first_content = None
        if first_audio is None or first_content is None:
            counts["missing"] += 1
        else:
            counts["answered"] += 1
            counts["interrupted_answers"] += int(truncated)
        if first_audio is not None:
            audio.append(first_audio + silence)
            reply.append(first_audio)
        if first_content is not None:
            content.append(first_content + silence)
        rows.append((row, first_audio, first_content, silence))
    return dict(scope="last_sound_to_golive_output", target_ms=target,
                percentile="nearest_rank", counts=dict(counts),
                first_audio=distribution(audio, target), first_content=distribution(content, target),
                reply=distribution(reply, target)), rows


def gate_reasons(report, minimum, metric):
    counts = report["counts"]
    reasons = []
    for key in ("missing", "unverified", "duplicates", "malformed"):
        if counts[key]:
            reasons.append(f"{counts[key]} {key} record(s)")
    names = {"first-audio": ("first_audio",), "content": ("first_content",),
             "both": ("first_audio", "first_content")}[metric]
    for name in names:
        stats = report[name]
        if stats["n"] < minimum:
            reasons.append(f"{name}: {stats['n']} samples, need {minimum}")
        if stats["p95_ms"] is not None and stats["p95_ms"] >= report["target_ms"]:
            reasons.append(f"{name}: p95 {stats['p95_ms']} ms is not below {report['target_ms']} ms")
    return reasons


def format_number(value):
    return "-" if value is None else f"{value:g}"


def print_report(report, rows, gated):
    print(f"{'time':9} {'turn':12} {'spec':5} {'start':>6} {'asr':>6} {'llm':>6} "
          f"{'ttft':>6} {'seg':>6} {'tts':>6} {'audio':>7} {'+pause':>7} {'content+pause':>13}")
    for row, audio, content, pause in rows:
        time_match = re.search(r"\d{2}:\d{2}:\d{2}", str(row.get("time", "")))
        time = time_match.group() if time_match else "-"
        turn = str(row.get("turn", ""))
        if not re.fullmatch(r"(?:item_|turn_)[A-Za-z0-9_-]{1,50}", turn):
            turn = "-"
        start, llm = integer(row, "turn_start_ms"), integer(row, "llm_first_token_ms")
        ttft = llm - start if llm is not None and start is not None else None
        values = (start, integer(row, "asr_final_ms"), llm, ttft,
                  integer(row, "first_segment_ms"), integer(row, "tts_first_audio_ms"))
        spec = str(boolean(row, "speculative")).lower()
        print(f"{time:9} {turn:12} {spec:5} " + " ".join(f"{format_number(v):>6}" for v in values) +
              f" {format_number(audio):>7} {format_number(None if audio is None else audio + pause):>7}"
              f" {format_number(None if content is None else content + pause):>13}")
    print(f"\n{report['counts']['answered']} answered turns. Counts: " +
          ", ".join(f"{key}={value}" for key, value in report["counts"].items() if key != "answered"))
    for name, label in (("first_audio", "last sound → first audio"),
                        ("first_content", "last sound → first content"),
                        ("reply", "turn judged over → first audio")):
        stats = report[name]
        print(f"{label}: p50 {format_number(stats['p50_ms'])}  p90 {format_number(stats['p90_ms'])}"
              f"  p95 {format_number(stats['p95_ms'])}  max {format_number(stats['max_ms'])} ms;"
              f" n={stats['n']}, {stats['at_or_above_target']} at or above {report['target_ms']} ms")
    print("Scope: audio leaving golive; the PBX, carrier and handset leg is not measured.")
    if report["counts"]["tool_results_joined"]:
        print("Tool-result content is joined to the original caller using explicit monotonic offsets; "
              "the caller's pause is added once and result turns are not extra caller samples.")
    if report["counts"]["unverified"]:
        print("Legacy/ambiguous records are estimates; their speech origin and audio presence are not verified.")
    if gated:
        print("Gate: " + ("PASS" if report["gate"]["passed"] else "FAIL — " + "; ".join(report["gate"]["reasons"])))


def positive(value):
    number = int(value)
    if number <= 0:
        raise argparse.ArgumentTypeError("must be positive")
    return number


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("log", nargs="?", default="golive-telephony.log")
    parser.add_argument("pause", nargs="?", type=positive, default=380, help="assumed pause for legacy reports")
    parser.add_argument("--gate", action="store_true", help="exit 1 unless the measured p95 is strictly below target")
    parser.add_argument("--target-ms", type=positive, default=1200)
    parser.add_argument("--min-turns", type=positive, default=20)
    parser.add_argument("--metric", choices=("first-audio", "content", "both"), default="first-audio")
    parser.add_argument("--json", action="store_true", help="output numeric summary only")
    args = parser.parse_args()
    try:
        with Path(args.log).open(encoding="utf-8") as stream:
            report, rows = analyse(stream, args.pause, args.target_ms)
    except (OSError, UnicodeError) as error:
        print(f"cannot read latency log: {error}", file=sys.stderr)
        return 2
    reasons = gate_reasons(report, args.min_turns, args.metric)
    report["gate"] = dict(enabled=args.gate, metric=args.metric, min_turns=args.min_turns,
                          passed=not reasons, reasons=reasons)
    if args.json:
        print(json.dumps(report, indent=2))
    else:
        print_report(report, rows, args.gate)
    return int(args.gate and bool(reasons))


if __name__ == "__main__":
    sys.exit(main())
