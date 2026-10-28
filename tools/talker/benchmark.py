#!/usr/bin/env python3
"""Private llama.cpp clause benchmark. stdout contains aggregate numbers only.

Native server API contract: https://github.com/ggml-org/llama.cpp/tree/master/tools/server
Cold means a fresh process, not a flushed operating-system disk cache. Prefix
priming excludes the current caller and opener; exact-query caching is never used.
"""
from __future__ import annotations

import argparse
import contextlib
import json
import math
import os
from pathlib import Path
import socket
import subprocess
import tempfile
import time
import unicodedata
import urllib.error
import urllib.parse
import urllib.request

try:
    from .prompt import CLAUSE_BOUNDARIES, END_TOKEN, FIRST_CLAUSE_LIMIT, prompt_for_pair
except ImportError:
    from prompt import CLAUSE_BOUNDARIES, END_TOKEN, FIRST_CLAUSE_LIMIT, prompt_for_pair

MODES = ("warm_prefix", "warm_no_cache", "cold_process")


def private_path(value: str | Path) -> Path:
    """Reject checkouts, except explicitly ignored/untracked home-private data.

    This workstation tracks $HOME itself. Match the extractor's narrow exception
    for its explicitly ignored private data, never an ordinary project checkout.
    """
    path = Path(value).expanduser().resolve()
    for parent in (path, *path.parents):
        if (parent / ".git").exists():
            if parent != Path.home():
                raise ValueError("private_path_inside_git_checkout")
            ignored = subprocess.run(["git", "-C", str(parent), "check-ignore", "-q", str(path)], capture_output=True)
            tracked = subprocess.run(["git", "-C", str(parent), "ls-files", "--", str(path)], capture_output=True)
            if ignored.returncode or tracked.returncode or tracked.stdout:
                raise ValueError("home_private_path_must_be_gitignored_and_untracked")
    return path


def private_dir(value: str | Path) -> Path:
    path = private_path(value)
    path.mkdir(parents=True, exist_ok=True, mode=0o700)
    path.chmod(0o700)
    return path


def write_private(path: Path, value, *, jsonl=False):
    path = private_path(path)
    descriptor, temporary = tempfile.mkstemp(prefix=".talker-", dir=path.parent)
    try:
        with os.fdopen(descriptor, "w") as out:
            os.fchmod(out.fileno(), 0o600)
            if jsonl:
                for row in value:
                    out.write(json.dumps(row, ensure_ascii=False) + "\n")
            else:
                json.dump(value, out, ensure_ascii=False, indent=2)
                out.write("\n")
        os.replace(temporary, path)
    finally:
        if os.path.exists(temporary):
            os.unlink(temporary)


def read_jsonl(path: str | Path) -> list[dict]:
    with private_path(path).open() as source:
        return [json.loads(line) for line in source if line.strip()]


def percentile(values, fraction):
    if not values:
        return None
    ordered = sorted(values)
    # Nearest rank, explicitly conservative for the small held-out sample.
    return round(ordered[max(0, math.ceil(len(ordered) * fraction) - 1)], 3)


def distribution(values):
    values = [v for v in values if isinstance(v, (float, int)) and math.isfinite(v)]
    return {"n": len(values), "p50": percentile(values, .50),
            "p95": percentile(values, .95), "max": round(max(values), 3) if values else None}


def wilson(successes: int, total: int):
    if not total:
        return {"rate": None, "ci95_low": None, "ci95_high": None}
    p, z = successes / total, 1.959963984540054
    center = (p + z*z/(2*total)) / (1+z*z/total)
    half = z * math.sqrt(p*(1-p)/total + z*z/(4*total*total)) / (1+z*z/total)
    return {"rate": round(p, 6), "ci95_low": round(max(0, center-half), 6),
            "ci95_high": round(min(1, center+half), 6)}


def content_chars(text):
    return sum(not c.isspace() and not unicodedata.category(c).startswith("P") for c in text)


def clause_state(text, *, eos=False, limited=False, max_chars=FIRST_CLAUSE_LIMIT):
    """Completion is punctuation or EOS, never an arbitrary character slice.

    'valid' is structural only. Semantic acceptability belongs to evaluate.py.
    A punctuation boundary preceding a token limit is still a complete clause.
    """
    # Match prompt.first_clause: formatting before the first spoken character
    # is not a completed empty clause, even when it contains a newline.
    text = text.lstrip()
    boundary = next((i for i, c in enumerate(text) if c in CLAUSE_BOUNDARIES), None)
    if boundary is not None:
        clause = text[:boundary+1].strip()
        complete = True
    else:
        clause, complete = text.strip(), bool(eos) and not limited
    length = content_chars(clause)
    special = "<think>" in clause or "</think>" in clause or "<|" in clause
    valid = complete and 0 < length <= max_chars and not special
    return {"clause": clause, "clause_chars": length, "complete": int(complete),
            "valid_clause": int(valid), "empty": int(length == 0),
            "short": int(0 < length <= max_chars),
            "long": int(length > max_chars), "incomplete": int(not complete),
            "clipped": int(limited), "special_output": int(special)}


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, *args, **kwargs):
        raise ValueError("redirect_forbidden")


