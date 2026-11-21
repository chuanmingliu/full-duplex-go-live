#!/usr/bin/env python3
"""Install built artifacts in fresh directories and exercise actual MCP transports.

Run with `uv run --frozen python scripts/verify_dist.py` after `uv build` and
`python scripts/package.py`. No runner jobs, network service calls or dials.
"""
import asyncio
import hashlib
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
from uuid import uuid4
import zipfile

from mcp import ClientSession, StdioServerParameters
from mcp.client.stdio import stdio_client

ROOT = Path(__file__).resolve().parents[1]
TOOLS = {"golive_get_status", "golive_list_calls", "golive_list_scenarios", "golive_get_call",
         "golive_place_call", "golive_analyze_call", "golive_get_operation"}


def check(condition, message):
    if not condition:
        raise RuntimeError(message)


def run(args):
    result = subprocess.run(args, text=True, capture_output=True, timeout=180)
    check(result.returncode == 0, "Artifact preparation failed: " + result.stderr[-1500:])


async def handshake(command, args, cwd, config, log):
    params = StdioServerParameters(command=str(command), args=args, cwd=str(cwd),
                                  env={**os.environ, "GOLIVE_MCP_CONFIG": str(config)})
    with log.open("w") as errors:
        async with stdio_client(params, errlog=errors) as (read, write):
            async with ClientSession(read, write) as session:
                await session.initialize()
                tools = (await session.list_tools()).tools
                check({tool.name for tool in tools} == TOOLS, "Tool discovery differs")
                check(all(tool.inputSchema and tool.outputSchema for tool in tools), "Tool schema missing")
                resource = await session.read_resource("golive://architecture")
                check("RustPBX" in resource.contents[0].text, "Architecture resource unavailable")
                prompt = await session.get_prompt("review_call", {"call_id": "out-fixture"})
                check(bool(prompt.messages), "Review prompt unavailable")
                denied = await session.call_tool("golive_place_call", {"request_id": str(uuid4()),
                     "expected_state": "a" * 64, "mode": "active_call"})
                check(denied.isError, "Calls were not disabled by local configuration")


async def main():
    bundle = ROOT / "dist/golive-plugin-0.1.0.zip"
    original = hashlib.sha256(bundle.read_bytes()).hexdigest()
    run([sys.executable, str(ROOT / "scripts/package.py")])
    check(hashlib.sha256(bundle.read_bytes()).hexdigest() == original, "Plugin zip is not reproducible")
    with tempfile.TemporaryDirectory(prefix="golive-distribution-") as temporary:
        work = Path(temporary)
        with zipfile.ZipFile(bundle) as archive:
            names = archive.namelist()
            check(all(n.startswith("golive/") and ".." not in Path(n).parts for n in names), "Unsafe zip path")
            check(not any(part in {".env", ".venv", "mcp.local.json", "__pycache__"}
                          for n in names for part in Path(n).parts), "Private or generated files in package")
            archive.extractall(work)
        plugin = work / "golive"
        config = work / "mcp.json"
        config.write_text(json.dumps({"runner_home": str(work / "nonexistent-runner"),
                          "remote_staging": "/tmp/unused", "journal_url": "http://127.0.0.1:1",
                          "allow_real_calls": False, "allow_sim_calls": False}))
        run(["uv", "sync", "--frozen", "--no-dev", "--directory", str(plugin)])
        for host, manifest in (("claude", ".mcp.json"), ("codex", ".mcp.codex.json")):
            spec = json.loads((plugin / manifest).read_text())["mcpServers"]["golive"]
            args = [arg.replace("${CLAUDE_PLUGIN_ROOT}", str(plugin)) for arg in spec["args"]]
            cwd = plugin / spec["cwd"] if "cwd" in spec else work
            await handshake(spec["command"], args, cwd, config, work / (host + ".log"))
        environment = work / "wheel-env"
        run(["uv", "venv", "--python", sys.executable, str(environment)])
        wheel = ROOT / "dist/golive_mcp-0.1.0-py3-none-any.whl"
        run(["uv", "pip", "install", "--python", str(environment / "bin/python"), str(wheel)])
        await handshake(environment / "bin/golive-mcp", [], work, config, work / "wheel.log")
        check(not (work / "nonexistent-runner").exists(), "Discovery or denied call created a runner")
    print(json.dumps({"reproducible_zip": True, "clean_zip_install": True,
                      "claude_mcp_launch": True, "codex_mcp_launch": True,
                      "clean_wheel_launch": True, "tools_per_client": len(TOOLS),
                      "real_calls": 0, "runner_jobs": 0, "zip_sha256": original}))


if __name__ == "__main__":
    asyncio.run(main())
