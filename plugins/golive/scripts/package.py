#!/usr/bin/env python3
"""Build a deterministic distributable plugin zip from an explicit file allowlist."""
import hashlib
from pathlib import Path
import zipfile

root = Path(__file__).resolve().parents[1]
files = [root / name for name in ("README.md", "LICENSE", "pyproject.toml", "uv.lock", "config.example.json",
         ".mcp.json", ".mcp.codex.json", ".claude-plugin/plugin.json", ".codex-plugin/plugin.json")]
files += list((root / "src/golive_mcp").glob("*.py"))
files += list((root / "skills").glob("*/SKILL.md"))
files += list((root / "tests").glob("*.py"))
files += [root / "scripts/package.py", root / "scripts/verify_dist.py", root / "tests/evaluation.xml"]
output = root / "dist/golive-plugin-0.1.0.zip"
output.parent.mkdir(exist_ok=True)
with zipfile.ZipFile(output, "w", compression=zipfile.ZIP_DEFLATED) as archive:
    for path in sorted(files):
        if not path.is_file() or path.is_symlink():
            raise SystemExit("Missing or unexpected package input")
        info = zipfile.ZipInfo("golive/" + path.relative_to(root).as_posix(), (2026, 1, 1, 0, 0, 0))
        info.compress_type = zipfile.ZIP_DEFLATED
        info.external_attr = 0o100644 << 16
        archive.writestr(info, path.read_bytes())
print(output)
print("sha256 " + hashlib.sha256(output.read_bytes()).hexdigest())
