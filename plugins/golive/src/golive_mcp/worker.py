"""Fixed box-side operations. Transferred by the Mac runner; stdlib only.

No arbitrary commands, configuration writes, service restarts, or raw log output.
"""
from __future__ import annotations

import array
import base64
from collections import Counter
import datetime
import fcntl
import hashlib
import importlib.util
import json
import math
import os
from pathlib import Path
import re
import shlex
import statistics
import subprocess
import sys
import urllib.request
import wave
from uuid import UUID

PREFIX = "GOLIVE_MCP_RESULT "
ID_PATTERN = r"[A-Za-z0-9_-]{1,80}"


class GuardError(ValueError):
    """Only deliberate, public-safe precondition messages may leave the box."""


def require(condition, message):
    if not condition:
        raise GuardError(message)


def sha(path):
    h = hashlib.sha256()
    with Path(path).open("rb") as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b""):
            h.update(chunk)
    return h.hexdigest()


def get(url, token=None):
    request = urllib.request.Request(url, headers={"Authorization": "Bearer " + token} if token else {})
    with urllib.request.urlopen(request, timeout=10) as response:
        if response.status == 204:
            return {"ready": True}
        raw = response.read(16_000_001)
        require(len(raw) <= 16_000_000, "Service response is too large")
        return json.loads(raw)


def select(data, fields):
    return {key: data[key] for key in fields if key in data}


def numeric(data):
    """Report numeric metrics only; no provider strings or customer text."""
    if isinstance(data, dict):
        return {k: numeric(v) for k, v in data.items() if isinstance(v, (dict, list, int, float)) or v is None}
    if isinstance(data, list):
        return [numeric(v) for v in data if isinstance(v, (dict, list, int, float)) or v is None]
    return data


def ended(value):
    return isinstance(value, str) and bool(value) and not value.startswith("0001-")


def identifier(value):
    require(isinstance(value, str) and re.fullmatch(ID_PATTERN, value), "Invalid identifier")
    return value


def env_value(path, key):
    for line in path.read_text().splitlines():
        if line.startswith(key + "="):
            return line.split("=", 1)[1].strip().strip("\"'")
    return ""


def parse(line):
    try:
        return json.loads(line) if line.lstrip().startswith("{") else dict(
            word.split("=", 1) for word in shlex.split(line) if "=" in word)
    except ValueError:
        return {}


def distribution(values):
    values = sorted(values)
    return {"n": len(values), "p50_ms": statistics.median(values) if values else None,
            "p95_ms": values[math.ceil(.95 * len(values)) - 1] if values else None,
            "max_ms": max(values) if values else None}


def interruption_metrics(rows):
    pending = None
    cuts, playback_opens, unpaired, other_cuts = [], [], 0, 0
    def stamp(value):
        return datetime.datetime.fromisoformat(value.replace("Z", "+00:00"))
    for row in rows:
        if row.get("msg") == "listen: utterance opened":
            pending = (stamp(row["time"]), row.get("barge_in") in (True, "true"))
        elif (row.get("msg") == "new query while answering: cutting the answer"
              and row.get("barge_in") in (True, "true") and row.get("speaking") in (True, "true")):
            if pending is None:
                unpaired += 1
            else:
                elapsed = (stamp(row["time"]) - pending[0]).total_seconds() * 1000
                if elapsed >= 0:
                    cuts.append(round(elapsed, 3))
                    if pending[1]:
                        playback_opens.append(round(elapsed, 3))
                else:
                    unpaired += 1
                pending = None
        elif row.get("msg") == "new query while answering: cutting the answer" and row.get("speaking") in (True, "true"):
            other_cuts += 1
    return {"boundary": "VAD utterance open to server playback cut", "summary": distribution(cuts),
            "samples_ms": cuts, "unpaired": unpaired,
            "opened_during_playback": distribution(playback_opens), "non_barge_cancellations": other_cuts,
            "kept_acknowledgments": sum(r.get("msg") == "listen: backchannel, not an interruption; keeping the floor" for r in rows),
            "detector_assists": sum(r.get("msg") == "backchannel: earlier ASR final requested" for r in rows),
            "detector_fallbacks": sum(r.get("msg") == "backchannel: legacy fallback" for r in rows)}


