#!/usr/bin/env python3
"""One synthetic PCMU SIP call in the isolated simulator Docker network.

No credentials, providers, carrier, retries, transfer, or arbitrary public target.
Run with Python 3.11+ stdlib in the dedicated fixture container.
"""
import argparse
import ipaddress
import json
import math
import re
import secrets
import select
import socket
import struct
import sys
import time


def mulaw(sample):
    sign = 0x80 if sample < 0 else 0
    value = min(abs(sample), 32635) + 132
    exponent = max(0, value.bit_length() - 8)
    mantissa = (value >> (exponent + 3)) & 0x0F
    return (~(sign | (exponent << 4) | mantissa)) & 0xFF


def packet_parts(packet):
    text = packet.decode("utf-8", errors="replace")
    head, _, body = text.partition("\r\n\r\n")
    lines = head.split("\r\n")
    headers = {}
    for line in lines[1:]:
        key, sep, value = line.partition(":")
        if sep:
            headers[key.lower()] = value.strip()
    return lines[0], headers, body


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--allow-isolated-sip", action="store_true", required=True)
    parser.add_argument("--target-host", default="activecall", choices=["activecall", "127.0.0.1", "localhost"])
    parser.add_argument("--target-port", default=25060, type=int)
    parser.add_argument("--run-id", required=True)
    parser.add_argument("--sip-domain", default="simulator.invalid")
    parser.add_argument("--duration", default=8.0, type=float)
    parser.add_argument("--cancel-after-ms", default=-1, type=int)
    args = parser.parse_args()
    if not re.fullmatch(r"sim-[0-9a-f]{24}", args.run_id):
        parser.error("run ID must be the exact reserved simulator run")
    if not re.fullmatch(r"[A-Za-z0-9.-]+(?::[0-9]+)?", args.sip_domain):
        parser.error("invalid isolated SIP domain")
    if not 0 < args.duration <= 30 or not 1024 <= args.target_port <= 65535:
        parser.error("test duration must be 1..30 seconds and target port unprivileged")
    if not -1 <= args.cancel_after_ms <= 5000:
        parser.error("cancellation delay must be -1 (disabled) or 0..5000 ms")
    target_ip = socket.gethostbyname(args.target_host)
    if not ipaddress.ip_address(target_ip).is_private:
        parser.error("fixture refuses a public SIP target")
    target = (target_ip, args.target_port)
    sip = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
    sip.connect(target)
    local_ip, local_port = sip.getsockname()
    rtp = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
    rtp.bind((local_ip, 0))
    rtp.setblocking(False)
    rtp_port = rtp.getsockname()[1]
    call_id = secrets.token_hex(12) + "@fixture.invalid"
    tag = secrets.token_hex(6)
    branch = "z9hG4bK" + secrets.token_hex(10)
    uri = f"sip:{args.run_id}@{args.sip_domain}"
    from_header = f"<sip:fixture@{local_ip}:{local_port}>;tag={tag}"
    to_header = f"<{uri}>"
    contact_uri = uri
    sdp = (f"v=0\r\no=- 1 1 IN IP4 {local_ip}\r\ns=isolated-simulator-fixture\r\n"
           f"c=IN IP4 {local_ip}\r\nt=0 0\r\nm=audio {rtp_port} RTP/AVP 0\r\n"
           "a=rtpmap:0 PCMU/8000\r\na=ptime:20\r\na=sendrecv\r\n")

    def request(method, body="", cseq=1, transaction_branch=None):
        nonlocal to_header
        selected_uri = contact_uri if method in ("ACK", "BYE") else uri
        headers = [f"{method} {selected_uri} SIP/2.0",
                   f"Via: SIP/2.0/UDP {local_ip}:{local_port};branch={transaction_branch or branch};rport",
                   "Max-Forwards: 5", f"From: {from_header}", f"To: {to_header}",
                   f"Call-ID: {call_id}", f"CSeq: {cseq} {method}",
                   f"Contact: <sip:fixture@{local_ip}:{local_port}>"]
        if body:
            headers.append("Content-Type: application/sdp")
        headers.append(f"Content-Length: {len(body.encode())}")
        sip.send(("\r\n".join(headers) + "\r\n\r\n" + body).encode())

    def ok_response(headers):
        values = ["SIP/2.0 200 OK"]
        for name in ["Via", "From", "To", "Call-ID", "CSeq"]:
            values.append(f"{name}: {headers.get(name.lower(), '')}")
        values.append("Content-Length: 0")
        sip.send(("\r\n".join(values) + "\r\n\r\n").encode())

    report = {"evidence": "isolated_sip_synthetic_pcmu", "answered": False,
              "cancel_sent": False, "cancelled": False, "ended": False,
              "rtp_sent": 0, "rtp_received": 0, "non_silent_rtp_received": 0,
              "carrier_verified": False, "natural_speech_verified": False}
    start = time.monotonic()
    deadline = start + args.duration + 12
    answered_at = None
    media_target = None
    next_packet = 0.0
    sequence = 0
    timestamp = 0
    ssrc = secrets.randbits(32)
    bye_sent = False
    request("INVITE", sdp)
    try:
        while time.monotonic() < deadline:
            now = time.monotonic()
            if not report["cancel_sent"] and args.cancel_after_ms >= 0 and (now - start) * 1000 >= args.cancel_after_ms:
                if report["answered"]:
                    report["failure"] = "call answered before planned CANCEL"
                    break
                request("CANCEL")
                report["cancel_sent"] = True
            if answered_at is not None and not bye_sent and now - answered_at >= args.duration:
                request("BYE", cseq=2, transaction_branch="z9hG4bK" + secrets.token_hex(10))
                bye_sent = True
            if media_target and not bye_sent and now >= next_packet:
                # One-second tone / one-second silence keeps input flowing
                # throughout the test and lets the mock ASR detect turns.
                speaking = int(now - answered_at) % 2 == 0
                pcmu = bytes(mulaw(int(6000 * math.sin(2 * math.pi * 440 * (timestamp + i) / 8000))) if speaking else 0xFF for i in range(160))
                rtp.sendto(struct.pack("!BBHII", 0x80, 0, sequence % 65536, timestamp % (2**32), ssrc) + pcmu, media_target)
                report["rtp_sent"] += 1
                sequence += 1
                timestamp += 160
                next_packet = now + 0.020
            readable, _, _ = select.select([sip, rtp], [], [], 0.01)
            for ready in readable:
                packet = ready.recv(65535)
                if ready is rtp:
                    if len(packet) >= 12 and (packet[1] & 0x7F) == 0:
                        report["rtp_received"] += 1
                        if any(value not in (0xFF, 0x7F) for value in packet[12:]):
                            report["non_silent_rtp_received"] += 1
                    continue
                first, headers, body = packet_parts(packet)
                if first.startswith("BYE "):
                    ok_response(headers)
                    report["ended"] = True
                    break
                if not first.startswith("SIP/2.0 "):
                    continue
                status = int(first.split()[1])
                cseq_method = headers.get("cseq", "").split()[-1:]
                if status == 200 and cseq_method == ["INVITE"]:
                    to_header = headers.get("to", to_header)
                    match = re.search(r"<([^>]+)>", headers.get("contact", ""))
                    if match:
                        contact_uri = match.group(1)
                    request("ACK", transaction_branch="z9hG4bK" + secrets.token_hex(10))
                    if not report["answered"]:
                        report["answered"] = True
                        answered_at = time.monotonic()
                        report["answer_after_ms"] = round((answered_at - start) * 1000)
                        ip = re.search(r"^c=IN IP4 ([^\r\n]+)", body, re.M)
                        port = re.search(r"^m=audio ([0-9]+)", body, re.M)
                        if not ip or not port or not ipaddress.ip_address(ip.group(1)).is_private:
                            report["failure"] = "missing or non-private SDP media target"
                            break
                        media_target = (ip.group(1), int(port.group(1)))
                elif status == 487 and cseq_method == ["INVITE"] and report["cancel_sent"]:
                    to_header = headers.get("to", to_header)
                    request("ACK")
                    report["cancelled"] = report["ended"] = True
                    break
                elif status == 200 and cseq_method == ["BYE"]:
                    report["ended"] = True
                    break
                elif status >= 300 and cseq_method == ["INVITE"]:
                    to_header = headers.get("to", to_header)
                    request("ACK")
                    report["failure"] = f"SIP INVITE rejected with {status}"
                    break
            if report["ended"] or "failure" in report:
                break
        if not report["ended"] and "failure" not in report:
            report["failure"] = "bounded SIP fixture deadline"
    finally:
        sip.close()
        rtp.close()
    print(json.dumps(report, sort_keys=True))
    if "failure" in report or (args.cancel_after_ms < 0 and (not report["answered"] or report["non_silent_rtp_received"] == 0)):
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
