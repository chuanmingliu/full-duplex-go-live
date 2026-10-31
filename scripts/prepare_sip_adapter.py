#!/usr/bin/env python3
"""Copy an agent profile into a separate experiment; never start a service."""
import argparse
import json
import os
from pathlib import Path


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--base", type=Path, required=True)
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument("--state-dir", type=Path, required=True,
                        help="absolute state path as seen by the golive process")
    args = parser.parse_args()
    if not args.state_dir.is_absolute():
        parser.error("--state-dir must be absolute")
    with args.base.open() as source:
        profile = json.load(source)
    profile.update(addr="127.0.0.1:9875", max_sessions=1, max_sessions_per_ip=1,
                   web_root="", hangup_url="",
                   store_dir=str(args.state_dir / "golive"),
                   last_call_file=str(args.state_dir / "last-call.json"))
    profile.setdefault("realtime", {}).update(
        enabled=True, hold_for_opening=True, hold_timeout_seconds=180,
        store_calls=True)
    # Exclusive creation prevents overwriting a production profile. The copy
    # may contain private instructions, so do not use default file permissions.
    fd = os.open(args.output, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    with os.fdopen(fd, "w") as target:
        json.dump(profile, target, ensure_ascii=False, indent=2)
        target.write("\n")
    print(f"Created {args.output}; no service started. Agent port: 9875.")


if __name__ == "__main__":
    main()
