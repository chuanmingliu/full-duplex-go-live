"""Host-side composition contract for Xiaocui. No network or storage at import time."""
import hashlib
import json


TURN = "ai.liuchuanming.golive/turn"


class CatalogChanged(ValueError):
    """A skill changed between selection and retrieval."""


def verified_guidance(call, link, server_label, accept):
    uri = link["uri"]
    entry = call("skills/get", uri=uri)["skill"]
    expected = link["_meta"][TURN]
    if entry["uri"] != uri or expected["uri"] != uri or entry["resources"] != [expected]:
        raise CatalogChanged("skill manifest changed")
    # The host supplies its own policy for this origin, URI and exact digest.
    if not accept(server_label, uri, expected["digest"]):
        return None
    files = call("resources/read", uri=uri)["contents"]
    if len(files) != 1 or files[0]["uri"] != uri or files[0]["mimeType"] != "text/markdown":
        raise ValueError("unexpected skill resource")
    text = files[0]["text"]
    raw = text.encode("utf-8")
    if len(raw) != expected["size"] or "sha256:" + hashlib.sha256(raw).hexdigest() != expected["digest"]:
        raise CatalogChanged("skill content changed")
    # Our catalog emits exactly two JSON-quoted YAML frontmatter fields.
    head, sep, body = text.partition("\n---\n")
    lines = head.splitlines()
    if not sep or len(lines) != 3 or lines[0] != "---":
        raise ValueError("invalid skill frontmatter")
    frontmatter = {}
    for line in lines[1:]:
        key, value = line.split(": ", 1)
        frontmatter[key] = json.loads(value)
    if frontmatter != entry["frontmatter"]:
        raise ValueError("skill frontmatter differs from its manifest")
    return {"server": server_label, "uri": uri, "digest": expected["digest"], "guidance": body.strip()}


def render_turn(call, *, original_prompt, host_policy, host_tools, server_label, event,
                state, state_version, accept_skill, allowed_tools):
    """Return labeled prompt components. The caller owns its state snapshot and tools."""
    args = {"name": "outbound-turn", "arguments": {"event": event, "state": json.dumps(state)}}
    fallback = {"original_prompt": original_prompt, "host_policy": host_policy,
                "host_tools": host_tools, "session_state": dict(state), "state_version": state_version,
                "skill_context": [], "workflow_tools": [], "catalog_revision": None}
    for _ in range(2):
        try:
            route = call("prompts/get", **args)
            meta = route["_meta"][TURN]
            skills = []
            authorized_names = set()
            for message in route["messages"]:
                link = message["content"]
                if link["type"] != "resource_link":
                    raise ValueError("unexpected prompt content")
                item = verified_guidance(call, link, server_label, accept_skill)
                if item is not None:
                    skills.append(item)
                    if binding := link["_meta"].get("ai.liuchuanming.golive/tool"):
                        authorized_names.add(binding)
            names = authorized_names.intersection(meta["tools"], allowed_tools).difference(host_tools)
            tools = [t for t in call("tools/list")["tools"] if t["name"] in names] if names else []
            if route["messages"] and call("prompts/get", **args)["_meta"][TURN]["revision"] != meta["revision"]:
                raise CatalogChanged("catalog changed during rendering")
            return {**fallback, "skill_context": skills, "workflow_tools": tools,
                    "catalog_revision": meta["revision"]}
        except CatalogChanged:
            continue
        except (KeyError, TypeError, ValueError, OSError):
            break
    return fallback
