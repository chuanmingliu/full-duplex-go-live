#!/usr/bin/env python3
"""Check service ownership, branch coverage and local runbook links without secrets."""
import json
from pathlib import Path
import re
from urllib.parse import unquote

ROOT = Path(__file__).resolve().parents[1]


def main():
    catalog = json.loads((ROOT / "docs/services/catalog.json").read_text())
    registry = json.loads((ROOT / "docs/services/branches.json").read_text())
    ids = [service["id"] for service in catalog["services"]]
    assert len(ids) == len(set(ids)), "duplicate service ID"
    assert set(ids) == set(registry["services"]), "branch registry must cover every service"
    for service in catalog["services"]:
        sid = service["id"]
        assert service["codebase"] in catalog["codebases"], sid
        assert service["branch_key"] == sid, sid
        branch = registry["services"][sid]
        assert branch["branch"].startswith("codex/"), sid
        assert branch["scope"] in ("source", "deployment-integration"), sid
        expected = f"https://github.com/{branch['repository']}/tree/{branch['branch']}"
        assert branch["url"] == expected, sid
    docs = list((ROOT / "docs/services").glob("*.md"))
    docs += list((ROOT / "deploy/services").rglob("*.md"))
    for doc in docs:
        for target in re.findall(r"\[[^\]]*\]\(([^)]+)\)", doc.read_text()):
            if "://" in target or target.startswith("#"):
                continue
            target = unquote(target.split("#", 1)[0].strip("<>"))
            assert (doc.parent / target).exists(), f"{doc.relative_to(ROOT)}: {target}"
    agent = ROOT / ".cursor/agents/golive-service-maintainer.md"
    assert agent.read_text().startswith("---\nname: golive-service-maintainer\n")
    print(f"Validated {len(ids)} service-to-codebase/branch mappings and {len(docs)} runbooks")


if __name__ == "__main__":
    main()
