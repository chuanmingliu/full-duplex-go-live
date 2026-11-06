import hashlib
import json
from pathlib import Path
import shutil
import subprocess
from types import SimpleNamespace
from uuid import uuid4
import wave

import pytest

from golive_mcp import worker


@pytest.fixture
def ops(tmp_path):
    return worker.Operations("http://journal.invalid", tmp_path)


def test_journal_summary_omits_chat_and_sensitive_fields(ops, monkeypatch):
    data = {"call": {"id": "out-test", "session": "sess-test", "instructions": "PRIVATE",
                    "transcript": [{"who": "caller", "text": "PRIVATE"}, {"who": "assistant", "text": "PRIVATE"}]},
            "archive": {"archived": True, "audio": True, "dir": "PRIVATE"}}
    monkeypatch.setattr(ops, "journal", lambda path: data)
    result = ops.get_call("out-test")
    assert result["transcript_lines"] == {"caller": 1, "assistant": 1}
    assert "PRIVATE" not in json.dumps(result)
    assert result["archive_verified"] is False
    with pytest.raises(ValueError):
        ops.get_call("../../etc/passwd")


def test_real_review_requires_done_before_archive_read(ops, monkeypatch):
    monkeypatch.setattr(ops, "get_call", lambda call: {"kind": "real"})
    monkeypatch.setattr(ops, "verify_archive", lambda call: pytest.fail("archive must not be read"))
    with pytest.raises(ValueError, match="owner's done"):
        ops.analyze("out-test")


def test_interruption_pairing_and_ack_preservation():
    def row(ms, msg, **fields):
        return {"time": f"2026-01-01T00:00:00.{ms:03d}Z", "msg": msg, **fields}
    rows = [row(0, "listen: utterance opened", barge_in=True),
            row(90, "listen: backchannel, not an interruption; keeping the floor"),
            row(200, "listen: utterance opened", barge_in=True),
            row(340, "new query while answering: cutting the answer", barge_in=True, speaking=True),
            row(350, "new query while answering: cutting the answer", barge_in=False, speaking=True),
            row(400, "new query while answering: cutting the answer", barge_in=True, speaking=True)]
    result = worker.interruption_metrics(rows)
    assert result["samples_ms"] == [140]
    assert result["kept_acknowledgments"] == 1
    assert result["unpaired"] == 1


def setup_real(ops, monkeypatch, outcome):
    (ops.home / "aria").mkdir()
    (ops.home / "aria/.env").write_text("ALLOWED_CALLEE=+15555550100\nPRIVATE_TOKEN=secret\n")
    monkeypatch.setattr(ops, "preflight", lambda expected: {"build": "abcdef0"})
    calls = []
    def run(args, **kwargs):
        calls.append(args)
        if isinstance(outcome, Exception):
            raise outcome
        return outcome
    monkeypatch.setattr(worker.subprocess, "run", run)
    return calls


def test_call_receipt_exact_route_and_replay_protection(ops, monkeypatch):
    calls = setup_real(ops, monkeypatch, SimpleNamespace(returncode=0,
                         stdout='PRIVATE\noriginator answered 200: {"call_id":"out-test"}'))
    request = str(uuid4())
    result = ops.call(request, "active_call", "a" * 64)
    assert result["stage"] == "dial_accepted" and result["call_id"] == "out-test"
    assert calls[0][-1] == "call" and len(calls) == 1
    assert "PRIVATE" not in json.dumps(result)
    with pytest.raises(ValueError, match="already exists"):
        ops.call(request, "active_call", "a" * 64)
    assert len(calls) == 1


def test_timeout_does_not_redial(ops, monkeypatch):
    calls = setup_real(ops, monkeypatch, subprocess.TimeoutExpired("PRIVATE", 60))
    request = str(uuid4())
    result = ops.call(request, "active_call", "a" * 64)
    assert result["stage"] == "outcome_unknown" and len(calls) == 1
    assert "PRIVATE" not in json.dumps(result)
    assert json.loads((ops.root / f"codex-latency/mcp/{request}.json").read_text())["stage"] == "outcome_unknown"


@pytest.mark.parametrize("mode,scenario", [("gpt-live-1", None), ("active_call", "test"), ("sim", "../evil")])
def test_invalid_call_route_never_runs_helper(ops, monkeypatch, mode, scenario):
    calls = setup_real(ops, monkeypatch, SimpleNamespace(returncode=0, stdout=""))
    with pytest.raises(ValueError):
        ops.call(str(uuid4()), mode, "a" * 64, scenario)
    assert calls == []


