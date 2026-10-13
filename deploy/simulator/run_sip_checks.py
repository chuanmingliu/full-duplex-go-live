#!/usr/bin/env python3
"""Opt-in synthetic SIP checks against the already running isolated project."""
import argparse
import json
import os
from pathlib import Path
import secrets
import subprocess
import time
import urllib.error
import urllib.request

ROOT = Path(__file__).resolve().parents[2]
BASE = "http://127.0.0.1:18865"
PYTHON = "python:3.13-alpine@sha256:540c7d91f98ff6880174c40e99067bf5941eb54d818a7a5e094d188b196a934d"


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--allow-isolated-sip", action="store_true", required=True)
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()
    token = os.environ.get("SIM_CONTROL_TOKEN", "")
    if len(token) < 32:
        raise SystemExit("SIM_CONTROL_TOKEN required")
    args.output.mkdir(parents=True, exist_ok=True, mode=0o700)

    def api(path, body=None, key=None):
        headers = {"Authorization": "Bearer " + token}
        if key:
            headers.update({"Idempotency-Key": key, "Content-Type": "application/json"})
        req = urllib.request.Request(BASE + path, data=json.dumps(body).encode() if body is not None else None, headers=headers)
        with urllib.request.urlopen(req, timeout=5) as response:
            raw = response.read(1 << 20)
            return json.loads(raw) if raw else None

    api("/readyz")
    network = json.loads(subprocess.check_output(["docker", "network", "inspect", "golive-user-simulator_simulator"]))[0]
    if network.get("Labels", {}).get("com.docker.compose.project") != "golive-user-simulator":
        raise SystemExit("refusing an unrelated Docker network")
    for case in ("cooperative", "confused", "cancel"):
        scenario = json.loads((ROOT / "scenarios/simulator" / ("cooperative.json" if case == "cancel" else case + ".json")).read_text())
        prepared = api("/sim/v1/runs", scenario, "sip-check-" + secrets.token_hex(12))
        run_id = prepared["run_id"]
        result = {"case": case, "run_id": run_id}
        try:
            command = ["docker", "run", "--rm", "--name", "golive-user-simulator-fixture-" + secrets.token_hex(6),
                       "--network", "golive-user-simulator_simulator", "--memory", "128m", "--cpus", "0.5",
                       "--cap-drop", "ALL", "--security-opt", "no-new-privileges", "--read-only",
                       "--mount", "type=bind,source=" + str(ROOT / "deploy/simulator/activecall/sip_fixture.py") + ",target=/fixture.py,readonly",
                       PYTHON, "python", "/fixture.py", "--allow-isolated-sip", "--run-id", run_id, "--duration", "8"]
            if case == "cancel":
                command += ["--cancel-after-ms", "1000"]
            fixture = subprocess.run(command, capture_output=True, text=True, timeout=35)
            # Container diagnostics may be verbose; do not include raw stderr or
            # environment in reports. The fixture's final JSON is content-free.
            result["fixture"] = json.loads(fixture.stdout.strip().splitlines()[-1])
            if fixture.returncode:
                raise RuntimeError("SIP fixture failed: " + str(result["fixture"].get("failure", "media check")))
            for _ in range(40):
                try:
                    result["report"] = api("/sim/v1/runs/" + run_id + "/report")
                    break
                except urllib.error.HTTPError as error:
                    if error.code != 409:
                        raise
                    time.sleep(0.2)
            else:
                raise RuntimeError("report deadline")
            report = result["report"]
            checks = {c["name"]: c["result"] for c in report["checks"]}
            if report["phase"] != "completed" or report["failures"] or report["invalid_observations"]:
                raise RuntimeError("unclean application report")
            if case == "cancel":
                if not result["fixture"]["cancelled"] or result["fixture"]["answered"] or result["fixture"]["rtp_received"]:
                    raise RuntimeError("CANCEL unexpectedly answered or carried audio")
            else:
                for check in ("answered_once", "greeting_once", "clean_termination"):
                    if checks[check] != "pass":
                        raise RuntimeError("failed check " + check)
                if case == "confused" and checks.get("interruption_overlap") != "pass":
                    raise RuntimeError("scripted overlap not observed")
            result["status"] = "pass"
        except Exception as error:
            result["status"] = "fail"
            result["error_type"] = type(error).__name__
            try:
                api("/sim/v1/runs/" + run_id + "/stop", {})
            except Exception:
                pass
            raise
        finally:
            path = args.output / (case + ".json")
            fd = os.open(path, os.O_CREAT | os.O_TRUNC | os.O_WRONLY, 0o600)
            with os.fdopen(fd, "w") as out:
                json.dump(result, out, indent=2)
        print(case + ": pass (isolated SIP / synthetic audio)")


if __name__ == "__main__":
    main()
