#!/usr/bin/env python3
"""Render a dedicated Active Call config without embedding credentials in Git."""
import argparse
import json
import os
from pathlib import Path


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--output", required=True)
    parser.add_argument("--token-file", required=True)
    parser.add_argument("--webhook-url", default="http://simulator:18865/active-call/incoming")
    parser.add_argument("--external-ip", default="")
    parser.add_argument("--sip-port", type=int, default=25060)
    parser.add_argument("--rtp-start", type=int, default=24000)
    parser.add_argument("--rtp-end", type=int, default=24019)
    args = parser.parse_args()
    if not (1024 <= args.sip_port <= 65535 and 1024 <= args.rtp_start < args.rtp_end <= 65535 and args.rtp_end - args.rtp_start <= 100):
        parser.error("SIP/RTP ports must be bounded unprivileged ports")
    token = Path(args.token_file).read_text().strip()
    if len(token) < 32 or any(ord(c) < 33 for c in token):
        parser.error("webhook token must contain at least 32 non-whitespace characters")
    # JSON strings are also valid TOML basic strings for these ASCII settings.
    text = f'''http_addr = "0.0.0.0:8080"
addr = "0.0.0.0"
udp_port = {args.sip_port}
rtp_bind_ip = "0.0.0.0"
rtp_start_port = {args.rtp_start}
rtp_end_port = {args.rtp_end}
codecs = ["pcmu"]
accept_timeout = "15s"
log_level = "warn"
auto_refer = false
enable_options_response = false
graceful_shutdown = true
graceful_shutdown_timeout = 5
'''
    if args.external_ip:
        text += f"external_ip = {json.dumps(args.external_ip)}\n"
    text += f'''
[recording]
enabled = false
[handler]
type = "webhook"
url = {json.dumps(args.webhook_url)}
method = "POST"
headers = [["Authorization", {json.dumps("Bearer " + token)}]]
'''
    path = Path(args.output)
    path.parent.mkdir(parents=True, exist_ok=True)
    fd = os.open(path, os.O_CREAT | os.O_EXCL | os.O_WRONLY, 0o600)
    with os.fdopen(fd, "w") as output:
        output.write(text)


if __name__ == "__main__":
    main()
