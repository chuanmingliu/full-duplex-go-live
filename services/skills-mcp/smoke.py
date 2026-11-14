"""Verify the installed service from the ThinkPad as its operator (stdlib only)."""
import hashlib
import json
import os
import uuid
from pathlib import Path
from urllib.error import HTTPError
from urllib.request import Request, urlopen

from turn_renderer import render_turn

ROOT = Path(os.environ.get("STACK_DIR", Path.home() / "golive-skills-mcp"))
BASE = "http://127.0.0.1:8794"
VERSION = "2026-07-28"


def fetch(path, key=None, data=None, headers=None):
    hdr = {"Accept": "application/json, text/event-stream", **(headers or {})}
    if key:
        token = (ROOT / "secrets" / key).read_text().strip()
        hdr["Authorization"] = "Bearer " + token
    body = None
    if data is not None:
        body = json.dumps(data).encode()
        hdr["Content-Type"] = "application/json"
    with urlopen(Request(BASE + path, data=body, headers=hdr), timeout=185) as res:
        return res.read()


def rpc(method, **params):
    meta = {
        "io.modelcontextprotocol/protocolVersion": VERSION,
        "io.modelcontextprotocol/clientInfo": {"name": "xiaocui-smoke", "version": "1"},
        "io.modelcontextprotocol/clientCapabilities": {"extensions": {"io.modelcontextprotocol/skills": {}}},
    }
    headers = {"Mcp-Protocol-Version": VERSION, "Mcp-Method": method}
    if method == "resources/read":
        headers["Mcp-Name"] = params["uri"]
    if method in {"tools/call", "prompts/get"}:
        headers["Mcp-Name"] = params["name"]
    msg = {"jsonrpc": "2.0", "id": 1, "method": method, "params": {**params, "_meta": meta}}
    return json.loads(fetch("/mcp", "runtime", msg, headers))["result"]


def main():
    assert b"Skills &amp; tools" in fetch("/")
    try:
        fetch("/admin/catalog", "runtime")
    except HTTPError as error:
        assert error.code == 401
    else:
        raise RuntimeError("runtime token granted administrative access")
    catalog = json.loads(fetch("/admin/catalog", "admin"))
    assert "io.modelcontextprotocol/skills" in rpc("server/discover")["capabilities"]["extensions"]
    entries = rpc("skills/list")["skills"]
    legacy = json.loads(rpc("resources/read", uri="skill://index.json")["contents"][0]["text"])
    assert {e["uri"] for e in entries} == {e["url"] for e in legacy["skills"]}
    for entry in entries:
        assert rpc("skills/get", uri=entry["uri"])["skill"] == entry
        text = rpc("resources/read", uri=entry["uri"])["contents"][0]["text"].encode()
        assert entry["resources"] == [{"uri": entry["uri"], "size": len(text),
                                      "digest": "sha256:" + hashlib.sha256(text).hexdigest()}]
    tools = [t["name"] for t in rpc("tools/list")["tools"]]
    outcome = "disabled"
    if catalog["quote_total"]["enabled"]:
        assert "quote_total" in tools
        # Exercise CRM intake with a synthetic call ID and an unresolved coverage tier.
        result = rpc("tools/call", name="quote_total", arguments={
            "call": "skills-smoke-" + str(uuid.uuid4()),
            "memory": {"quote": {"三者险": True}}, "memory_version": 1})
        assert not result.get("isError"), "CRM tool reported an error"
        outcome = result["structuredContent"]["status"]
        assert outcome in {"need", "quote"}
    # All inputs here are synthetic. Pin the manifests discovered by this test.
    approved = {(e["uri"], e["resources"][0]["digest"]) for e in entries}
    observed = []
    for event, state in [("conversation.normal", {}), ("wechat.notification_help", {"wechat.status": "consented"}),
                         ("wechat.notification_help", {"wechat.status": "added"}), ("quote.revised", {})]:
        slots = render_turn(rpc, original_prompt="原始外呼提示", host_policy="fixture policy", host_tools={},
                            server_label="thinkpad-xiaocui", event=event, state=state, state_version=1,
                            accept_skill=lambda origin, uri, digest: (uri, digest) in approved,
                            allowed_tools={"quote_total"})
        assert slots["catalog_revision"] == catalog["revision"], "renderer fell back"
        observed.append({"event": event, "state": state, "skills": [s["uri"] for s in slots["skill_context"]],
                         "tools": [t["name"] for t in slots["workflow_tools"]]})
    print(json.dumps({"revision": catalog["revision"], "skill_count": len(entries),
                      "tools": tools, "synthetic_crm_status": outcome, "manifest_verified": True, "rendered_turns": observed}))


if __name__ == "__main__":
    main()