def http_client():
    # No ambient proxy: local private inference must remain on this machine.
    return urllib.request.build_opener(urllib.request.ProxyHandler({}), NoRedirect())


def local_url(url):
    parsed = urllib.parse.urlparse(url)
    if parsed.scheme != "http" or parsed.hostname not in {"127.0.0.1", "localhost", "::1"}:
        raise ValueError("inference_endpoint_must_be_loopback_http")
    if parsed.username or parsed.password or parsed.query or parsed.fragment:
        raise ValueError("invalid_inference_endpoint")
    if parsed.hostname == "localhost":
        # Do not trust an altered hosts/DNS entry for private inference.
        netloc = "127.0.0.1" + (f":{parsed.port}" if parsed.port else "")
        url = urllib.parse.urlunparse(parsed._replace(netloc=netloc))
    return url.rstrip("/")


def request_json(url, payload, timeout=60):
    request = urllib.request.Request(local_url(url), json.dumps(payload).encode(), {"Content-Type": "application/json"})
    with http_client().open(request, timeout=timeout) as response:
        return json.load(response)


def events(response):
    data = []
    for raw in response:
        line = raw.decode("utf-8").rstrip("\r\n")
        if line.startswith("data:"):
            data.append(line[5:].lstrip())
        elif not line and data:
            joined = "\n".join(data)
            data = []
            if joined != "[DONE]":
                yield json.loads(joined)
    if data and "\n".join(data) != "[DONE]":
        yield json.loads("\n".join(data))


def run_completion(url, prompt, *, cache_prompt, max_tokens=48, timeout=60,
                   max_chars=FIRST_CLAUSE_LIMIT, clock=time.perf_counter):
    started = clock()
    payload = {"prompt": prompt, "stream": True, "cache_prompt": cache_prompt,
               "id_slot": 0, "n_predict": max_tokens, "temperature": 0,
               "seed": 0, "return_tokens": True, "stop": [END_TOKEN, "<|im_start|>"]}
    request = urllib.request.Request(local_url(url)+"/completion", json.dumps(payload).encode(),
                                     {"Content-Type": "application/json"})
    text, final, first_token, first_text, clause_ms = "", {}, None, None, None
    complete_clause_ms = None
    streamed_tokens = 0
    with http_client().open(request, timeout=timeout) as response:
        for event in events(response):
            now = (clock()-started)*1000
            if "error" in event:
                raise RuntimeError("server_stream_error")
            piece = event.get("content", "")
            token_count = len(event.get("tokens", []))
            if first_token is None and (token_count or piece):
                first_token = now
            if first_text is None and piece.strip():
                first_text = now
            text += piece
            streamed_tokens += token_count
            current = clause_state(text, max_chars=max_chars)
            if complete_clause_ms is None and current["complete"]:
                complete_clause_ms = now
            if clause_ms is None and current["valid_clause"]:
                clause_ms = now
            if event.get("stop"):
                final = event
                if event.get("stop_type") == "eos" or event.get("stopped_eos"):
                    eos = True
                else:
                    eos = (event.get("stop_type") == "word" or event.get("stopped_word")) and event.get("stopping_word") == END_TOKEN
                limited = event.get("stop_type") == "limit" or bool(event.get("stopped_limit"))
                state = clause_state(text, eos=eos, limited=limited, max_chars=max_chars)
                if state["complete"] and complete_clause_ms is None:
                    complete_clause_ms = now
                if state["valid_clause"] and clause_ms is None:
                    clause_ms = now
                break
    if not final:
        raise RuntimeError("stream_without_terminal_event")
    timings = final.get("timings", {})
    result = {"text": text, **state, "ttft_ms": first_token, "first_text_ms": first_text,
              "clause_ms": clause_ms if state["valid_clause"] else None,
              "complete_clause_ms": complete_clause_ms,
              "request_ms": (clock()-started)*1000, "streamed_tokens": streamed_tokens,
              "predicted_tokens": timings.get("predicted_n", final.get("tokens_predicted")),
              "prompt_evaluated_tokens": timings.get("prompt_n", final.get("tokens_evaluated")),
              "cached_tokens": timings.get("cache_n", final.get("tokens_cached")),
              "prompt_ms": timings.get("prompt_ms"),
              "truncated_context": int(bool(final.get("truncated"))), "error": 0}
    if result["truncated_context"]:
        result["valid_clause"], result["clause_ms"] = 0, None
    return result


