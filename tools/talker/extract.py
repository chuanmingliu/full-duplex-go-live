#!/usr/bin/env python3
"""Extract private, call-disjoint TALKER examples without printing speech.

The logs supply the early-final caller input and actually played opener. The
journal supplies final answers. All output, including reports, stays outside
the project. Only aggregate counts are printed.
"""

from __future__ import annotations

import argparse
import ast
import collections
from datetime import datetime
import hashlib
import json
import os
from pathlib import Path
import re
import subprocess
import sys
import unicodedata

from prompt import END_TOKEN, first_clause, prompt_for_pair, strip_opener

VERSION = 1
PROJECT_ROOT = Path(__file__).resolve().parents[2]
LOG_PAIR = re.compile(r'(\w+)=("(?:[^"\\]|\\.)*"|\S*)')
BAD_EVENTS = {
    "think: speculation missed; regenerating": "revised",
    "think: abandoning the speculative turn": "abandoned",
    "speak: the opener became a backchannel; the caller went on": "backchanneled",
    "speak: turn truncated": "interrupted",
    "think: resuming the backend with function results": "tool_turn",
}
OPENER_EVENT = "speak: opened the answer from the phrase cache at the pause"
EARLY_FINAL_EVENT = "asr: final for the words so far"
SEGMENT_EVENTS = {"speak: segment synthesized", "speak: segment played from the phrase cache"}


def normalized(text: str) -> str:
    return "".join(ch for ch in unicodedata.normalize("NFKC", text)
                   if not ch.isspace() and not unicodedata.category(ch).startswith("P"))


def timestamp(text: str) -> float:
    return datetime.fromisoformat(text.replace("Z", "+00:00")).timestamp()


def digest(value: object) -> str:
    return hashlib.sha256(json.dumps(value, ensure_ascii=False, sort_keys=True, separators=(",", ":")).encode()).hexdigest()


def parse_line(line: str) -> dict:
    if line.lstrip().startswith("{"):
        value = json.loads(line)
        return value if isinstance(value, dict) else {}
    if not line.startswith("time="):
        return {}
    # Go's slog uses Go string escapes (including \\xNN, not just JSON).
    return {key: ast.literal_eval(value) if value.startswith('"') else value
            for key, value in LOG_PAIR.findall(line)}


def private_output_dir(path: str | Path) -> Path:
    """Refuse project/tracked paths; allow explicitly ignored home-private data.

    This workstation tracks $HOME itself. Its explicitly ignored private data
    root is permitted; an ordinary project repository is never permitted.
    """
    target = Path(path).expanduser().resolve()
    if target == PROJECT_ROOT or PROJECT_ROOT in target.parents:
        raise ValueError("private output must be outside the project")
    git_roots = [p for p in [target, *target.parents] if (p / ".git").exists()]
    for root in git_roots:
        if root != Path.home():
            raise ValueError("private output must be outside git worktrees")
        ignored = subprocess.run(["git", "-C", str(root), "check-ignore", "-q", str(target)], capture_output=True)
        tracked = subprocess.run(["git", "-C", str(root), "ls-files", "--", str(target)], capture_output=True)
        if ignored.returncode != 0 or tracked.returncode != 0 or tracked.stdout:
            raise ValueError("home-private output must be explicitly gitignored and untracked")
    target.mkdir(parents=True, exist_ok=True, mode=0o700)
    target.chmod(0o700)
    return target


def write_private(path: Path, content: str) -> None:
    if path.is_symlink():
        raise ValueError("refusing symlink output")
    fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600)
    os.fchmod(fd, 0o600)
    with os.fdopen(fd, "w", encoding="utf-8") as out:
        out.write(content)


