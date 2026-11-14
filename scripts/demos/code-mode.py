#!/usr/bin/env python3
"""Run the local MCP code-mode walkthrough and render its verified protocol trace."""
import argparse
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile

ROOT = Path(__file__).resolve().parents[2]
SERVICE = ROOT / "services" / "skills-mcp"


def render_trace(trace, output):
    if not trace.get("verified") or not trace.get("steps"):
        raise ValueError("The walkthrough did not produce a verified trace")
    template = Path(__file__).with_suffix(".html").read_text()
    payload = json.dumps(trace, ensure_ascii=True).replace("<", "\\u003c")
    if template.count("__CODE_MODE_TRACE__") != 1:
        raise ValueError("The viewer template must have exactly one trace placeholder")
    fragment = template.replace("__CODE_MODE_TRACE__", payload)
    if len(fragment.encode()) >= 1_000_000:
        raise ValueError("The inline trace exceeds the viewer size limit")
    (output / "code-mode-demo.html").write_text(fragment)
    (output / "trace.json").write_text(json.dumps(trace, ensure_ascii=False, indent=2) + "\n")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--output", type=Path, default=ROOT / "outputs" / "code-mode-demo")
    parser.add_argument("--race", action="store_true", help="enable Go race detection")
    parser.add_argument("--render-trace", type=Path, help="render an existing verified trace without rerunning")
    args = parser.parse_args()
    output = args.output.resolve()
    output.mkdir(parents=True, exist_ok=True)
    if args.render_trace:
        render_trace(json.loads(args.render_trace.read_text()), output)
        print(f"Rendered verified trace in {output}")
        return 0
    command = ["go", "test", "-count=1", "-v", "-timeout=120s"]
    if args.race:
        command.append("-race")
    command.extend(["-run", "^TestCodeModeWalkthrough$", "."])
    print("Running the MCP service and JavaScript sandbox with a synthetic CRM.", flush=True)
    print("The JavaScript is prewritten. All protocol results are captured from this run.", flush=True)
    with tempfile.TemporaryDirectory(prefix="run-", dir=output) as temporary:
        trace_path = Path(temporary) / "trace.json"
        env = dict(os.environ, GOLIVE_CODE_MODE_DEMO_TRACE=str(trace_path))
        try:
            completed = subprocess.run(command, cwd=SERVICE, env=env, timeout=180, check=False)
        except (OSError, subprocess.TimeoutExpired) as error:
            print(f"Demo failed: {error}", file=sys.stderr)
            return 1
        if completed.returncode:
            return completed.returncode
        trace = json.loads(trace_path.read_text())
        render_trace(trace, output)
    print(f"\nStep-through viewer: {output / 'code-mode-demo.html'}")
    print(f"Verified protocol trace: {output / 'trace.json'}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
