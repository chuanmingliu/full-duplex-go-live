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
import argparse, datetime, json, os, re, statistics, subprocess, sys, urllib.request

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
sid = ""
lines = []
with open(a.log, encoding="utf-8", errors="replace") as f:
    for line in f:
        if not sid:
            if a.call in line:
                m = re.search(r"session=(sess_[0-9a-f]+)", line)
                if m:
                    sid = m.group(1)
        if sid and sid in line:
            lines.append(line)
if not sid:
    sys.exit("no session for %s in %s" % (a.call, a.log))

def kv(line, key, default=None):
    m = re.search(r"\b%s=(-?[0-9.]+)" % re.escape(key), line)
    return float(m.group(1)) if m else default

def count(pat):
    return sum(1 for l in lines if pat in l)

turns = []
for l in lines:
    if 'msg="turn: response latency"' in l:
        t = {k: kv(l, k) for k in ("first_audio_out_ms", "end_silence_ms", "turn_start_ms", "llm_first_token_ms", "first_segment_ms", "tts_first_audio_ms")}
        t["turn"] = re.search(r"turn=([a-z_0-9]+)", l).group(1)
        t["speculative"] = "speculative=true" in l
        t["at"] = re.search(r"T(\d\d:\d\d:\d\d)", l).group(1)
        turns.append(t)
measured = [t for t in turns if t["first_audio_out_ms"] and t["first_audio_out_ms"] > 0 and t["end_silence_ms"]]
llm = sorted(kv(l, "ms") for l in lines if 'llm hedge: a copy answered first' in l)
tts = sorted(kv(l, "ms") for l in lines if 'speak: first audio for segment' in l)
cut = count("new query while answering: cutting the answer")
truncated = count('msg="speak: turn truncated"')
summary = next((l for l in lines if "session: response latency summary" in l), "")
fill = next((l for l in lines if "line went quiet and was filled" in l), "")
started = next((l for l in lines if 'msg="session started"' in l), "")
ended = next((l for l in reversed(lines) if "response latency summary" in l), "")
build = a.build
if not build:
    m = re.search(r'version=([0-9a-f]{7,})', started)
    build = m.group(1) if m else "?"
abandoned = kv(summary, "speculation_abandoned", 0)
held = kv(summary, "speculation_held", 0)

def pct(xs, p):
    if not xs:
        return None
    xs = sorted(xs)
    return xs[min(len(xs) - 1, int(round(p * (len(xs) - 1))))]

def fmt(x):
    return "–" if x is None else str(int(x))

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
            report = {"_error": str(err)[:80]}

# --- the write-up --------------------------------------------------------
when = re.search(r"time=(\S+)", started)
when = when.group(1)[:16].replace("T", " ") if when else "?"
out = []
out.append("# Simulated call %s — %s, build %s" % (a.call, when, build))
out.append("")
out.append("The simulated customer (run `%s`, scenario `%s`) answered; %s." % (
    run or "?", report.get("scenario_id", "?"),
    ("it ended with: " + report["termination_reason"]) if report.get("termination_reason") else "how it ended is not in a report yet"))
out.append("")
out.append("## What golive measured")
out.append("")
pause = measured[0]["end_silence_ms"] if measured else 380
out.append("The target's definition, **caller's last sound → first audio**, is the headline; the stages are from the turn closing, %s ms after the last sound (the console's definition, 'first audio' column)." % fmt(pause))
out.append("")
out.append("| turn | at | begun before close | chat model first token | first clause | synth first audio | first audio | **last sound → first audio** |")
out.append("| --- | --- | ---: | ---: | ---: | ---: | ---: | ---: |")
for t in measured:
    out.append("| %s | %s | %s | %s | %s | %s | %s | **%s** |" % (t["turn"], t["at"], fmt(-t["turn_start_ms"] if t["turn_start_ms"] else 0), fmt(t["llm_first_token_ms"]), fmt(t["first_segment_ms"]), fmt(t["tts_first_audio_ms"]), fmt(t["first_audio_out_ms"]), fmt(t["first_audio_out_ms"] + t["end_silence_ms"])))
fa = [t["first_audio_out_ms"] + t["end_silence_ms"] for t in measured]
over = sum(1 for x in fa if x > 1200)
out.append("")
out.append("- %d turns measured, last sound → first audio: p50 **%s**, p95 %s, max %s ms; **%d over 1200**. (Console's definition: p50 %s, max %s.) Fewer than 40 turns: the p95 is the worst turn or the one before it." % (
    len(fa), fmt(pct(fa, .5)), fmt(pct(fa, .95)), fmt(max(fa) if fa else None), over,
    fmt(pct([x - pause for x in fa], .5)), fmt(max(fa) - pause if fa else None)))
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
    checks = ", ".join("%s %s" % (c.get("name"), c.get("result")) for c in report.get("checks", []))
    gaps = sorted(g["value_ms"] for g in report.get("assistant_response_gaps", []) if g.get("valid"))
    out.append("- source `%s`, provider `%s`; assistant utterances %s, customer utterances %s, overlap %s ms." % (
        report.get("source_commit"), report.get("provider_model"), report.get("assistant_sample_count"), report.get("simulator_sample_count"), report.get("overlap_duration_ms")))
    out.append("- Checks: %s." % checks)
    out.append("- Its measure of the assistant's response gaps (application audio boundary, not a handset): %d valid%s." % (
        len(gaps), (", p50 %s, max %s ms" % (fmt(pct(gaps, .5)), fmt(max(gaps)))) if gaps else ""))
    if report.get("failures"):
        out.append("- Failures: %s." % "; ".join(report["failures"]))
    if report.get("action_outcomes"):
        out.append("- Scripted actions: %s." % json.dumps(report["action_outcomes"], ensure_ascii=False)[:300])
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
