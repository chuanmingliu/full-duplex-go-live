"""Run skills tests with the active provider's connection settings.

Only provider credentials, endpoint and model identifiers enter the child
environment. The all/edges phases use synthetic prompts and session data.
Business tool calls are intercepted by the Go test's synthetic executor.

The explicit `slot` phase additionally reads the saved instructions on the box
for the owner's default-prompt v1/v2 comparison. It requires an isolated fixture.
"""
import hashlib
import json
import os
from pathlib import Path
import re
import subprocess
import sys
import time
import urllib.request

os.umask(0o077)
root = Path.home() / "golive-telephony"
pids = set(re.findall(r"pid=(\d+)", subprocess.check_output(
    ["ss", "-lntpH", "sport = :8765"], text=True)))
assert len(pids) == 1, "active engine listener ambiguous"
process = Path("/proc") / pids.pop()
paths = [root / "configs/telephony.json", process / "exe", process / "cmdline"]
phase = sys.argv[3] if len(sys.argv) > 3 else "all"
assert phase in ("all", "edges", "slot")
if phase == "slot":
    assert len(sys.argv) > 4 and sys.argv[4] == "fixture", "slot phase requires fixture"
    paths.append(Path.home() / "golive-last-call.json")


def hashes():
    return [hashlib.sha256(path.read_bytes()).hexdigest() for path in paths]


before = hashes()
active = {key.decode(): value.decode() for key, value in (
    item.split(b"=", 1) for item in (process / "environ").read_bytes().split(b"\0")
    if b"=" in item)}
args = [item.decode() for item in (process / "cmdline").read_bytes().split(b"\0") if item]
envfile = Path(args[args.index("-env") + 1] if "-env" in args else ".env.local")
if not envfile.is_absolute():
    envfile = root / envfile
for line in envfile.read_text().splitlines():
    line = line.strip()
    if not line or line.startswith("#"):
        continue
    key, sep, value = line.removeprefix("export ").partition("=")
    if not sep:
        continue
    value = value.strip()
    if len(value) >= 2 and value[0] in ("'", '"') and value[-1] == value[0]:
        value = value[1:-1]
    active.setdefault(key.strip(), value)
profile = json.loads(paths[0].read_text())
provider = active.get("GOLIVE_LLM", profile.get("llm", "deepseek"))
key_name, prefix, default_model = {
    "deepseek": ("DEEPSEEK_API_KEY", "DEEPSEEK", "deepseek-chat"),
    "openai-compatible": ("OPENAI_API_KEY", "OPENAI", "gpt-4o-mini"),
    "cerebras": ("CEREBRAS_API_KEY", "CEREBRAS", "qwen-3.8-27b"),
    "inception": ("INCEPTION_API_KEY", "INCEPTION", "mercury-2.5"),
}[provider]
model = active.get("GOLIVE_BACKEND_MODEL", profile.get("backend_model", default_model))
allowed = {key_name, key_name + "S", key_name + "S_FILE",
           "GOLIVE_BACKEND_BASE_URL", "GOLIVE_BACKEND_MODEL",
           "GOLIVE_" + prefix + "_BASE_URL", "GOLIVE_" + prefix + "_MODEL"}
allowed.update(key_name + "_" + str(n) for n in range(2, 10))
env = {key: value for key, value in active.items() if key in allowed}
env.update({"PATH": "/usr/local/bin:/usr/bin:/bin", "HOME": str(Path.home()),
            "SKILLS_REAL_PROVIDER": provider, "SKILLS_REAL_MODEL": model,
            "SKILLS_SIM_URL": "http://127.0.0.1:8794/mcp",
            "SKILLS_SIM_TOKEN_FILE": str(Path.home() / "golive-skills-mcp/secrets/runtime"),
            "SKILLS_REAL_RUNS": sys.argv[2],
            "SKILLS_REAL_REPORT": str(Path.cwd() / "report.json")})
assert 1 <= int(sys.argv[2]) <= 20
if phase == "slot":
    env["SKILLS_SLOT_PROMPT_FILE"] = str(paths[-1])
print(json.dumps({"provider": provider, "model": model, "prompts": "saved-default" if phase == "slot" else "synthetic",
                  "tts": "mock", "real_business_actions": False}), flush=True)
fixture = None
if len(sys.argv) > 4 and sys.argv[4] == "fixture":
    directory = Path.cwd() / "mcp-fixture"
    directory.mkdir(mode=0o700)
    token = directory / "runtime"
    token.write_text("simulation-runtime-token")
    token.chmod(0o600)
    fixture_log = (directory / "service.log").open("w")
    fixture = subprocess.Popen([str(Path(sys.argv[1]).with_name("catalog.test")),
                                "-test.run", "^TestSkillsSimulationService$", "-test.timeout", "16m"],
                               env={"PATH": env["PATH"], "SKILLS_FIXTURE_DIR": str(directory)},
                               stdout=fixture_log, stderr=subprocess.STDOUT)
    env["SKILLS_SIM_URL"] = "http://127.0.0.1:18795/mcp"
    env["SKILLS_SIM_TOKEN_FILE"] = str(token)
try:
    if fixture is not None:
        for _ in range(50):
            assert fixture.poll() is None, "isolated service exited"
            try:
                with urllib.request.urlopen("http://127.0.0.1:18795/healthz", timeout=1) as response:
                    assert response.status == 200
                break
            except OSError:
                time.sleep(0.1)
        else:
            raise RuntimeError("isolated service did not start")
    tests = {"slot": "^TestSkillSlotRealModel$", "edges": "^TestSkillsRealModelEdges$", "all": "^TestSkillsRealModel"}[phase]
    result = subprocess.run([sys.argv[1], "-test.run", tests,
                             "-test.v", "-test.timeout", "15m"],
                            env=env, cwd=root, timeout=905)
finally:
    if fixture is not None:
        fixture.terminate()
        fixture.wait(timeout=5)
        fixture_log.close()
    preserved = hashes() == before
    print(json.dumps({"original_runtime_and_profile_preserved": preserved}), flush=True)
    assert preserved, "active runtime changed during simulation"
sys.exit(result.returncode)