def read_events(path: Path, session: str | None = None) -> tuple[dict, dict]:
    turns: dict[str, dict] = {}
    counts = collections.Counter()
    latest_final = None
    if not path.exists():
        return turns, {"missing_log": 1}
    with path.open(encoding="utf-8", errors="replace") as source:
        for line in source:
            try:
                event = parse_line(line)
            except (ValueError, SyntaxError, TypeError):
                counts["malformed_log_lines"] += 1
                continue
            if session is not None and event.get("session") != session:
                continue
            if event.get("msg") == EARLY_FINAL_EVENT:
                latest_final = event
            turn_id = event.get("turn")
            if not turn_id:
                continue
            turn = turns.setdefault(turn_id, {"delegations": [], "openers": [], "bad": set(), "complete": False,
                                               "segments": [], "completions": []})
            msg = event.get("msg", "")
            if msg == "think: delegating":
                if not turn["delegations"]:
                    turn["early_final"] = latest_final
                turn["delegations"].append(event)
                try:
                    if int(event.get("revision", 0)) > 0:
                        turn["bad"].add("revised")
                except (TypeError, ValueError):
                    turn["bad"].add("invalid_revision")
                if event.get("reason") not in ("speculative", "final_transcript"):
                    turn["bad"].add("tool_or_scripted_delegation")
            elif msg == OPENER_EVENT:
                turn["openers"].append(event.get("text", ""))
            elif msg == "think: backend complete":
                try:
                    calls = int(event.get("tool_calls", -1))
                except (TypeError, ValueError):
                    calls = -1
                if calls > 0:
                    turn["bad"].add("tool_turn")
                elif calls == 0:
                    turn["complete"] = True
                    turn["completions"].append(event)
            elif msg in SEGMENT_EVENTS:
                turn["segments"].append(event.get("text", ""))
            if msg in BAD_EVENTS:
                turn["bad"].add(BAD_EVENTS[msg])
            # Any turn-associated tools event is conservative evidence that a
            # function, scripted ack, or readout participated in this answer.
            if msg.startswith("tools:"):
                turn["bad"].add("tool_turn")
    return turns, dict(counts)


def source_kind(call: dict, call_hash: str, manifest: dict) -> str:
    source = manifest.get(call_hash, call.get("source_kind"))
    if source in ("real", "simulated"):
        return source
    if isinstance(call.get("simulated"), bool):
        return "simulated" if call["simulated"] else "real"
    return "unknown"


def assign_splits(pairs: list[dict], seed: str) -> None:
    """Rank eligible calls, so small archives still have held-out calls."""
    calls = sorted({pair.get("group_hash", pair["call_hash"]) for pair in pairs}, key=lambda value: digest([seed, value]))
    holdout = max(1, round(len(calls) * 0.1)) if len(calls) >= 3 else 0
    train_end, valid_end = len(calls) - 2 * holdout, len(calls) - holdout
    assignments = {value: "train" if index < train_end else "valid" if index < valid_end else "test"
                   for index, value in enumerate(calls)}
    for pair in pairs:
        pair["split"] = assignments[pair.get("group_hash", pair["call_hash"])]


