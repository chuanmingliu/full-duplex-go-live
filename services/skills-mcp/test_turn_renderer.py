import hashlib
import unittest

from turn_renderer import TURN, render_turn


class TurnRendererTest(unittest.TestCase):
    uri = "skill://fixture/SKILL.md"
    text = '---\nname: "fixture"\ndescription: "Test workflow"\n---\n\nGuidance'
    frontmatter = {"name": "fixture", "description": "Test workflow"}

    def setUp(self):
        self.calls = []
        self.file = self.text
        self.manifest = {"uri": self.uri, "size": len(self.text.encode()),
                         "digest": "sha256:" + hashlib.sha256(self.text.encode()).hexdigest()}

    def rpc(self, method, **params):
        self.calls.append(method)
        if method == "prompts/get":
            return {"_meta": {TURN: {"revision": 4, "tools": ["quote_total"]}}, "messages": [
                {"content": {"type": "resource_link", "uri": self.uri, "_meta": {
                    TURN: self.manifest, "ai.liuchuanming.golive/tool": "quote_total"}}}]}
        if method == "skills/get":
            return {"skill": {"uri": self.uri, "resources": [self.manifest], "frontmatter": self.frontmatter}}
        if method == "resources/read":
            return {"contents": [{"uri": self.uri, "mimeType": "text/markdown", "text": self.file}]}
        if method == "tools/list":
            return {"tools": [{"name": "quote_total", "inputSchema": {"type": "object"}}]}
        self.fail(method)

    def render(self, allow, native=None):
        return render_turn(self.rpc, original_prompt="owner text", host_policy="host policy",
                           host_tools=native or {}, state_version=12, server_label="thinkpad",
                           event="quote.revised", state={}, accept_skill=lambda *_: allow,
                           allowed_tools={"quote_total"})

    def test_rejected_manifest_loads_no_body_or_tool(self):
        slots = self.render(False)
        self.assertEqual(slots["skill_context"], [])
        self.assertEqual(slots["workflow_tools"], [])
        self.assertNotIn("resources/read", self.calls)

    def test_integrity_failure_preserves_baseline(self):
        self.file += "changed"
        slots = self.render(True)
        self.assertEqual(slots["original_prompt"], "owner text")
        self.assertEqual(slots["state_version"], 12)
        self.assertIsNone(slots["catalog_revision"])
        self.assertEqual(slots["workflow_tools"], [])

    def test_native_tool_wins_and_guidance_has_provenance(self):
        slots = self.render(True, {"quote_total": {"source": "existing"}})
        self.assertEqual(slots["skill_context"][0]["server"], "thinkpad")
        self.assertEqual(slots["skill_context"][0]["guidance"], "Guidance")
        self.assertEqual(slots["workflow_tools"], [])
        self.assertNotIn("tools/list", self.calls)