def prefix_for_prompt(prompt):
    prefix, marker, _ = prompt.rpartition("<|im_start|>user\n")
    if not marker or not prefix:
        raise ValueError("prompt_missing_current_caller_boundary")
    return prefix


def prime_prefix(url, prompt, timeout):
    return request_json(local_url(url)+"/completion", {
        "prompt": prefix_for_prompt(prompt), "n_predict": 0, "stream": False,
        "id_slot": 0, "cache_prompt": False, "temperature": 0}, timeout)


@contextlib.contextmanager
def server(args, log_path):
    """Own only this temporary process, with private logs and loopback binding."""
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        port = sock.getsockname()[1]
    url = f"http://127.0.0.1:{port}"
    command = [args.server_bin, "--model", args.model, "--host", "127.0.0.1",
               "--port", str(port), "--threads", str(args.threads),
               "--threads-batch", str(args.threads), "--n-gpu-layers", "0",
               "--parallel", "1", "--ctx-size", str(args.ctx_size),
               "--device", "none", "--no-op-offload", "--no-warmup",
               "--no-context-shift", "--cache-ram", "0"]
    started = time.perf_counter()
    log_path = private_path(log_path)
    with os.fdopen(os.open(log_path, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600), "wb") as log:
        os.fchmod(log.fileno(), 0o600)
        process = subprocess.Popen(command, stdout=log, stderr=subprocess.STDOUT)
        try:
            deadline = started + args.startup_timeout
            while time.perf_counter() < deadline:
                if process.poll() is not None:
                    raise RuntimeError("server_exited_before_ready")
                try:
                    with http_client().open(url+"/health", timeout=.5) as response:
                        if response.status == 200:
                            break
                except (OSError, urllib.error.URLError):
                    pass
                time.sleep(.025)
            else:
                raise TimeoutError("server_startup_timeout")
            yield url, (time.perf_counter()-started)*1000
        finally:
            if process.poll() is None:
                process.terminate()
                try:
                    process.wait(timeout=10)
                except subprocess.TimeoutExpired:
                    process.kill()
                    process.wait(timeout=10)


def summarize(rows):
    summary = {}
    for mode in MODES:
        selected = [r for r in rows if r["mode"] == mode]
        if not selected:
            continue
        valid = [r for r in selected if r.get("valid_clause") and not r.get("error")]
        under = sum(r.get("from_early_final_clause_ms") is not None and
                    r["from_early_final_clause_ms"] < 300 for r in valid)
        end_to_end_under = sum(r.get("from_launch_clause_ms") is not None and
                              r["from_launch_clause_ms"] < 300 for r in valid)
        summary[mode] = {
            "attempts": len(selected), "errors": sum(r.get("error", 0) for r in selected),
            "valid_clauses": len(valid), "valid_under_300ms": under,
            "valid_under_300ms_all_attempts": wilson(under, len(selected)),
            "cold_launch_under_300ms": end_to_end_under if mode == "cold_process" else None,
            **{name: sum(r.get(name, 0) for r in selected) for name in
               ("empty", "short", "long", "incomplete", "clipped", "special_output", "truncated_context")},
            **{name: distribution([r.get(name) for r in selected]) for name in
               ("ttft_ms", "first_text_ms", "request_ms", "server_ready_ms", "from_launch_ttft_ms",
                "from_launch_clause_ms", "from_early_final_ttft_ms", "from_early_final_clause_ms",
                "prompt_build_ms", "prefix_prime_ms", "complete_clause_ms", "prompt_chars",
                "prompt_evaluated_tokens", "cached_tokens", "predicted_tokens")},
            "clause_ms": distribution([r.get("clause_ms") for r in valid]),
        }
    return summary