def make_archive(ops, monkeypatch):
    directory = ops.home / "golive-archive/call"
    directory.mkdir(parents=True)
    call = {"id": "out-test", "session": "sess-test", "build": "abcdef0", "ended_at": "2026-01-01T00:00:01Z",
            "transcript": [{"who": "caller", "text": "PRIVATE"}, {"who": "assistant", "text": "PRIVATE"}]}
    (directory / "call.json").write_text(json.dumps(call))
    (directory / "session.json").write_text(json.dumps({"id": "sess-test", "closed_at": call["ended_at"]}))
    (directory / "transcript.txt").write_text("PRIVATE\n")
    (directory / "golive.log").write_text('session=sess-test msg="fixture"\n')
    with wave.open(str(directory / "audio.wav"), "wb") as wav:
        wav.setnchannels(2)
        wav.setsampwidth(2)
        wav.setframerate(16000)
        wav.writeframes(b"\x01\x00\x02\x00" * 100)
    manifest = {"id": "out-test", "session": "sess-test", "audio": True, "files": {}}
    for file in directory.iterdir():
        manifest["files"][file.name] = {"bytes": file.stat().st_size, "sha256": worker.sha(file)}
    (directory / "manifest.json").write_text(json.dumps(manifest))
    data = {"call": call, "archive": {"archived": True, "audio": True, "dir": str(directory)}}
    monkeypatch.setattr(ops, "journal", lambda path: data)
    return directory, data


def test_archive_requires_five_hashes_and_both_channels(ops, monkeypatch):
    directory, _ = make_archive(ops, monkeypatch)
    _, _, proof = ops.verify_archive("out-test")
    assert proof["files_verified"] == 5 and proof["channels_with_signal"] == 2
    (directory / "transcript.txt").write_text("corrupted")
    with pytest.raises(ValueError, match="verification"):
        ops.verify_archive("out-test")


def test_missing_audio_and_path_escape_rejected(ops, monkeypatch, tmp_path):
    directory, data = make_archive(ops, monkeypatch)
    data["archive"]["audio"] = False
    with pytest.raises(ValueError, match="pending"):
        ops.verify_archive("out-test")
    data["archive"].update(audio=True, dir=str(tmp_path))
    with pytest.raises(ValueError, match="Unexpected archive"):
        ops.verify_archive("out-test")


def test_analysis_uses_real_analyzer_and_reports_missing_samples(ops, monkeypatch):
    directory, _ = make_archive(ops, monkeypatch)
    source = Path(__file__).resolve().parents[3] / "scripts/call_latency.py"
    if not source.exists():
        pytest.skip("Source repository required for canonical analyzer integration")
    (ops.root / "scripts").mkdir(parents=True)
    shutil.copyfile(source, ops.root / "scripts/call_latency.py")
    result = ops.analyze("out-test", owner_done=True)
    assert result["normal_first_content"]["n"] == 0
    assert result["gate"]["passed"] is False and result["normal_gate"]["passed"] is False
    assert result["acoustic_handset_verified"] is False
    assert "PRIVATE" not in json.dumps(result)


def test_baseline_busy_and_route_guards(ops, monkeypatch):
    state = {"backend": "local", "endpoint": "ws://127.0.0.1:8765/v1/realtime", "preset_openers": True}
    health = {"status": "ok", "sessions": 1}
    monkeypatch.setattr(ops, "snapshot", lambda: (state, "a" * 64, health, {"instructions": "PRIVATE"}))
    with pytest.raises(ValueError, match="changed"):
        ops.preflight("b" * 64)
    with pytest.raises(ValueError, match="busy"):
        ops.preflight("a" * 64)
    health["sessions"] = 0
    state["endpoint"] = "ws://127.0.0.1:8095/v1/realtime"
    with pytest.raises(ValueError, match="original GoLive"):
        ops.preflight("a" * 64)


def test_utterance_before_playback_still_pairs_to_explicit_barge_cut():
    result = worker.interruption_metrics([
        {"time": "2026-01-01T00:00:00Z", "msg": "listen: utterance opened", "barge_in": False},
        {"time": "2026-01-01T00:00:00.200Z", "msg": "new query while answering: cutting the answer", "barge_in": True, "speaking": True}])
    assert result["samples_ms"] == [200]
    assert result["opened_during_playback"]["n"] == 0


