#!/usr/bin/env python3
"""The retrospective of a simulated call, in numbers: what golive measured
stage by stage, what the simulated customer's report says, and the shape of
the conversation. Never a word of what was said.

    scripts/sim-retro.py <call id> [--log golive-telephony.log] [--sim-dir ~/golive-sim]
                         [--md journal/retros/<call id>.md]

The call id is the originator's (out-…); the run id comes from the dials
file `phone-brain.sh call sim` writes. The report is read from the simulator
with its control token, on the box. Prints Markdown; --md writes it too.
"""
import argparse, importlib.util, json, os, re, shlex, sys, urllib.request
from collections import Counter
from pathlib import Path

# sim-brain invokes this command with python -I, which excludes the script's
# directory from sys.path. Load only the adjacent, bundled numeric reporter.
sys.dont_write_bytecode = True
spec = importlib.util.spec_from_file_location("call_latency", Path(__file__).with_name("call_latency.py"))
latency = importlib.util.module_from_spec(spec)
spec.loader.exec_module(latency)

ap = argparse.ArgumentParser()
ap.add_argument("call")
ap.add_argument("--log", default="golive-telephony.log")
ap.add_argument("--sim-dir", default=os.path.expanduser("~/golive-sim"))
ap.add_argument("--sim-url", default="http://127.0.0.1:18865")
ap.add_argument("--md", default="")
ap.add_argument("--build", default="")
a = ap.parse_args()

# --- the pairing ---------------------------------------------------------
run = ""
try:
    for line in open(os.path.join(a.sim_dir, "data", "dials.jsonl"), encoding="utf-8"):
        d = json.loads(line)
        if d.get("call") == a.call:
            run = d.get("run", "")
except OSError:
    pass

# --- golive's log --------------------------------------------------------
def fields(line):
    try:
        if line.lstrip().startswith("{"):
            value = json.loads(line)
            return value if isinstance(value, dict) else {}
        return dict(word.split("=", 1) for word in shlex.split(line) if "=" in word)
    except (ValueError, TypeError):
        return {}


sid = ""
with open(a.log, encoding="utf-8", errors="replace") as f:
    for line in f:
        if a.call not in line:
            continue
        row = fields(line)
        if row.get("call") == a.call and row.get("session"):
            sid = row["session"]
            break
if not sid:
    sys.exit("no session for %s in %s" % (a.call, a.log))
# Read the complete session, including its start before the call was paired.
with open(a.log, encoding="utf-8", errors="replace") as f:
    lines = [line for line in f if sid in line and fields(line).get("session") == sid]

def kv(line, key, default=None):
    value = fields(line).get(key)
    try:
        return float(value) if not isinstance(value, bool) else default
    except (ValueError, TypeError):
        return default

def count(pat):
    return sum(1 for l in lines if pat in l)

measurements, measured = latency.analyse(lines, pause=380, target=1200)
llm = sorted(value for l in lines if 'llm hedge: a copy answered first' in l
             if (value := kv(l, "ms")) is not None)
tts = sorted(value for l in lines if 'speak: first audio for segment' in l
             if (value := kv(l, "ms")) is not None)
cut = count("new query while answering: cutting the answer")
truncated = sum(fields(line).get("msg") == "speak: turn truncated" for line in lines)
summary = next((l for l in lines if "session: response latency summary" in l), "")
fill = next((l for l in lines if "line went quiet and was filled" in l), "")
started = next((l for l in lines if fields(l).get("msg") == "session started"), "")
ended = next((l for l in reversed(lines) if "response latency summary" in l), "")
build = a.build
if not build:
    m = re.fullmatch(r'[0-9a-f]{7,}', str(fields(started).get("version", "")))
    build = m.group(0) if m else "?"
abandoned = kv(summary, "speculation_abandoned", 0)
held = kv(summary, "speculation_held", 0)

def pct(xs, p):
    return latency.distribution(xs, 1200)[{.5: "p50_ms", .9: "p90_ms", .95: "p95_ms"}[p]]