def call_pairs(call: dict, events: dict, call_hash: str, kind: str,
               max_context_turns: int, max_prompt_chars: int) -> tuple[list, dict]:
    pairs = []
    counts = collections.Counter()
    context: list[dict] = []
    if call.get("greeting"):
        context.append({"role": "assistant", "content": call["greeting"]})
    for turn in sorted(call.get("turns") or [], key=lambda item: item.get("n", 0)):
        counts["turns_seen"] += 1
        event = events.get(turn.get("id"), {})
        bad = set(event.get("bad", set()))
        focus = turn.get("focus") or {}
        if any(focus.get(key) for key in ("another_voice", "vetoed", "late")):
            bad.add("other_speaker")
        if turn.get("interrupted"):
            bad.add("interrupted")
        if not event.get("complete"):
            bad.add("no_complete_backend")
        delegations = event.get("delegations", [])
        if len(delegations) != 1 or len(event.get("completions", [])) != 1:
            bad.add("ambiguous_backend")
        if not delegations or delegations[0].get("reason") != "speculative":
            bad.add("no_early_final")
        caller = delegations[0].get("transcript", "").strip() if delegations else ""
        current_at = None
        if delegations:
            try:
                current_at = timestamp(delegations[0]["time"])
            except (KeyError, ValueError, TypeError):
                pass
        if not caller:
            bad.add("empty_caller")
        early = event.get("early_final")
        if not early or normalized(early.get("text", "")) != normalized(caller):
            bad.add("no_matching_early_final")
        else:
            try:
                lag = timestamp(delegations[0]["time"]) - timestamp(early["time"])
                if not 0 <= lag <= 2:
                    bad.add("stale_early_final")
            except (KeyError, ValueError, TypeError):
                bad.add("invalid_early_final_time")
        openers = event.get("openers", [])
        opener = openers[0].strip() if openers else ""
        if not opener:
            bad.add("no_actual_opener")
        if len(openers) > 1:
            bad.add("multiple_openers")
        raw_answer = turn.get("assistant_text", "").strip()
        if "".join(event.get("segments", [])).strip() != raw_answer:
            bad.add("answer_log_mismatch")
        if opener and normalized(raw_answer).startswith(normalized(opener)):
            bad.add("repeated_opener")
        answer = strip_opener(raw_answer, opener)
        if not answer:
            bad.add("empty_answer")
        clause = first_clause(answer)
        if answer and clause is None:
            bad.add("first_clause_too_long_or_invalid")
        if not bad:
            pair = {
                "id": digest([call_hash, turn.get("n")])[:24],
                "call_hash": call_hash, "source_kind": kind,
                "context": [{"role": item["role"], "content": item["content"]} for item in context
                            if item.get("_completed_at", float("-inf")) < (current_at or float("-inf"))],
                "caller": caller, "opener": opener,
                "prompt_options": {"max_context_turns": max_context_turns,
                                   "max_prompt_chars": max_prompt_chars},
                "clause": clause, "full_answer": answer,
                "provenance": {"turn_number": turn.get("n"), "early_final": True,
                               "opener_from_log": True, "revision": 0},
            }
            try:
                prompt_for_pair(pair, max_context_turns=max_context_turns, max_prompt_chars=max_prompt_chars)
            except ValueError:
                bad.add("prompt_too_long")
            else:
                pairs.append(pair)
        if bad:
            counts["turns_rejected"] += 1
            for reason in sorted(bad):
                counts["reject_" + reason] += 1
        # Context may include completed turns without an opener or a long first
        # clause, but never revisions, interruptions, functions or abandoned
        # speech. No future words enter an earlier example.
        context_bad = bad - {"no_actual_opener", "first_clause_too_long_or_invalid", "prompt_too_long", "no_early_final",
                             "no_matching_early_final", "stale_early_final", "invalid_early_final_time", "repeated_opener"}
        if not context_bad and answer:
            prior_caller = turn.get("caller_text", "").strip()
            try:
                completed_at = timestamp(event["completions"][0]["time"])
            except (KeyError, IndexError, ValueError, TypeError):
                completed_at = None
            if prior_caller and completed_at is not None:
                context.extend([{"role": "user", "content": prior_caller, "_completed_at": completed_at},
                                {"role": "assistant", "content": opener + answer, "_completed_at": completed_at}])
    counts["eligible_before_dedup"] = len(pairs)
    return pairs, dict(counts)