def test_simulation_reserves_then_only_dials_simulator(ops, monkeypatch):
    (ops.sim / "scenarios/custom").mkdir(parents=True)
    (ops.sim / "scenarios/custom/test-case.json").write_text('{"persona":"PRIVATE"}')
    (ops.sim / "tokens.env").write_text("SIM_CONTROL_TOKEN=PRIVATE\n")
    monkeypatch.setattr(ops, "preflight", lambda expected: {"build": "abcdef0"})
    def get(url, token=None):
        assert token == "PRIVATE"
        if url.endswith("/runs"):
            return {"runs": []}
        return {"phase": "prepared", "ready": True}
    monkeypatch.setattr(worker, "get", get)
    calls = []
    def run(args, **kwargs):
        calls.append(args)
        return SimpleNamespace(returncode=0, stdout='run_id: sim-test' if "prepare" in args
                               else 'originator answered 200: {"call_id":"out-sim"}')
    monkeypatch.setattr(worker.subprocess, "run", run)
    result = ops.call(str(uuid4()), "sim", "a" * 64, "test-case")
    assert len(calls) == 2 and calls[1][-3:] == ["call", "sim", "sim-test"]
    assert result["call_id"] == "out-sim" and result["run_id"] == "sim-test"
    assert "PRIVATE" not in json.dumps(result)


def test_scenario_discovery_marks_conflicts_without_exposing_prompts(ops):
    for folder in ("simulator", "custom"):
        path = ops.sim / "scenarios" / folder
        path.mkdir(parents=True)
        (path / "repeated.json").write_text('{"persona":"PRIVATE"}')
    result = ops.list_scenarios()
    assert result["total"] == 2 and all(row["ambiguous"] for row in result["scenarios"])
    assert "PRIVATE" not in json.dumps(result)


@pytest.mark.parametrize("live_env", [b"GOLIVE_REALTIME_STORE_CALLS=0\0GOLIVE_STORE_DIR=/tmp/calls\0GOLIVE_STORE_TTL_HOURS=8760",
                                       b"GOLIVE_REALTIME_STORE_CALLS=1\0GOLIVE_STORE_DIR=/tmp/calls\0GOLIVE_STORE_TTL_HOURS=24"])
def test_recording_is_checked_in_live_process(ops, monkeypatch, live_env):
    state = {"backend": "local", "endpoint": "ws://127.0.0.1:8765/v1/realtime", "preset_openers": True}
    monkeypatch.setattr(ops, "snapshot", lambda: (state, "a" * 64, {"status": "ok", "sessions": 0},
                                                 {"instructions": "PRIVATE", "call": "out-test"}))
    monkeypatch.setattr(ops, "journal", lambda path: {"call": {"id": "out-test", "ended_at": "2026-01-01T00:00:00Z"}})
    monkeypatch.setattr(worker, "get", lambda url: {"status": "ok"})
    monkeypatch.setattr(worker.subprocess, "check_output", lambda *args, **kwargs: 'users:(("golive",pid=123,fd=3))')
    original = Path.read_bytes
    monkeypatch.setattr(Path, "read_bytes", lambda path: live_env if str(path) == "/proc/123/environ" else original(path))
    with pytest.raises(ValueError, match="Live two-sided recording"):
        ops.preflight("a" * 64)


def test_analysis_separates_quotes_normal_and_other_sessions(ops, monkeypatch):
    import importlib.util
    directory, _ = make_archive(ops, monkeypatch)
    repo = Path(__file__).resolve().parents[3]
    if not (repo / "scripts/test_call_latency.py").exists():
        pytest.skip("Source repository required for canonical analyzer integration")
    spec = importlib.util.spec_from_file_location("fixtures", repo / "scripts/test_call_latency.py")
    fixture = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(fixture)
    (ops.root / "scripts").mkdir(parents=True)
    shutil.copyfile(repo / "scripts/call_latency.py", ops.root / "scripts/call_latency.py")
    rows = [fixture.turn(i) for i in range(100, 120)]
    _, child = fixture.quote_pair(first_content_ms=200, first_readout_ms=400)
    rows += [child, fixture.input_delivery(origin_to_result_start_ms=5000),
             fixture.turn(30, first_content_ms=100),
             {"msg": "tools: task submitted", "session": "sess_test", "turn": "item_30", "tool": "quote_total"}]
    for row in rows:
        row["session"] = "sess-test"
    rows.append(fixture.turn(999, first_content_ms=5000, session="some_other_session"))
    log = directory / "golive.log"
    log.write_text("\n".join(json.dumps(row) for row in rows))
    manifest = json.loads((directory / "manifest.json").read_text())
    manifest["files"]["golive.log"] = {"bytes": log.stat().st_size, "sha256": worker.sha(log)}
    (directory / "manifest.json").write_text(json.dumps(manifest))
    result = ops.analyze("out-test", owner_done=True, handset_estimate=True)
    assert result["normal_first_content"]["n"] == 20
    assert result["normal_first_content"]["p95_ms"] == 880
    assert result["quotation_acknowledgment"]["n"] == 1
    assert result["quotation_result"]["p95_ms"] == 5580
    assert result["quotation_readout"]["p95_ms"] == 5780
    assert result["normal_gate"]["passed"] is True
    assert result["optional_handset_estimate"] == {"assumed_extra_ms": 50, "normal_p95_ms": 930, "measured": False}