def fmt(x):
    return "–" if x is None else f"{x:g}"


def nonnegative_int(value):
    return value if type(value) is int and value >= 0 else None


def status_counts(rows, allowed):
    counts = Counter()
    for row in rows:
        status = row.get("result") if isinstance(row, dict) else None
        counts[status if isinstance(status, str) and status in allowed else "unknown"] += 1
    return ", ".join(f"{status}={counts[status]}" for status in sorted(counts)) or "none"

# --- the simulator's report ---------------------------------------------
report = {}
if run:
    token = ""
    try:
        for line in open(os.path.join(a.sim_dir, "tokens.env"), encoding="utf-8"):
            if line.startswith("SIM_CONTROL_TOKEN="):
                token = line.split("=", 1)[1].strip().strip("\"'")
    except OSError:
        pass
    if token:
        try:
            req = urllib.request.Request(a.sim_url + "/sim/v1/runs/" + run + "/report", headers={"Authorization": "Bearer " + token})
            with urllib.request.urlopen(req, timeout=10) as resp:
                report = json.load(resp)
        except Exception as err:  # the report may not exist yet
            report = {"_error": type(err).__name__}

# --- the write-up --------------------------------------------------------
when = str(fields(started).get("time", "?"))[:16].replace("T", " ")
out = []
out.append("# Simulated call %s — %s, build %s" % (a.call, when, build))
out.append("")
out.append("The simulated customer (run `%s`, scenario `%s`) answered; %s." % (
    run or "?", report.get("scenario_id", "?"),
    ("it ended with: " + report["termination_reason"]) if report.get("termination_reason") else "how it ended is not in a report yet"))
out.append("")
out.append("## What golive measured")
out.append("")
out.append("The target's definition, **caller's last sound → first audio**, is the headline; first content is reported beside it. Each turn's own pause is added to both figures. The stage columns are from the turn closing (the console's definition).")
out.append("These measurements stop at audio leaving golive; the PBX, gateway, carrier and handset leg is not measured.")
out.append("")
out.append("| turn | at | begun before close | chat model first token | first clause | synth first audio | first audio | pause | **last sound → first audio** | **last sound → first content** |")
out.append("| --- | --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |")
for row, first_audio, first_content, pause in measured:
    turn = str(row.get("turn", ""))
    if not re.fullmatch(r"(?:item_|turn_)[A-Za-z0-9_-]{1,50}", turn):
        turn = "?"
    at = re.search(r"\d{2}:\d{2}:\d{2}", str(row.get("time", "")))
    start = latency.integer(row, "turn_start_ms")
    values = (turn, at.group() if at else "?", fmt(-start if start is not None else None),
              fmt(latency.integer(row, "llm_first_token_ms")), fmt(latency.integer(row, "first_segment_ms")),
              fmt(latency.integer(row, "tts_first_audio_ms")), fmt(first_audio), fmt(pause),
              fmt(first_audio + pause if first_audio is not None else None),
              fmt(first_content + pause if first_content is not None else None))
    out.append("| %s | %s | %s | %s | %s | %s | %s | %s | **%s** | **%s** |" % values)
out.append("")
for key, title, label in (("first_audio", "First audio", "first audio"),
                          ("first_content", "First content", "first content")):
    stats = measurements[key]
    out.append("- %s: **%d** measured turns; last sound → %s p50 **%s**, p95 **%s**, max %s ms; **%d at or above 1200**." % (
        title, stats["n"], label, fmt(stats["p50_ms"]), fmt(stats["p95_ms"]),
        fmt(stats["max_ms"]), stats["at_or_above_target"]))
reply = measurements["reply"]
out.append("- Console's definition: p50 %s, max %s ms. p50 is the ordinary median; p90/p95 use nearest rank. These are sample statistics, not a sustained p95 acceptance claim." % (fmt(reply["p50_ms"]), fmt(reply["max_ms"])))
counts = measurements["counts"]
out.append("- Records: answered %d; non-speech %d; cancelled %d; interrupted answers %d; missing %d; unverified %d; duplicates %d; malformed %d." % tuple(counts[key] for key in (
    "answered", "non_speech", "cancelled", "interrupted_answers", "missing", "unverified", "duplicates", "malformed")))
