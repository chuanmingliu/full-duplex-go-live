#!/usr/bin/env python3
"""Prompt privately and save the standalone Speko service/adapter credentials."""
import argparse
import getpass
import os
from pathlib import Path
import re
import secrets
import sys
import tempfile


def save_key(path: Path, key: str) -> None:
    # Credentials are opaque bearer tokens, not a guaranteed provider prefix.
    # Pasting from a dashboard can include surrounding spaces (including NBSP).
    key = key.strip()
    if not key:
        raise ValueError("No key entered. Paste the API key at the hidden prompt, then press Enter.")
    if not re.fullmatch(r"[A-Za-z0-9._~+/-]+=*", key):
        raise ValueError("Paste only the API key, without quotes, a Bearer prefix, or internal whitespace.")
    if path.is_symlink():
        raise ValueError("Refusing to replace a symlink; choose the actual env file.")
    path.parent.mkdir(mode=0o700, parents=True, exist_ok=True)
    lines = path.read_text().splitlines() if path.exists() else []
    token = ""
    for line in lines:
        match = re.fullmatch(r"(?:export\s+)?GOLIVE_AUTH_TOKEN\s*=\s*(.*?)\s*", line)
        if match:
            token = match.group(1).strip("\"'")
    if token and not re.fullmatch(r"[A-Za-z0-9_.-]+", token):
        raise ValueError("Existing GOLIVE_AUTH_TOKEN is not a supported dotenv token.")
    token = token or secrets.token_urlsafe(32)
    updates = {"SPEKO_API_KEY": key, "GOLIVE_AUTH_TOKEN": token, "GOLIVE_ADAPTER_TOKEN": token}
    output = []
    for line in lines:
        match = re.match(r"\s*(?:export\s+)?([A-Z_]+)\s*=", line)
        if not match or match.group(1) not in updates:
            output.append(line)
    # Single quotes preserve bearer characters such as a leading ~ when sourced
    # by a shell, and are also understood by GoLive's dotenv loader.
    output.extend(f"{name}='{value}'" if name == "SPEKO_API_KEY" else f"{name}={value}"
                  for name, value in updates.items())
    temporary = None
    try:
        with tempfile.NamedTemporaryFile(mode="w", dir=path.parent, prefix=".speko-", delete=False) as target:
            temporary = target.name
            os.fchmod(target.fileno(), 0o600)
            target.write("\n".join(output) + "\n")
            target.flush()
            os.fsync(target.fileno())
        os.replace(temporary, path)
        temporary = None
    finally:
        if temporary is not None:
            os.unlink(temporary)


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--env", type=Path, default=Path.home() / ".config/golive/speko.env")
    args = parser.parse_args()
    if not sys.stdin.isatty():
        parser.error("Run in an interactive terminal; the API key is read with echo disabled.")
    try:
        save_key(args.env.expanduser(), getpass.getpass("Paste Speko API key, then press Enter (input hidden): "))
    except (ValueError, OSError) as exc:
        # Do not print exceptions whose messages could contain file contents.
        parser.exit(1, (str(exc) if isinstance(exc, ValueError) else "Could not write the credential file.") + "\n")
    print(f"Saved private credentials to {args.env.expanduser()} (mode 600).")
    print("Restart the Speko service with this env file. No service was restarted and no call was placed.")


if __name__ == "__main__":
    main()
