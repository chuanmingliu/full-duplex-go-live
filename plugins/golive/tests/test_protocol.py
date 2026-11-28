import json
import os
from pathlib import Path
import sys
import time
from uuid import uuid4

from mcp import ClientSession, StdioServerParameters
from mcp.client.stdio import stdio_client

from golive_mcp.runner import PREFIX


async def test_real_stdio_discovery_resource_validation_and_operation_poll(tmp_path):
    for folder in ("in", "out", "done"):
        (tmp_path / ".runner" / folder).mkdir(parents=True)
    (tmp_path / ".runner/alive").write_text(str(time.time()))
    config = tmp_path / "config.json"
    config.write_text(json.dumps({"runner_home": str(tmp_path), "remote_staging": "/tmp/box",
                                  "journal_url": "http://journal.invalid"}))
    server = StdioServerParameters(command=sys.executable, args=["-m", "golive_mcp.server"],
                                   env={**os.environ, "GOLIVE_MCP_CONFIG": str(config)})
    async with stdio_client(server) as (read, write):
        async with ClientSession(read, write) as session:
            await session.initialize()
            tools = (await session.list_tools()).tools
            assert len(tools) == 7 and all(tool.inputSchema and tool.annotations for tool in tools)
            assert all(tool.outputSchema for tool in tools)
            resources = await session.list_resources()
            assert len(resources.resources) == 2
            architecture = await session.read_resource("golive://architecture")
            assert "RustPBX" in architecture.contents[0].text
            assert len((await session.list_prompts()).prompts) == 1
            invalid = await session.call_tool("golive_get_call", {"call_id": "../etc/passwd"})
            assert invalid.isError
            denied = await session.call_tool("golive_place_call", {"request_id": str(uuid4()),
                     "expected_state": "a" * 64, "mode": "active_call"})
            assert denied.isError
            queued = await session.call_tool("golive_get_status", {})
            assert not queued.isError
            request = queued.structuredContent["request_id"]
            job = tmp_path / ".runner/in" / f"golive-mcp-{request}.job"
            assert job.exists()
            # A fake runner completes the read-only job. No SSH or phone operation is executed.
            output = tmp_path / ".runner/out" / f"golive-mcp-{request}.txt"
            output.write_text("PRIVATE\n" + PREFIX + json.dumps({"request_id": request, "ok": True,
                              "data": {"health": {"status": "ok", "sessions": 0}}}) + "\n")
            result = await session.call_tool("golive_get_operation", {"request_id": request})
            assert result.structuredContent["state"] == "completed"
            assert result.structuredContent["result"]["data"]["health"]["status"] == "ok"
            assert "PRIVATE" not in str(result)