if counts["unverified"]:
    out.append("- Legacy/ambiguous records are estimates: speech origin and audio presence are unverified; a missing pause assumes 380 ms. They cannot establish acceptance.")
out.append("- Chat model, fastest of the copies, %d answers: p50 %s, p90 %s, max %s ms. Synthesizer first audio, %d segments: p50 %s, p90 %s, max %s ms." % (
    len(llm), fmt(pct(llm, .5)), fmt(pct(llm, .9)), fmt(max(llm) if llm else None), len(tts), fmt(pct(tts, .5)), fmt(pct(tts, .9)), fmt(max(tts) if tts else None)))
out.append("- Guesses: %d held, %d abandoned. Answers cut by the customer speaking: %d (%d after audio had played)." % (held, abandoned, cut, truncated))
if fill:
    out.append("- The line went quiet %s times; %s ms of silence filled (vad.line_fill_ms)." % (fmt(kv(fill, "times")), fmt(kv(fill, "filled_ms"))))
hour = int(when[11:13]) if len(when) >= 13 and when[11:13].isdigit() else -1
if 23 <= hour or hour == 0:
    out.append("- Made in the 23:00 hour, when the chat model alone misses the target on real calls too; do not read a build's worth from it.")
out.append("")
out.append("## What the customer's report says")
out.append("")
if report and not report.get("_error"):
    checks = status_counts(report.get("checks", []), {"pass", "fail", "skip", "not_run"})
    gaps = sorted(g["value_ms"] for g in report.get("assistant_response_gaps", [])
                  if isinstance(g, dict) and g.get("valid") is True
                  and g.get("evidence") == "application_audio_boundary"
                  and nonnegative_int(g.get("value_ms")) is not None)
    out.append("- source `%s`, provider `%s`; assistant utterances %s, customer utterances %s, overlap %s ms." % (
        report.get("source_commit"), report.get("provider_model"),
        fmt(nonnegative_int(report.get("assistant_sample_count"))),
        fmt(nonnegative_int(report.get("simulator_sample_count"))),
        fmt(nonnegative_int(report.get("overlap_duration_ms")))))
    out.append("- Checks: %s." % checks)
    out.append("- Its measure of the assistant's response gaps (application audio boundary, not a handset): %d valid%s." % (
        len(gaps), (", p50 %s, p95 %s, max %s ms" % (fmt(pct(gaps, .5)), fmt(pct(gaps, .95)), fmt(max(gaps)))) if gaps else ""))
    if report.get("failures"):
        failures = report["failures"]
        out.append("- Failures: %d (free-text details omitted)." % (len(failures) if isinstance(failures, list) else 0))
    if report.get("action_outcomes"):
        actions = report["action_outcomes"] if isinstance(report["action_outcomes"], list) else []
        statuses = status_counts(actions, {"requested", "started", "completed", "cancelled", "timed_out", "failed", "skipped", "not_triggered"})
        times = [action["at_ms"] for action in actions if isinstance(action, dict)
                 and nonnegative_int(action.get("at_ms")) is not None]
        timing = "; at_ms p50 %s, max %s" % (fmt(pct(times, .5)), fmt(max(times))) if times else ""
        out.append("- Scripted action observations: %d; %s%s. IDs, reasons and text omitted." % (len(actions), statuses, timing))
else:
    out.append("- No report (%s)." % (report.get("_error") if report else "no run paired with this call"))
out.append("")
out.append("## Open")
out.append("")
out.append("- (fill in: what the slow turns were laid to, what changed, what to try next)")
text = "\n".join(out) + "\n"
print(text)
if a.md:
    os.makedirs(os.path.dirname(a.md) or ".", exist_ok=True)
    with open(a.md, "w", encoding="utf-8") as f:
        f.write(text)
    print("written to", a.md, file=sys.stderr)
