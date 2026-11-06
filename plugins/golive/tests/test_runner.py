import json
from concurrent.futures import ThreadPoolExecutor
import os
from pathlib import Path
import shlex
import subprocess
import sys
import time
from uuid import uuid4

import pytest

from golive_mcp.runner import PREFIX, Runner


@pytest.fixture
def runner(tmp_path):
    for folder in ("in", "out", "done"):
        (tmp_path / ".runner" / folder).mkdir(parents=True)
    (tmp_path / ".runner/alive").write_text(str(time.time()))
    return Runner({"runner_home": str(tmp_path), "remote_staging": "/tmp/box-staging",
                   "journal_url": "http://127.0.0.1:8790", "allow_real_calls": True, "allow_sim_calls": True})


def test_duplicate_call_only_polls_and_changed_uuid_payload_rejected(runner):
    request = str(uuid4())
    args = {"mode": "active_call", "expected_state": "a" * 64, "scenario": None}
    first = runner.submit("call", args, request)
    assert first["state"] == "queued"
    assert runner.submit("call", args, request)["state"] == "queued"
    assert len(list((runner.queue / "in").glob("*.job"))) == 1
    with pytest.raises(ValueError, match="different arguments"):
        runner.submit("call", {**args, "mode": "sim"}, request)


def test_mode_configuration_and_unsupported_routes(runner):
    runner.config["allow_real_calls"] = False
    with pytest.raises(ValueError, match="disabled"):
        runner.submit("call", {"mode": "active_call"}, str(uuid4()))
    with pytest.raises(ValueError, match="Only Active Call"):
        runner.submit("call", {"mode": "gpt-live-1"}, str(uuid4()))
    assert not list((runner.queue / "in").glob("*.job"))


def test_stale_runner_and_busy_queue_fail_before_receipt(runner):
    (runner.queue / "alive").write_text("0")
    with pytest.raises(ValueError, match="heartbeat"):
        runner.submit("status", {})
    (runner.queue / "alive").write_text(str(time.time()))
    (runner.queue / "in/another.job").write_text("#timeout 30\ntrue\n")
    with pytest.raises(ValueError, match="busy"):
        runner.submit("status", {})


def test_receipts_exclude_runner_noise_and_unknown_never_requeues(runner):
    request = str(uuid4())
    args = {"mode": "active_call", "expected_state": "a" * 64}
    runner.submit("call", args, request)
    (runner.queue / "in" / f"golive-mcp-{request}.job").unlink()
    assert runner.submit("call", args, request)["state"] == "outcome_unknown"
    output = runner.queue / "out" / f"golive-mcp-{request}.txt"
    output.write_text("PRIVATE raw error and credentials\n[runner] exit 1\n")
    assert "PRIVATE" not in json.dumps(runner.poll(request))
    output.write_text("PRIVATE\n" + PREFIX + json.dumps({"request_id": request, "ok": True,
                      "data": {"call_id": "out-test"}}) + "\n[runner] exit 0\n")
    result = runner.poll(request)
    assert result["result"]["data"]["call_id"] == "out-test"
    assert "PRIVATE" not in json.dumps(result)
    assert not list((runner.queue / "in").glob("*.job"))


def test_staged_worker_hash_and_small_job(runner):
    request = runner.submit("status", {})["request_id"]
    job = runner.queue / "in" / f"golive-mcp-{request}.job"
    assert len(job.read_bytes()) < 8192
    assert "hashlib.sha256(b).hexdigest()" in job.read_text()
    worker = next(runner.home.glob("golive-mcp-worker-*.py"))
    assert worker.stat().st_mode & 0o777 == 0o600
    assert job.stat().st_mode & 0o777 == 0o600


@pytest.mark.parametrize("change", [{"remote_staging": "/tmp/box;touch /tmp/x"},
    {"journal_url": "http://user:secret@example.com"}, {"allow_real_calls": "true"}, {"shell": "echo"}])
def test_invalid_config_rejected(runner, change):
    with pytest.raises(ValueError):
        Runner({**runner.config, **change})


@pytest.mark.parametrize("optimized", [False, True])
def test_changed_remote_payload_never_executes(runner, tmp_path, optimized):
    request = runner.submit("status", {})["request_id"]
    job = runner.queue / "in" / f"golive-mcp-{request}.job"
    body = "\n".join(line for line in job.read_text().splitlines() if not line.startswith("#"))
    command = shlex.split(body.removeprefix("set -eu\n"))
    marker = tmp_path / "unexpected-execution"
    modified = tmp_path / "modified-worker.py"
    modified.write_text(f"from pathlib import Path; Path({str(marker)!r}).touch()")
    command[command.index("-c") + 2] = str(modified)
    command[0] = sys.executable
    if optimized:
        command.insert(1, "-O")
    result = subprocess.run(command, capture_output=True, text=True)
    assert result.returncode != 0
    assert not marker.exists(), "Corrupted payload executed with Python optimizations enabled"


@pytest.mark.parametrize("payload", ['{"request_id":', '[]', 'null', '"PRIVATE"', '{}'])
def test_malformed_completion_is_unknown_without_retry_or_private_output(runner, payload):
    request = runner.submit("status", {})["request_id"]
    output = runner.queue / "out" / f"golive-mcp-{request}.txt"
    output.write_text(PREFIX + payload + "\n[runner] exit 0\n")
    result = runner.poll(request)
    assert result["state"] == "outcome_unknown"
    assert "PRIVATE" not in json.dumps(result)


def test_parallel_same_request_publishes_once(runner):
    request = str(uuid4())
    args = {"mode": "active_call", "expected_state": "a" * 64}
    with ThreadPoolExecutor(max_workers=4) as pool:
        results = list(pool.map(lambda _: runner.submit("call", args, request), range(4)))
    assert all(r["request_id"] == request for r in results)
    assert len(list((runner.queue / "in").glob("*.job"))) == 1


def test_failed_queue_publish_retains_intent_and_never_retries(runner, monkeypatch):
    request = str(uuid4())
    args = {"mode": "active_call", "expected_state": "a" * 64}
    def interrupted_publish(*args):
        raise OSError("Simulated interruption before publish")
    monkeypatch.setattr(os, "replace", interrupted_publish)
    with pytest.raises(OSError):
        runner.submit("call", args, request)
    assert runner.submit("call", args, request)["state"] == "outcome_unknown"
    assert not list((runner.queue / "in").glob("*.job"))
