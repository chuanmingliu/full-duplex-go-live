#!/usr/bin/env python3
"""Dispatch consented CRM calls due in the current Shanghai minute, on the configured line."""
import datetime as dt
import fcntl
import hashlib
import json
import os
from pathlib import Path
import re
import sqlite3
import sys
import tempfile
import urllib.request

SHANGHAI = dt.timezone(dt.timedelta(hours=8))
CRM = "http://127.0.0.1:8791"
ENGINE = "http://127.0.0.1:8765"
ORIGINATOR = "http://127.0.0.1:3000"


def http(url, token=None, body=None):
    headers = {"Authorization": "Bearer " + token} if token else {}
    data = None
    if body is not None:
        headers["Content-Type"] = "application/json"
        data = json.dumps(body).encode()
    req = urllib.request.Request(url, data=data, headers=headers)
    with urllib.request.urlopen(req, timeout=45 if url.endswith("/dial") else 5) as resp:
        return json.load(resp)


def recipient(home):
    for line in (home / "aria/.env").read_text().splitlines():
        if line.startswith("ALLOWED_CALLEE="):
            number = line.partition("=")[2].strip().strip("\"'")
            if re.fullmatch(r"\+?[0-9]{7,15}", number):
                return number
    raise ValueError("no configured outbound recipient")


def phone_key(number):
    if re.fullmatch(r"(?:\+?86)?1[3-9]\d{9}", number):
        return "+86" + number[-11:]
    return number


def recipients(home, policy=Path("/etc/golive/outbound-recipients.env")):
    default = recipient(home)
    numbers = {phone_key(default): default}
    path = home / ".config/golive/scheduled-recipients.json"
    if policy.exists() or path.exists():
        if policy.exists():
            key, sep, text = policy.read_text().strip().partition("=")
            if key != "GOLIVE_EXTRA_CALLEES" or not sep:
                raise ValueError("invalid recipient policy")
            extra = text.split(",") if text else []
        else:
            extra = json.loads(path.read_text())
        if not isinstance(extra, list) or not all(isinstance(n, str) and re.fullmatch(r"\+861[3-9][0-9]{9}", n) for n in extra):
            raise ValueError("invalid scheduled recipient whitelist")
        for number in extra:
            numbers.setdefault(number, number)
    return numbers


def due(lead, minute):
    plan = lead.get("call_plan") or {}
    return (not lead.get("deleted") and not lead.get("do_not_call")
            and lead.get("outbound_consent") is True and "schedule" in lead.get("tags", [])
            and plan.get("timezone") == "Asia/Shanghai" and plan.get("time") == minute.strftime("%H:%M")
            and minute.isoweekday() % 7 in plan.get("days", []))


def now():
    return dt.datetime.now(SHANGHAI).replace(second=0, microsecond=0)


def dispatch(home, db, request=http, clock=now):
    minute = clock()
    slot = minute.isoformat()
    if db.execute("SELECT 1 FROM attempts WHERE slot=?", (slot,)).fetchone():
        return
    token = (home / "golive-crm/api.token").read_text().strip()
    def leads():
        return request(CRM + "/api/leads?tag=schedule", token)["leads"]

    allowed = recipients(home)
    for lead in leads():
        if not due(lead, minute):
            continue
        to = allowed.get(phone_key(lead["values"]["phone"]))
        if to is None:
            print("skipped: recipient is not on the whitelist", slot, lead["id"])
            continue
        saved = json.loads((home / ".config/golive/aria-call-defaults.json").read_text())
        template = saved.get("crm_template", "car-renewal")
        preview = request(CRM + "/api/call", token, {
            "template": template, "vars": saved.get("vars", {}), "dry_run": True})
        if preview.get("dry_run") is not True or not preview.get("ok") or preview["render"]["template"] != template:
            raise ValueError("could not preview saved call template")
        health = request(ENGINE + "/healthz")
        binding = request(ORIGINATOR + "/api/realtime/brain")
        if health.get("status") != "ok" or health.get("sessions") != 0:
            print("line busy or unavailable", slot)
            return
        if binding.get("backend") != "local" or binding.get("endpoint") != "ws://127.0.0.1:8765/v1/realtime":
            raise ValueError("original GoLive route is unavailable")
        current = next((r for r in leads() if r["id"] == lead["id"]), None)
        if (not current or not due(current, minute) or phone_key(current["values"]["phone"]) != phone_key(to)
                or recipients(home).get(phone_key(to)) != to or clock() != minute):
            return
        purpose = current["values"]["purpose"]
        instructions = preview["render"]["instructions"]
        if purpose in ("follow-up", "reminder", "survey"):
            content = request(CRM + "/api/call-purposes/" + purpose, token)["content"].strip()
            if content:
                instructions += "\n\nBusiness content for this " + purpose + " call:\n" + content
        elif purpose != "normal":
            raise ValueError("unknown call purpose")
        if clock() != minute:
            return
        body = {"to": to, "instructions": "[golive:prompt-source=crm:v1]\n" + instructions,
                "greeting": preview["greeting"]}
        # The originator has one locked phone line. Commit before the POST so a
        # timeout, restart or another worker cannot repeat an uncertain attempt.
        with db:
            claimed = db.execute("INSERT OR IGNORE INTO attempts(slot,lead,state) VALUES(?,?,'pending')",
                                 (slot, lead["id"])).rowcount
        if not claimed:
            return
        result = request(ORIGINATOR + "/api/outbound/dial", body=body)
        if result.get("ok") is not True or not result.get("call_id"):
            raise ValueError("dial was not confirmed")
        with db:
            db.execute("UPDATE attempts SET state='accepted', call_id=? WHERE slot=?", (result["call_id"], slot))
        print("accepted", slot, lead["id"], result["call_id"])
        return


def publish_call_owners(home, db):
    """Mirror confirmed dial receipts for the CRM hangup worker, including after restart."""
    db.execute("CREATE TABLE IF NOT EXISTS published_owners(slot TEXT PRIMARY KEY)")
    rows = db.execute("SELECT slot, lead, call_id FROM attempts WHERE state='accepted' "
                      "AND slot NOT IN (SELECT slot FROM published_owners)").fetchall()
    directory = home / "golive-crm/scheduled-owners"
    for slot, lead, call_id in rows:
        directory.mkdir(mode=0o700, exist_ok=True)
        destination = directory / hashlib.sha256(call_id.encode()).hexdigest()
        with tempfile.NamedTemporaryFile(dir=directory, delete=False) as tmp:
            tmp.write(lead.encode())
            tmp.flush()
            os.fsync(tmp.fileno())
        try:
            try:
                os.link(tmp.name, destination)
            except FileExistsError:
                if destination.read_text() != lead:
                    raise ValueError("conflicting owner for accepted call")
            with db:
                db.execute("INSERT OR IGNORE INTO published_owners VALUES(?)", (slot,))
        finally:
            os.unlink(tmp.name)


def run():
    os.umask(0o077)
    home = Path.home()
    lockpath = home / "golive-telephony/codex-latency/mcp/call.lock"
    with lockpath.open("a") as lock:
        try:
            fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError:
            return
        with sqlite3.connect(home / "golive-crm/scheduled-calls.sqlite") as db:
            db.execute("CREATE TABLE IF NOT EXISTS attempts(slot TEXT PRIMARY KEY, lead TEXT NOT NULL, state TEXT NOT NULL, call_id TEXT)")
            try:
                dispatch(home, db)
            finally:
                publish_call_owners(home, db)


if __name__ == "__main__":
    try:
        run()
    except Exception as err:
        sys.exit("Scheduled call check failed: " + type(err).__name__)
