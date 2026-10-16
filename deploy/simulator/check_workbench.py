#!/usr/bin/env python3
"""Exercise a newly built simulator against a temporary loopback gateway stub.

No existing service is contacted. No invite, media, call control or judge request
is issued. This checks binary wiring and control evidence, not conversation.
"""

import argparse
import json
import os
from pathlib import Path
import secrets
import socket
import subprocess
import tempfile
import threading
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.error import HTTPError, URLError
from urllib.request import ProxyHandler, Request, build_opener


def check(binary):
    repo = Path(__file__).resolve().parents[2]
    gateway_requests = []

    class Gateway(BaseHTTPRequestHandler):
        def do_GET(self):
            gateway_requests.append((self.command, self.path))
            self.send_response(200 if self.path == "/list" else 404)
            self.send_header("Content-Type", "application/json")
            self.end_headers()
            self.wfile.write(b"[]")

        def do_POST(self):
            gateway_requests.append((self.command, self.path))
            self.send_error(405)

        def log_message(self, *_):
            pass

    gateway = ThreadingHTTPServer(("127.0.0.1", 0), Gateway)
    thread = threading.Thread(target=gateway.serve_forever, daemon=True)
    thread.start()
    try:
        with tempfile.TemporaryDirectory(prefix="simulator-workbench-") as temp:
            with socket.socket() as reservation:
                reservation.bind(("127.0.0.1", 0))
                port = reservation.getsockname()[1]
            origin = f"http://127.0.0.1:{port}"
            token = secrets.token_hex(32)
            # Do not inherit provider credentials or existing service settings.
            env = {
                "PATH": os.defpath,
                "SIM_CONTROL_TOKEN": token,
                "SIM_WEBHOOK_TOKEN": secrets.token_hex(32),
                "SIM_MEDIA_TOKEN": secrets.token_hex(32),
                "SIM_ACTIVECALL_URL": f"http://127.0.0.1:{gateway.server_port}",
                "SIM_REALTIME_URL": f"ws://127.0.0.1:{port}/v1/realtime",
                "SIM_SIP_DOMAIN": "workbench-test.invalid",
            }
            opener = build_opener(ProxyHandler({}))

            def request(path, *, method="GET", data=None, auth=True, status=200, key=None):
                headers = {"Authorization": f"Bearer {token}"} if auth else {}
                if data is not None:
                    headers["Content-Type"] = "application/json"
                    data = json.dumps(data).encode()
                if key:
                    headers["Idempotency-Key"] = key
                try:
                    response = opener.open(
                        Request(origin + path, data=data, headers=headers, method=method), timeout=2
                    )
                except HTTPError as error:
                    response = error
                with response:
                    body = response.read(1 << 20)
                    assert response.status == status, (method, path, response.status, status)
                    return body, response.headers

            def api(path, **kwargs):
                return json.loads(request("/sim/v1/" + path, **kwargs)[0])

            with open(Path(temp) / "process.log", "wb") as log:
                process = subprocess.Popen(
                    [str(binary), "-profile", str(repo / "configs/simulator/mock.json"),
                     "-addr", f"127.0.0.1:{port}", "-data", str(Path(temp) / "runs")],
                    cwd=repo, env=env, stdout=log, stderr=log,
                )
                try:
                    deadline = time.monotonic() + 10
                    while True:
                        assert process.poll() is None, "temporary simulator failed to start"
                        try:
                            # Fresh authentication also prevents accepting another process.
                            settings = api("config")
                            break
                        except URLError:
                            if time.monotonic() >= deadline:
                                raise AssertionError("temporary simulator startup timed out") from None
                            time.sleep(0.05)
                    assert settings["active"]["asr"] == "mock"
                    assert settings["active"]["client_rate"] == 16000
                    assert settings["constraints"]["receiving_only"] is True
                    request("/sim/v1/config", auth=False, status=401)
                    page, headers = request("/sim/", auth=False)
                    assert b"app.js" in page and "script-src 'self'" in headers["Content-Security-Policy"]
                    for asset in ("app.js", "style.css"):
                        request("/sim/" + asset, auth=False)
                    assert api("readiness")["gateway_ready"] is True
                    assert len(api("scenarios")["scenarios"]) == 5
                    scenario = json.loads((repo / "scenarios/simulator/cooperative.json").read_text())
                    validated = api("validate", method="POST", data=scenario)
                    assert api("runs")["runs"] == []
                    prepared = api("runs", method="POST", data=scenario, key="workbench-smoke")
                    run_id = prepared["run_id"]
                    assert prepared["dial_target_reference"] == f"sip:{run_id}@workbench-test.invalid"
                    assert api("runs", method="POST", data=scenario, key="workbench-smoke")["run_id"] == run_id
                    assert [run["run_id"] for run in api("runs")["runs"]] == [run_id]
                    stopped = api(f"runs/{run_id}/stop", method="POST")
                    assert stopped["phase"] == "cancelled"
                    report = api(f"runs/{run_id}/report")
                    assert report["run_id"] == run_id and report["phase"] == "cancelled"
                    assert report["scenario_hash"] == validated["scenario_hash"]
                    assert report["prompt_hash"] == validated["prompt_hash"]
                    assert report["transcript_retained"] is False
                    request(f"/sim/v1/runs/{run_id}/transcript", status=409)
                    assert api(f"runs/{run_id}/events")["events"]
                    assert gateway_requests and all(item == ("GET", "/list") for item in gateway_requests)
                finally:
                    process.terminate()
                    try:
                        process.wait(timeout=7)
                    except subprocess.TimeoutExpired:
                        process.kill()
                        process.wait(timeout=2)
            assert process.returncode == 0, "temporary simulator did not shut down cleanly"
        print("PASS: workbench binary routing, auth, validation, reservation, report and cleanup; gateway readiness probes only")
    finally:
        gateway.shutdown()
        gateway.server_close()
        thread.join(timeout=2)


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--binary", type=Path, required=True, help="newly built golive-simulator binary")
    args = parser.parse_args()
    check(args.binary.resolve(strict=True))