def extract(archive: Path, output: Path, *, seed: str = "local-talker-v1",
            max_context_turns: int = 6, max_prompt_chars: int = 768,
            manifest: dict | None = None) -> dict:
    output = private_output_dir(output)
    candidates = []
    stats = collections.Counter()
    kind_counts = collections.Counter()
    call_hashes = set()
    for path in sorted(archive.rglob("call.json")):
        stats["calls_seen"] += 1
        try:
            call = json.loads(path.read_text(encoding="utf-8"))
            if not isinstance(call, dict):
                raise ValueError("invalid call")
            call_hash = digest([call.get("id"), call.get("session"), call.get("started_at")])
            if call_hash in call_hashes:
                stats["duplicate_calls"] += 1
                continue
            call_hashes.add(call_hash)
            kind = source_kind(call, call_hash, manifest or {})
            kind_counts[kind] += 1
            events, log_counts = read_events(path.with_name("golive.log"), call.get("session"))
            stats.update(log_counts)
            if log_counts.get("malformed_log_lines"):
                stats["calls_rejected_malformed_log"] += 1
                continue
            pairs, pair_counts = call_pairs(call, events, call_hash, kind, max_context_turns, max_prompt_chars)
            stats.update(pair_counts)
            dialogue = [(said.get("who"), normalized(said.get("text", ""))) for said in call.get("transcript") or []]
            group_hash = digest(dialogue) if dialogue else call_hash
            for pair in pairs:
                pair["group_hash"] = group_hash
                pair["reference_clause"] = pair["clause"]
                pair["reference_full"] = pair["full_answer"]
            candidates.extend(pairs)
        except (ValueError, TypeError, KeyError, OSError):
            # Exceptions can contain customer text; reports contain counts only.
            stats["unreadable_calls"] += 1
    pairs = []
    seen_inputs, seen_targets = set(), set()
    for pair in sorted(candidates, key=lambda item: (item["call_hash"], item["id"])):
        prompt = prompt_for_pair(pair, max_context_turns=max_context_turns, max_prompt_chars=max_prompt_chars)
        input_key = digest(prompt)
        # Also remove replayed caller/answer pairs when previous context differs.
        target_key = digest([pair["caller"].strip(), pair["opener"].strip(), pair["full_answer"].strip()])
        if input_key in seen_inputs or target_key in seen_targets:
            stats["duplicate_pairs_dropped"] += 1
            continue
        seen_inputs.add(input_key)
        seen_targets.add(target_key)
        pairs.append(pair)
    assign_splits(pairs, seed)
    write_private(output / "pairs.jsonl", "".join(json.dumps(p, ensure_ascii=False) + "\n" for p in pairs))
    splits = {}
    for split in ("train", "valid", "test"):
        selected = [pair for pair in pairs if pair["split"] == split]
        sft = [{"prompt": prompt_for_pair(pair, max_context_turns=max_context_turns, max_prompt_chars=max_prompt_chars),
                "completion": pair["clause"] + END_TOKEN} for pair in selected]
        write_private(output / f"{split}.jsonl", "".join(json.dumps(p, ensure_ascii=False) + "\n" for p in sft))
        write_private(output / f"{split}.pairs.jsonl", "".join(json.dumps(p, ensure_ascii=False) + "\n" for p in selected))
        splits[split] = {"pairs": len(selected), "calls": len({p["call_hash"] for p in selected}),
                         "source_kind": dict(collections.Counter(p["source_kind"] for p in selected))}
    stats["pairs_kept"] = len(pairs)
    report = {"schema_version": VERSION, "seed": seed, "counts": dict(stats),
              "call_source_kind": dict(kind_counts), "splits": splits,
              "prompt": {"max_context_turns": max_context_turns, "max_prompt_chars": max_prompt_chars,
                         "max_clause_content_chars": 12, "thinking": False},
              "privacy": {"source_text_in_report": False, "output_file_mode": "0600"},
              "split_policy": "seed-ranked eligible calls 80/10/10 (rounded, minimum one held-out call each); duplicate prompts and caller/opener/full-answer triples kept once globally"}
    write_private(output / "extraction-report.json", json.dumps(report, indent=2, sort_keys=True) + "\n")
    return report


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--archive", type=Path, required=True)
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument("--seed", default="local-talker-v1")
    parser.add_argument("--max-context-turns", type=int, default=6)
    parser.add_argument("--max-prompt-chars", type=int, default=768)
    parser.add_argument("--source-manifest", type=Path, help="Private JSON call_hash -> real|simulated labels")
    args = parser.parse_args()
    try:
        manifest = json.loads(args.source_manifest.read_text()) if args.source_manifest else None
        report = extract(args.archive, args.output, seed=args.seed, max_context_turns=args.max_context_turns,
                         max_prompt_chars=args.max_prompt_chars, manifest=manifest)
    except (ValueError, TypeError, OSError):
        print("Extraction failed; check private paths, permissions, and configuration.", file=sys.stderr)
        return 1
    print(json.dumps(report, sort_keys=True))
    return 0 if all(report["splits"][split]["pairs"] for split in ("train", "valid", "test")) else 2


if __name__ == "__main__":
    raise SystemExit(main())
