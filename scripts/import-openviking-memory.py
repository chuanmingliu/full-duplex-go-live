#!/usr/bin/env python3
"""Copy one OpenViking memory file into the same user's CRM revision history."""
import argparse
import json
import os
import re
import sys
from urllib.error import HTTPError, URLError
from urllib.parse import urlencode
from urllib.request import HTTPRedirectHandler, Request, build_opener


class NoRedirect(HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return None


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("lead", help="CRM lead ID, identical to the OpenViking user ID")
    parser.add_argument("key", help="CRM memory key")
    parser.add_argument("file", help="file relative to the user's memories directory")
    parser.add_argument("--previous-seq", type=int, required=True)
    args = parser.parse_args()
    segment = r"[A-Za-z0-9_-]{1,64}"
    if not all(re.fullmatch(segment, item) for item in (args.lead, args.key)):
        parser.error("invalid lead or memory key")
    if not all(re.fullmatch(r"[\w-][\w.-]*", part) for part in args.file.split("/")):
        parser.error("expected a relative memory file path")

    uri = f"viking://user/{args.lead}/memories/{args.file}"
    crm = os.environ["CRM_URL"].rstrip("/")
    ov = os.environ["OPENVIKING_URL"].rstrip("/")
    client = build_opener(NoRedirect)
    read = Request(f"{ov}/api/v1/content/read?{urlencode({'uri': uri})}",
                   headers={"X-API-Key": os.environ["OPENVIKING_API_KEY"]})
    with client.open(read, timeout=15) as response:
        result = json.load(response)
    value = result.get("result")
    if result.get("status") != "ok" or not isinstance(value, str) or not value.strip():
        raise ValueError("OpenViking returned no memory text")
    body = {"value": value, "source": uri, "previous_seq": args.previous_seq}
    write = Request(f"{crm}/api/leads/{args.lead}/memory/{args.key}", method="PUT",
                    data=json.dumps(body).encode(),
                    headers={"Content-Type": "application/json", "X-Operator": "openviking-import",
                             "Authorization": "Bearer " + os.environ["CRM_TOKEN"]})
    with client.open(write, timeout=15) as response:
        revision = json.load(response)
    print(f"{args.lead}/{args.key} revision {revision['seq']}")


if __name__ == "__main__":
    try:
        main()
    except HTTPError as exc:
        sys.exit(f"Memory import failed with HTTP {exc.code} at {exc.url.split('?')[0]}")
    except (KeyError, URLError, ValueError, TimeoutError) as exc:
        sys.exit(f"Memory import failed: {exc}")
