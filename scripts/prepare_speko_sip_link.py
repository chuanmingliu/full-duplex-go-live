#!/usr/bin/env python3
"""Prepare an existing ThinkPad SIP Compose stack to use a tunneled Mac bridge."""
import argparse
import json
from pathlib import Path
import re
import shlex
import subprocess
import sys


# Sent as code; private values travel only through encrypted SSH stdin.
REMOTE_SCRIPT = r'''
import json, os, sys
from pathlib import Path
payload = json.load(sys.stdin)
root = Path(payload["stack"])
if not root.is_absolute():
    raise SystemExit("Stack must be an absolute path.")
base = json.loads((root / "private/adapter.json").read_text())
compose = json.loads((root / "compose.json").read_text())
adapter = compose["services"]["adapter"]
if adapter.get("entrypoint") != ["/opt/golive/adapter"] or "./private:/private:ro" not in adapter.get("volumes", []):
    raise SystemExit("Unrecognized adapter layout; no configuration changed.")
base["golive_url"] = "http://127.0.0.1:8095"
# The deployed adapter predates the optional API-base field. Standalone /v1
# endpoints need no new field or binary upgrade.
base.pop("golive_api_base", None)
overlay = {"services": {"adapter": {
    "env_file": ["./private/speko-adapter.env"],
    "command": ["-config", "/private/adapter-speko.json"]
}}}
files = {
    root / "private/adapter-speko.json": json.dumps(base, indent=2) + "\n",
    root / "private/speko-adapter.env": "GOLIVE_ADAPTER_TOKEN=" + payload["token"] + "\n",
    root / "compose.speko.json": json.dumps(overlay, indent=2) + "\n",
}
# An identical repeat is safe. Different existing files require inspection.
for path, content in files.items():
    if path.is_symlink() or (path.exists() and path.read_text() != content):
        raise SystemExit("A Speko configuration already exists with different contents; no files changed.")
for path, content in files.items():
    if not path.exists():
        fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
        with os.fdopen(fd, "w") as output:
            output.write(content)
    os.chmod(path, 0o600)
print("Prepared the Speko SIP profile and Compose override. Original configuration preserved; no service restarted or call placed.")
'''


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--box", required=True, help="SSH user@host or configured alias")
    parser.add_argument("--stack", required=True, help="absolute existing SIP stack directory on ThinkPad")
    parser.add_argument("--env", type=Path, default=Path.home() / ".config/golive/speko.env")
    args = parser.parse_args()
    token = ""
    for line in args.env.expanduser().read_text().splitlines():
        match = re.fullmatch(r"\s*(?:export\s+)?GOLIVE_AUTH_TOKEN\s*=\s*(.*?)\s*", line)
        if match:
            token = match.group(1).strip("\"'")
    if not re.fullmatch(r"[A-Za-z0-9_.-]+", token):
        parser.error("The env file needs a valid GOLIVE_AUTH_TOKEN; run configure_speko.py first.")
    if args.box.startswith("-"):
        parser.error("Invalid SSH destination.")
    result = subprocess.run(
        ["ssh", "-o", "ConnectTimeout=8", args.box, "python3 -c " + shlex.quote(REMOTE_SCRIPT)],
        input=json.dumps({"stack": args.stack, "token": token}), text=True,
    )
    sys.exit(result.returncode)


if __name__ == "__main__":
    main()