def attempt(args, pair, mode, repeat, url, ready_ms=None):
    row = {"id": pair["id"], "call_hash": pair.get("call_hash"), "split": pair.get("split"),
           "mode": mode, "repeat": repeat, "server_ready_ms": ready_ms}
    try:
        if mode == "warm_prefix":
            # Prefix inference represents work performed before early final.
            # Send only known context; the new caller/opener are not prefilled.
            seed_prompt = prompt_for_pair(pair)
            prime_started = time.perf_counter()
            prime_prefix(url, seed_prompt, args.timeout)
            row["prefix_prime_ms"] = (time.perf_counter()-prime_started)*1000
        build_started = time.perf_counter()
        prompt = prompt_for_pair(pair)
        row["prompt_build_ms"] = (time.perf_counter()-build_started)*1000
        row["prompt_chars"] = len(prompt)
        request_started = time.perf_counter()
        result = run_completion(url, prompt, cache_prompt=mode == "warm_prefix",
                                max_tokens=args.max_tokens, timeout=args.timeout,
                                max_chars=args.max_chars)
        row.update(result)
        for target, source in (("from_early_final_ttft_ms", "ttft_ms"),
                               ("from_early_final_clause_ms", "clause_ms")):
            row[target] = (request_started-build_started)*1000 + row[source] if row[source] is not None else None
        if mode == "cold_process":
            row["from_launch_ttft_ms"] = ready_ms + row["from_early_final_ttft_ms"] if row["ttft_ms"] is not None else None
            row["from_launch_clause_ms"] = ready_ms + row["from_early_final_clause_ms"] if row["clause_ms"] is not None else None
    except Exception as exc:
        row.update(error=1, error_type=type(exc).__name__, valid_clause=0)
    return row


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--pairs", required=True)
    parser.add_argument("--output-dir", required=True)
    parser.add_argument("--server-bin", required=True)
    parser.add_argument("--model", required=True)
    parser.add_argument("--split", default="test")
    parser.add_argument("--mode", choices=(*MODES, "all"), default="all")
    parser.add_argument("--limit", type=int, default=0)
    parser.add_argument("--repeats", type=int, default=3)
    parser.add_argument("--cold-limit", type=int, default=5)
    parser.add_argument("--threads", type=int, default=4)
    parser.add_argument("--ctx-size", type=int, default=2048)
    parser.add_argument("--max-tokens", type=int, default=48)
    parser.add_argument("--max-chars", type=int, default=FIRST_CLAUSE_LIMIT)
    parser.add_argument("--timeout", type=float, default=60)
    parser.add_argument("--startup-timeout", type=float, default=180)
    args = parser.parse_args(argv)
    if min(args.repeats, args.threads, args.ctx_size, args.max_tokens, args.max_chars) <= 0:
        parser.error("positive_counts_required")
    if min(args.limit, args.cold_limit) < 0:
        parser.error("limits_must_be_nonnegative")
    if min(args.limit, args.cold_limit) < 0 or min(args.timeout, args.startup_timeout) <= 0:
        parser.error("invalid_limits_or_timeouts")
    out = private_dir(args.output_dir)
    pairs = [p for p in read_jsonl(args.pairs) if p.get("split") == args.split]
    if args.limit:
        pairs = pairs[:args.limit]
    if not pairs:
        raise ValueError("no_selected_pairs")
    if len({p["id"] for p in pairs}) != len(pairs):
        raise ValueError("duplicate_pair_ids")
    modes = MODES if args.mode == "all" else (args.mode,)
    rows = []
    for mode in modes:
        chosen = pairs[:args.cold_limit] if mode == "cold_process" and args.cold_limit else pairs
        if mode == "cold_process":
            for repeat in range(args.repeats):
                for index, pair in enumerate(chosen):
                    try:
                        with server(args, out/f"server-cold-{repeat}-{index}.log") as (url, ready):
                            row = attempt(args, pair, mode, repeat, url, ready)
                    except Exception as exc:
                        row = {"id": pair["id"], "mode": mode, "repeat": repeat,
                               "error": 1, "error_type": type(exc).__name__, "valid_clause": 0}
                    rows.append(row)
                    write_private(out/"predictions.jsonl", rows, jsonl=True)
        else:
            try:
                with server(args, out/f"server-{mode}.log") as (url, ready):
                    # Exclude model warm-up from both warm measurements.
                    request_json(url+"/completion", {"prompt": "warmup", "n_predict": 1,
                        "cache_prompt": False, "id_slot": 0}, args.timeout)
                    for repeat in range(args.repeats):
                        for pair in chosen:
                            rows.append(attempt(args, pair, mode, repeat, url))
                            write_private(out/"predictions.jsonl", rows, jsonl=True)
            except Exception as exc:
                completed = {(r["id"], r["repeat"]) for r in rows if r["mode"] == mode}
                for repeat in range(args.repeats):
                    for pair in chosen:
                        if (pair["id"], repeat) not in completed:
                            rows.append({"id": pair["id"], "mode": mode, "repeat": repeat,
                                "error": 1, "error_type": type(exc).__name__, "valid_clause": 0})
    write_private(out/"predictions.jsonl", rows, jsonl=True)
    summary = {"selected_pairs": len(pairs), "threads": args.threads, "gpu_layers": 0,
               "cold_os_cache_flushed": 0, "cold_server_warmup": 0,
               "warm_prefix_is_ideal_prefill_not_verified_sequential_cache": 1,
               "warm_prefix_excludes_current_caller": 1,
               "includes_prompt_serialization_ms": 1,
               "includes_asr_or_tts_ms": 0, "modes": summarize(rows)}
    write_private(out/"summary.json", summary)
    write_private(out/"config.json", vars(args))
    print(json.dumps(summary))


if __name__ == "__main__":
    try:
        main()
    except Exception as exc:
        # Never interpolate exception text: servers sometimes echo private prompts.
        print(json.dumps({"fatal_errors": 1, "error_type": type(exc).__name__}))
        raise SystemExit(1)
