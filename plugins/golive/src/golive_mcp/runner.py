"""Bounded, asynchronous jobs for the existing Mac runner, with durable deduplication."""
from __future__ import annotations

import base64
import fcntl
import hashlib
import json
import os
from pathlib import Path
import re
import shlex
import time
from urllib.parse import urlsplit
from uuid import UUID, uuid4

PREFIX = "GOLIVE_MCP_RESULT "


def require(condition: bool, message: str) -> None:
    if not condition:
        raise ValueError(message)


def digest(raw: bytes) -> str:
    return hashlib.sha256(raw).hexdigest()


def write_private(path: Path, raw: bytes) -> None:
    with os.fdopen(os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600), "wb") as f:
        f.write(raw)
        f.flush()
        os.fsync(f.fileno())


class Runner:
    def __init__(self, config: dict):
        require(set(config) <= {"runner_home", "remote_staging", "journal_url",
                               "allow_real_calls", "allow_sim_calls"}, "Unknown configuration key")
        self.config = config
        self.home = Path(config.get("runner_home", "~/golive")).expanduser().resolve()
        self.queue = self.home / ".runner"
        self.staging = config["remote_staging"]
        require(bool(re.fullmatch(r"/[A-Za-z0-9_/-]+", self.staging)), "Invalid remote staging path")
        url = urlsplit(config["journal_url"])
        require(url.scheme in {"http", "https"} and bool(url.hostname) and
                not url.username and not url.password and not url.query and not url.fragment
                and url.path in {"", "/"}, "Journal URL must be an origin without credentials")
        for key in ("allow_real_calls", "allow_sim_calls"):
            require(type(config.get(key, False)) is bool, key + " must be boolean")

    @classmethod
    def configured(cls) -> Runner:
        path = Path(os.environ.get("GOLIVE_MCP_CONFIG", "~/.config/golive/mcp.json")).expanduser()
        try:
            return cls(json.loads(path.read_text()))
        except (OSError, KeyError, json.JSONDecodeError) as exc:
            raise ValueError("Configure GoLive using config.example.json and GOLIVE_MCP_CONFIG") from exc

    def submit(self, operation: str, arguments: dict, request_id: str | None = None) -> dict:
        require(operation in {"status", "list_calls", "list_scenarios", "get_call", "call", "analyze"}, "Unknown operation")
        if operation == "call":
            mode = arguments.get("mode")
            require(mode in {"active_call", "sim"}, "Only Active Call and simulator are supported")
            key = "allow_real_calls" if mode == "active_call" else "allow_sim_calls"
            require(self.config.get(key) is True, key + " is disabled in the local configuration")
            require(request_id is not None, "Calls require a stable request UUID")
        request_id = str(UUID(request_id)) if request_id else str(uuid4())
        require(self.queue.is_dir(), "Existing Mac runner queue is missing")
        name = "golive-mcp-" + request_id
        envelope = {"operation": operation, "arguments": arguments, "request_id": request_id,
                    "journal_url": self.config["journal_url"]}
        raw = json.dumps(envelope, sort_keys=True).encode()
        fingerprint = digest(raw)
        state_dir = self.queue / "golive-mcp"
        state_dir.mkdir(mode=0o700, exist_ok=True)
        with (state_dir / "queue.lock").open("a") as lock:
            fcntl.flock(lock, fcntl.LOCK_EX)
            receipt = state_dir / (request_id + ".json")
            if receipt.exists():
                require(json.loads(receipt.read_text())["sha256"] == fingerprint,
                        "Request UUID already belongs to different arguments")
                return self.poll(request_id)
            age = time.time() - float((self.queue / "alive").read_text())
            require(0 <= age < 60, "Runner heartbeat is stale; inspect the existing runner")
            require(not list((self.queue / "in").glob("*.job")), "Runner is busy; wait for its current job")
            require(not any((self.queue / folder / (name + suffix)).exists()
                            for folder, suffix in (("in", ".job"), ("done", ".job"),
                                                   ("out", ".part"), ("out", ".txt"))),
                    "Runner already knows this UUID; inspect its outcome, never redial")
            worker = Path(__file__).with_name("worker.py").read_bytes()
            worker_sha = digest(worker)
            filename = "golive-mcp-worker-" + worker_sha + ".py"
            local = self.home / filename
            if local.exists():
                require(not local.is_symlink() and digest(local.read_bytes()) == worker_sha,
                        "Staged worker hash mismatch")
            else:
                write_private(local, worker)
            remote = self.staging.rstrip("/") + "/" + filename
            # The runner continues after failed transfers. Verify the opened bytes before execution.
            bootstrap = (
                "import hashlib,sys; p=sys.argv[1]; b=open(p,'rb').read()\n"
                "if hashlib.sha256(b).hexdigest()!=sys.argv[2]: raise SystemExit('worker hash mismatch')\n"
                "sys.argv=[p,sys.argv[3]]; exec(compile(b,p,'exec'),{'__name__':'__main__'})"
            )
            command = shlex.join(["python3", "-B", "-c", bootstrap, remote, worker_sha,
                                  base64.b64encode(raw).decode()])
            job = f"#timeout 180\n#push {filename} {remote}\nset -eu\n{command}\n".encode()
            require(len(job) < 8192, "Operation exceeds runner size limit")
            # Record intent first. A crash in the publish window has an uncertain outcome, never a retry.
            write_private(receipt, json.dumps({"sha256": fingerprint, "operation": operation}).encode())
            temporary = self.queue / "in" / ("." + name + ".tmp")
            write_private(temporary, job)
            os.replace(temporary, self.queue / "in" / (name + ".job"))
        return {"request_id": request_id, "state": "queued", "next": "golive_get_operation"}

    def poll(self, request_id: str) -> dict:
        request_id = str(UUID(request_id))
        require((self.queue / "golive-mcp" / (request_id + ".json")).exists(),
                "Unknown plugin request UUID")
        name = "golive-mcp-" + request_id
        output = self.queue / "out" / (name + ".txt")
        if output.exists():
            require(output.stat().st_size < 2_000_000, "Unexpectedly large runner output; inspect privately")
            lines = output.read_text().splitlines()
            try:
                results = [json.loads(line[len(PREFIX):]) for line in lines if line.startswith(PREFIX)]
            except json.JSONDecodeError:
                results = []
            if (len(results) == 1 and isinstance(results[0], dict)
                    and results[0].get("request_id") == request_id and type(results[0].get("ok")) is bool):
                return {"request_id": request_id, "state": "completed", "result": results[0]}
            state = "outcome_unknown"
        elif (self.queue / "out" / (name + ".part")).exists():
            state = "running"
        elif (self.queue / "in" / (name + ".job")).exists():
            state = "queued"
        else:
            state = "outcome_unknown"
        return {"request_id": request_id, "state": state,
                "instruction": "Poll the same request; an unknown outcome never authorizes a redial."}
