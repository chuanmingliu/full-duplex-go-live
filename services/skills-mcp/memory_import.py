"""Import one completed GoLive journal call.json; never dial or release a call.

The original UTF-8 JSON and checksum remain local. Only projected evidence is
eligible for a configured dream provider. Uses Python's standard library only.
"""
import argparse
import hashlib
import ipaddress
import json
from datetime import datetime, timezone
from pathlib import Path
from urllib.parse import urlsplit
from urllib.request import HTTPRedirectHandler, Request, build_opener


def episode_from_archive(raw, mode, audio_refs=()):
    call = json.loads(raw)
    ended = datetime.fromisoformat(call["ended_at"].replace("Z", "+00:00"))
    if ended.year == 1 or ended.tzinfo is None or ended > datetime.now(timezone.utc):
        raise ValueError("archive must be a completed call")
    evidence = []
    for i, line in enumerate(call["transcript"]):
        kind = {"caller": "user", "assistant": "assistant"}[line["who"]]
        evidence.append({"id": f"transcript-{i}", "kind": kind,
                         "text": json.dumps(line, ensure_ascii=False)})
    for field, kind in (("turns", "timing"), ("quotation", "tool"),
                        ("warnings", "timing")):
        for i, item in enumerate(call.get(field) or []):
            evidence.append({"id": f"{field}-{i}", "kind": kind,
                             "text": json.dumps(item, ensure_ascii=False)})
    if not evidence:
        raise ValueError("archive has no evidence")
    return {"id": call["id"], "mode": mode, "closed_at": call["ended_at"],
            "evidence": evidence, "audio_refs": list(audio_refs),
            "archive_json": raw, "archive_sha256": hashlib.sha256(raw.encode()).hexdigest()}


class NoRedirect(HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return None


def import_episode(base, token_file, episode):
    url = urlsplit(base)
    try:
        local = ipaddress.ip_address(url.hostname).is_loopback
    except ValueError:
        local = False
    if (url.scheme != "https" and not (url.scheme == "http" and local)) or not url.hostname or url.username or url.password or url.query or url.fragment or url.path not in ("", "/"):
        raise ValueError("base URL must be HTTPS or loopback HTTP, without credentials or path")
    token = Path(token_file).read_text().strip()
    if not token:
        raise ValueError("empty admin token")
    req = Request(base.rstrip("/") + "/admin/memory/episodes",
                  data=json.dumps(episode, ensure_ascii=False).encode(),
                  headers={"Authorization": "Bearer " + token, "Content-Type": "application/json"})
    with build_opener(NoRedirect).open(req, timeout=15) as response:
        return json.load(response)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("call_json", type=Path)
    parser.add_argument("--mode", required=True, choices=("real", "simulation"))
    parser.add_argument("--audio-ref", action="append", default=[])
    parser.add_argument("--base", default="http://127.0.0.1:8794")
    parser.add_argument("--token-file", required=True, type=Path)
    args = parser.parse_args()
    # Decode bytes directly so the stored checksum covers original newline bytes.
    raw = args.call_json.read_bytes().decode("utf-8")
    result = import_episode(args.base, args.token_file,
                            episode_from_archive(raw, args.mode, args.audio_ref))
    print(json.dumps({"imported": result["id"], "released": args.mode == "simulation"}))


if __name__ == "__main__":
    main()