class Operations:
    def __init__(self, journal_url, home=None):
        self.home = Path(home) if home else Path.home()
        self.root = self.home / "golive-telephony"
        self.sim = self.home / "golive-sim"
        self.journal_url = journal_url.rstrip("/")
        self.saved_path = self.home / "golive-last-call.json"

    def journal(self, path):
        return get(self.journal_url + path)

    def snapshot(self):
        health = get("http://127.0.0.1:8765/healthz")
        binding = get("http://127.0.0.1:3000/api/realtime/brain")
        saved = json.loads(self.saved_path.read_text())
        profile_path = self.root / "configs/telephony.json"
        profile = json.loads(profile_path.read_text())
        state = {"build": health.get("version"), "profile_sha256": sha(profile_path),
                 "dial_helper_sha256": sha(self.root / "scripts/phone-brain.sh"),
                 "backend": binding.get("backend"), "endpoint": binding.get("endpoint"),
                 "binding_revision": binding.get("revision"),
                 "recipient_sha256": hashlib.sha256(env_value(self.home / "aria/.env", "ALLOWED_CALLEE").encode()).hexdigest(),
                 "context_sha256": {k: hashlib.sha256(saved.get(k, "").encode()).hexdigest()
                                    for k in ("instructions", "voice", "greeting")},
                 "preset_openers": not bool(profile.get("duplex", {}).get("talker_opener_url"))}
        state_hash = hashlib.sha256(json.dumps(state, sort_keys=True).encode()).hexdigest()
        return state, state_hash, health, saved

    def status(self):
        state, fingerprint, health, _ = self.snapshot()
        overview = self.journal("/api/overview")
        try:
            detector = get("http://127.0.0.1:18087/healthz").get("status") == "ready"
        except Exception:
            detector = False
        return {"state_sha256": fingerprint, "baseline": {k: v for k, v in state.items() if k != "recipient_sha256"},
                "health": select(health, ("status", "version", "sessions")),
                "journal": select(overview, ("recording", "retention_hours", "target_ms")),
                "detector_ready": detector,
                "recording_note": "Journal flag is a store setting; call preflight verifies the live process."}

    def list_calls(self, offset=0, limit=20):
        require(type(offset) is int and 0 <= offset <= 100000 and type(limit) is int and 1 <= limit <= 50,
                "Invalid page bounds")
        rows = self.journal("/api/overview").get("calls", [])
        # The existing Journal API returns its complete index; only a bounded page leaves the box.
        return {"calls": [select(row, ("id", "build", "started_at", "duration_s", "has_audio",
                                       "archived", "has_retro", "kind", "run_id")) for row in rows[offset:offset + limit]],
                "total": len(rows), "next_offset": offset + limit if offset + limit < len(rows) else None}

    def list_scenarios(self):
        scenarios = []
        for folder in ("simulator", "custom"):
            for path in sorted((self.sim / "scenarios" / folder).glob("*.json")):
                if path.is_file() and not path.is_symlink() and re.fullmatch(ID_PATTERN, path.stem):
                    scenarios.append({"name": path.stem, "source": folder, "sha256": sha(path)})
        counts = Counter(row["name"] for row in scenarios)
        for row in scenarios:
            row["ambiguous"] = counts[row["name"]] > 1
        require(len(scenarios) <= 100, "Too many deployed scenarios; inspect simulator privately")
        return {"scenarios": scenarios, "total": len(scenarios)}

    def get_call(self, call_id):
        data = self.journal("/api/calls/" + identifier(call_id))
        call = data["call"]
        require(call["id"] == call_id, "Journal returned a different call")
        counts = Counter(t.get("who") for t in call.get("transcript", []) if t.get("text", "").strip())
        return {"call": select(call, ("id", "session", "build", "started_at", "ended_at", "duration_s")),
                "kind": "simulated" if data.get("sim_run") else "real", "run_id": data.get("sim_run"),
                "archive": select(data.get("archive", {}), ("archived", "audio")),
                "transcript_lines": {side: counts[side] for side in ("caller", "assistant")},
                "journal_url": self.journal_url + "/#/call/" + call_id,
                "audio_url": self.journal_url + "/api/calls/" + call_id + "/audio.wav?raw=1",
                "archive_verified": False}

    def preflight(self, expected):
        state, fingerprint, health, saved = self.snapshot()
        require(fingerprint == expected, "Runtime or original configuration changed; inspect status again")
        require(health.get("status") == "ok" and health.get("sessions") == 0, "Engine is busy or unhealthy")
        require(state["backend"] == "local" and state["endpoint"] == "ws://127.0.0.1:8765/v1/realtime",
                "Aria is not bound to original GoLive Active Call")
        require(state["preset_openers"] and saved.get("instructions"), "Original preset/context baseline is unavailable")
        previous = self.journal("/api/calls/" + identifier(saved.get("call")))["call"]
        require(previous.get("id") == saved["call"] and ended(previous.get("ended_at")), "Previous call has not ended")
        require(get("http://127.0.0.1:8791/healthz").get("status") == "ok", "CRM is unhealthy")
        listeners = subprocess.check_output(["ss", "-lntpH", "sport = :8765"], text=True)
        pids = set(re.findall(r"pid=(\d+)", listeners))
        require(len(pids) == 1, "Cannot identify the live GoLive process")
        env = dict(v.split(b"=", 1) for v in (Path("/proc") / pids.pop() / "environ").read_bytes().split(b"\0") if b"=" in v)
        require(env.get(b"GOLIVE_REALTIME_STORE_CALLS") == b"1" and env.get(b"GOLIVE_STORE_DIR")
                and env.get(b"GOLIVE_STORE_TTL_HOURS") == b"8760", "Live two-sided recording with 365-day retention is required")
        overview = self.journal("/api/overview")
        require(overview.get("recording") is True and overview.get("retention_hours") == 8760,
                "Journal recording/retention differs")
        return state

    def call(self, request_id, mode, expected_state, scenario=None):
        request_id = str(UUID(request_id))
        require(mode in {"active_call", "sim"}, "Unsupported call route")
        require(re.fullmatch(r"[a-f0-9]{64}", expected_state), "Invalid baseline fingerprint")
        attempts = self.root / "codex-latency/mcp"
        attempts.mkdir(parents=True, mode=0o700, exist_ok=True)
        receipt = attempts / (request_id + ".json")
        with (attempts / "call.lock").open("a") as lock:
            fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
            require(not receipt.exists(), "Call request already exists on box; inspect it, never redial")
            baseline = self.preflight(expected_state)
            args = ["bash", str(self.root / "scripts/phone-brain.sh"), "call"]
            if mode == "active_call":
                require(scenario is None, "A real call cannot have a simulator scenario")
                callee = env_value(self.home / "aria/.env", "ALLOWED_CALLEE")
                require(bool(re.fullmatch(r"\+?[0-9]{5,20}", callee)), "Configured real recipient is unavailable")
            else:
                identifier(scenario)
                paths = [self.sim / "scenarios" / folder / (scenario + ".json") for folder in ("simulator", "custom")]
                paths = [p for p in paths if p.is_file() and not p.is_symlink()]
                require(len(paths) == 1, "Simulator scenario is missing or ambiguous")
                token = env_value(self.sim / "tokens.env", "SIM_CONTROL_TOKEN")
                require(bool(token), "Simulator token unavailable")
                get("http://127.0.0.1:18865/readyz", token)
                runs = get("http://127.0.0.1:18865/sim/v1/runs", token)
                runs = runs.get("runs", []) if isinstance(runs, dict) else runs
                require(isinstance(runs, list) and all(r.get("phase") in {"completed", "rejected", "expired", "failed", "cancelled", "aborted"} for r in runs), "Simulator already has a reservation")
            state = {"request_id": request_id, "stage": "requested", "kind": mode, "build": baseline["build"]}
            with receipt.open("x") as f:
                json.dump(state, f)
                f.flush()
                os.fsync(f.fileno())
            def save():
                temp = receipt.with_suffix(".tmp")
                temp.write_text(json.dumps(state))
                os.replace(temp, receipt)
            try:
                if mode == "sim":
                    state["stage"] = "reservation_requested"
                    save()
                    run = subprocess.run(["bash", str(self.sim / "scripts/sim-brain.sh"), "prepare", str(paths[0]), request_id],
                                         capture_output=True, text=True, timeout=35)
                    match = re.search(r"^run_id:\s+(sim-[A-Za-z0-9_-]+)", run.stdout, re.M)
                    require(run.returncode == 0 and match, "Reservation outcome unknown; inspect simulator")
                    state["run_id"] = match.group(1)
                    save()
                    require(get("http://127.0.0.1:18865/sim/v1/runs/" + state["run_id"], token).get("phase") in {"prepared", "waiting"}, "Reservation is not ready")
                    args += ["sim", state["run_id"]]
                self.preflight(expected_state)
                state["stage"] = "dial_requested"
                save()
                dial = subprocess.run(args, cwd=self.root, capture_output=True, text=True, timeout=60,
                                      env=dict(os.environ, LAST_CALL=str(self.saved_path), SIM_DIR=str(self.sim),
                                               ARIA_ENV=str(self.home / "aria/.env"), ARIA_URL="http://127.0.0.1:3000"))
                match = re.search(r'"call_id"\s*:\s*"([A-Za-z0-9_-]{1,80})"', dial.stdout)
                require(dial.returncode == 0 and match, "Dial outcome unknown; do not redial")
                state.update(stage="dial_accepted", call_id=match.group(1))
            except Exception:
                state.update(stage="outcome_unknown", instruction="Inspect this request and Aria; never automatically redial.")
            save()
            return state

    def verify_archive(self, call_id):
        data = self.journal("/api/calls/" + identifier(call_id))
        call, archive = data["call"], data.get("archive", {})
        require(call["id"] == call_id and ended(call.get("ended_at")), "Call is not ended")
        require(archive.get("archived") and archive.get("audio"), "Permanent audio archive is pending")
        directory = Path(archive["dir"]).resolve(strict=True)
        require(directory.is_relative_to((self.home / "golive-archive").resolve()), "Unexpected archive directory")
        manifest = json.loads((directory / "manifest.json").read_text())
        require(manifest["id"] == call_id and manifest["session"] == call["session"] and manifest.get("audio"), "Archive identity mismatch")
        for name in ("audio.wav", "transcript.txt", "call.json", "session.json", "golive.log"):
            path, entry = directory / name, manifest["files"][name]
            require(not path.is_symlink() and path.is_file() and path.stat().st_size == entry["bytes"] > 0
                    and sha(path) == entry["sha256"], "Archive file verification failed")
        saved = json.loads((directory / "call.json").read_text())
        session = json.loads((directory / "session.json").read_text())
        require(saved["id"] == call_id and saved["session"] == call["session"] == session["id"]
                and ended(session.get("closed_at")), "Saved session mismatch or still open")
        counts = Counter(t.get("who") for t in saved.get("transcript", []) if t.get("text", "").strip())
        require(counts["caller"] and counts["assistant"], "Archive transcript lacks one side")
        signals = [False, False]
        with wave.open(str(directory / "audio.wav"), "rb") as wav:
            require(wav.getnchannels() == 2 and wav.getsampwidth() == 2 and wav.getnframes() > 0, "Expected stereo PCM16 archive")
            seconds = wav.getnframes() / wav.getframerate()
            while chunk := wav.readframes(16000):
                samples = array.array("h", chunk)
                for side in (0, 1):
                    signals[side] |= any(samples[side::2])
        require(all(signals), "An audio channel is entirely silent")
        return data, directory, {"files_verified": 5, "channels_with_signal": 2, "seconds": seconds,
                                "caller_lines": counts["caller"], "assistant_lines": counts["assistant"]}

    def analyze(self, call_id, owner_done=False, handset_estimate=False):
        # Gate before reading private recordings or running analysis.
        meta = self.get_call(call_id)
        require(meta["kind"] == "simulated" or owner_done is True, "Wait for the owner's done before real-call analysis")
        data, directory, verification = self.verify_archive(call_id)
        analyzer = self.root / "scripts/call_latency.py"
        spec = importlib.util.spec_from_file_location("golive_latency", analyzer)
        module = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(module)
        lines = [line for line in (directory / "golive.log").read_text().splitlines(True)
                 if parse(line).get("session") == data["call"]["session"]]
        require(bool(lines), "No logs bound to this session")
        rows = [parse(line) for line in lines]
        report, measured = module.analyse(lines, 380, 1200)
        reasons = module.gate_reasons(report, 20, "both", "separate")
        origins = {r["origin_turn"] for r in report["tool_result_rows"] if r["quotation"]}
        origins.update(r["turn"] for r in rows if r.get("msg") == "tools: task submitted" and r.get("tool") == "quote_total" and r.get("turn"))
        normal, acknowledgments = [], []
        for row, audio, content, pause in measured:
            if content is None or row.get("_quote_result_joined") is True:
                continue
            (acknowledgments if row.get("turn") in origins else normal).append(content + pause)
        result = {"call_id": call_id, "build": data["call"].get("build"), "archive": verification,
                  "boundary": "server output", "acoustic_handset_verified": False,
                  "analyzer_sha256": sha(analyzer), "normal_first_content": distribution(normal),
                  "quotation_acknowledgment": distribution(acknowledgments),
                  "quotation_result": distribution([r["total_ms"] for r in report["tool_result_rows"] if r["quotation"]]),
                  "quotation_readout": numeric(report.get("quote_readout", {})),
                  "first_audio_including_openers": numeric(report.get("first_audio", {})),
                  "counts": numeric(report.get("counts", {})),
                  "canonical_conversational_content": numeric(report.get("conversational_first_content", {})),
                  "gate": {"passed": not reasons, "failure_count": len(reasons), "target_ms": 1200,
                           "minimum_caller_turns": 20, "quote_sla": "separate", "metric": "both"},
                  "normal_gate": {"passed": not reasons and len(normal) >= 20
                                  and distribution(normal)["p95_ms"] < 1200,
                                  "minimum_normal_turns": 20, "target_ms": 1200},
                  "interruptions": interruption_metrics(rows)}
        if handset_estimate and normal:
            result["optional_handset_estimate"] = {"assumed_extra_ms": 50,
                "normal_p95_ms": result["normal_first_content"]["p95_ms"] + 50, "measured": False}
        return result


def execute(envelope):
    operation = envelope["operation"]
    args = envelope["arguments"]
    ops = Operations(envelope["journal_url"])
    require(operation in {"status", "list_calls", "list_scenarios", "get_call", "call", "analyze"}, "Unknown operation")
    if operation == "call":
        return ops.call(envelope["request_id"], **args)
    return getattr(ops, operation)(**args)


def main():
    os.umask(0o077)
    envelope = json.loads(base64.b64decode(sys.argv[1]))
    result = {"request_id": str(UUID(envelope["request_id"]))}
    try:
        result.update(ok=True, data=execute(envelope))
    except GuardError as exc:
        result.update(ok=False, error=str(exc))
    except Exception as exc:
        result.update(ok=False, error="Operation failed; inspect the box privately", error_type=type(exc).__name__)
    print(PREFIX + json.dumps(result))


if __name__ == "__main__":
    main()
