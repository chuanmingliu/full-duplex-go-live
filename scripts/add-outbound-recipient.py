#!/usr/bin/env python3
"""ThinkPad admin operation: add one exact Chinese mobile destination end to end."""
import argparse
import contextlib
import fcntl
import json
import os
from pathlib import Path
import pwd
import re
import shutil
import subprocess
import tempfile
import time
import tomllib
import urllib.request

BOX = Path("/home/chuanming")
ARIA = BOX / "aria"
PBX = BOX / "telephony/rustpbx/config.toml"
UNIT = Path("/etc/systemd/system/aria.service.d/90-outbound-recipients.conf")
POLICY = Path("/etc/golive/outbound-recipients.env")
ENV_KEY = "GOLIVE_EXTRA_CALLEES="
MOBILE = re.compile(r"\+861[3-9][0-9]{9}")
EXTRAS = '(typeof process !== "undefined" ? (process.env.GOLIVE_EXTRA_CALLEES || "").split(",").filter(Boolean) : [])'


def api(url, body=None, token=None):
    headers = {"Content-Type": "application/json"}
    if token:
        headers["Authorization"] = "Bearer " + token
    data = None if body is None else json.dumps(body).encode()
    with urllib.request.urlopen(urllib.request.Request(url, data=data, headers=headers), timeout=5) as resp:
        return json.load(resp)


def replace_one(s, old, new):
    if s.count(old) != 1:
        raise ValueError("installed code differs from the expected Aria release")
    return s.replace(old, new)


def extend_dial(s):
    if "GOLIVE_EXTRA_CALLEES" in s[s.index("function isAllowedCallee"):s.index("function denyCalleeMessage")]:
        return s
    for value in ("parsed.e164", "trimmed"):
        original = "matchesChinaNumber(" + value + ", ALLOWED_CALLEE)"
        s = replace_one(s, "return " + original + ";", "return " + original + " || " + EXTRAS
                        + ".some(n => matchesChinaNumber(" + value + ", n));")
    return s


def extend_routing(s):
    if "const extraOutbound =" in s:
        return s
    guard = re.search(r'if \(direction === "out" && !matchesChinaNumber\(sipUserOf\(to\), (?:ALLOWED_CALLEE|"\+\d+")\)\)', s)
    if not guard:
        raise ValueError("outbound dialplan guard not recognized")
    old = guard.group()
    s = replace_one(s, old, 'const extraOutbound = direction === "out" && ' + EXTRAS +
                    '.find(n => matchesChinaNumber(sipUserOf(to), n));\n  ' + old[:-1] + ' && !extraOutbound)')
    old = "const rule = table.rules.find((r) => ruleMatches(r, ctx));"
    return replace_one(s, old, '''const trial = table.rules.find((r) => r.id === "trial-out" && r.enabled && r.action === "pstn");
  const rule = extraOutbound && trial ? { ...trial, id: "whitelist-out", target: "sip:" + extraOutbound.slice(3) + "@" + table.gateway, rewriteTo: extraOutbound.slice(3) } : table.rules.find((r) => ruleMatches(r, ctx));''')


def extend_pbx(s, number, default):
    rules = tomllib.loads(s)["proxy"]["routes"]
    name = "outbound-to-" + default
    newname = "outbound-to-" + number
    base = next(r for r in rules if r["name"] == name)
    if base["dest"] != "cellular_gw" or base["rewrite"]["to.user"] != default or base["match"]["from.user"] != "^1001$":
        raise ValueError("unexpected PBX outbound route")
    expected = json.loads(json.dumps(base))
    expected["name"] = newname
    expected["match"]["to.user"] = expected["match"]["to.user"].replace(default, number)
    expected["rewrite"]["to.user"] = number
    for rule in rules:
        if re.fullmatch(rule.get("match", {}).get("to.user", "(?!)"), number):
            if rule != expected:
                raise ValueError("new number overlaps an existing PBX rule")
            return s
    stanza = next(block for block in s.split("[[proxy.routes]]")[1:] if 'name = "' + name + '"' in block)
    stanza = re.split(r'^\[(?!proxy\.routes\.)', stanza, maxsplit=1, flags=re.M)[0]
    # Preserve the gateway identity and every other rewrite; update only the destination.
    lines = []
    for line in stanza.splitlines():
        if line.lstrip().startswith(("name =", '"to.user" =')):
            line = line.replace(default, number)
        lines.append(line)
    updated = s.rstrip() + "\n\n[[proxy.routes]]\n" + "\n".join(lines).strip() + "\n"
    if tomllib.loads(updated)["proxy"]["routes"] != rules + [expected]:
        raise ValueError("PBX addition changed another rule")
    return updated


def install(path, data, owner=None):
    path.parent.mkdir(parents=True, exist_ok=True)
    st = path.stat() if path.exists() else None
    with tempfile.NamedTemporaryFile(dir=path.parent, delete=False) as tmp:
        tmp.write(data.encode())
        tmp.flush()
        os.fchmod(tmp.fileno(), st.st_mode & 0o777 if st else 0o600)
        if st or owner:
            os.fchown(tmp.fileno(), *(owner or (st.st_uid, st.st_gid)))
        os.fsync(tmp.fileno())
    os.replace(tmp.name, path)


def mobile(raw):
    if not isinstance(raw, str):
        return None
    match = re.fullmatch(r"(?:\+?86)?(1[3-9][0-9]{9})", raw)
    return "+86" + match[1] if match else None


def scheduled_numbers(leads):
    numbers = set()
    for lead in leads:
        plan = lead.get("call_plan") or {}
        if (lead.get("deleted") or lead.get("do_not_call") or lead.get("outbound_consent") is not True
                or "schedule" not in lead.get("tags", []) or plan.get("timezone") != "Asia/Shanghai"
                or not re.fullmatch(r"(?:[01]\d|2[0-3]):[0-5]\d", plan.get("time", ""))
                or not plan.get("days") or not all(type(d) is int and 0 <= d <= 6 for d in plan["days"])):
            continue
        number = mobile(lead.get("values", {}).get("phone"))
        if number:
            numbers.add(number)
    return numbers


def configured_numbers():
    if POLICY.exists():
        text = POLICY.read_text().strip()
    elif UNIT.exists():
        legacy = re.fullmatch(r'\[Service\]\s+Environment="(GOLIVE_EXTRA_CALLEES=[^"]*)"\s*', UNIT.read_text())
        if not legacy:
            raise ValueError("unrecognized Aria whitelist configuration")
        text = legacy[1]
    else:
        return set()
    if not text.startswith(ENV_KEY):
        raise ValueError("unrecognized whitelist")
    raw = text[len(ENV_KEY):]
    values = set(raw.split(",")) if raw else set()
    if not all(MOBILE.fullmatch(v) for v in values):
        raise ValueError("invalid existing whitelist")
    return values


def preview(number):
    return api("http://127.0.0.1:3000/api/dialplan", {"direction":"out","to":number,"from":"1001"})


def apply_numbers(requested, prepare=False):
    existing = configured_numbers()
    default = re.search(r'^ALLOWED_CALLEE=["\']?(\+861\d{10})["\']?\s*$', (ARIA/".env").read_text(), re.M).group(1)
    additions = sorted(requested - existing - {default})
    if not prepare and not additions:
        return
    if api("http://127.0.0.1:8765/healthz").get("sessions") != 0:
        raise BlockingIOError("an active call is using the line")
    prior_default = preview(default)
    files = {}
    if prepare:
        src = ARIA/"src/lib/sip"
        bundle = next(p for p in (ARIA/".output/server/_ssr").glob("router-*.mjs") if "function isAllowedCallee(" in p.read_text())
        files.update({src/"dial.ts": extend_dial((src/"dial.ts").read_text()),
                      src/"routing.ts": extend_routing((src/"routing.ts").read_text()),
                      bundle: extend_routing(extend_dial(bundle.read_text())),
                      UNIT: "[Service]\nEnvironmentFile=" + str(POLICY) + "\n"})
    expected = sorted(existing | set(additions))
    cfg = PBX.read_text()
    for number in expected:
        cfg = extend_pbx(cfg, number[3:], default[3:])
    files[PBX] = cfg
    files[POLICY] = ENV_KEY + ",".join(expected) + "\n"
    old = {p:p.read_text() if p.exists() else None for p in files}
    changes = [p for p in files if old[p] != files[p]]
    if not changes:
        return
    backup = Path(tempfile.mkdtemp(prefix="golive-registration-",dir="/var/backups"))
    account = pwd.getpwnam("chuanming")
    def write(p, value):
        if p == POLICY:
            install(p, value, (0, account.pw_gid))
            p.chmod(0o640)
        else:
            install(p, value)
    def reload_services():
        if UNIT in changes:
            subprocess.run(["systemctl","daemon-reload"],check=True)
        subprocess.run(["systemctl","restart","aria.service"],check=True)
        if PBX in changes:
            subprocess.run(["docker","restart","rustpbx"],check=True,stdout=subprocess.DEVNULL)
    for p in changes:
        if old[p] is not None:
            dest = backup/p.relative_to("/")
            dest.parent.mkdir(parents=True,exist_ok=True)
            shutil.copy2(p,dest)
    try:
        for p in changes:
            write(p,files[p])
        if prepare:
            subprocess.run(["node","--check",bundle],check=True)
        reload_services()
        live = subprocess.check_output(["docker","exec","rustpbx","cat","/app/config.toml"],text=True)
        if live != cfg:
            raise RuntimeError("PBX did not mount the updated routes")
        for attempt in range(30):
            try:
                if preview(default) == prior_default:
                    break
            except OSError:
                pass
            time.sleep(1)
        else:
            raise RuntimeError("default route verification failed")
        for number in expected:
            result = preview(number)
            if result.get("action") != "forward" or not any("sip:"+number[3:]+"@" in t for t in result.get("targets", [])):
                raise RuntimeError("new route verification failed")
    except BaseException:
        for p in reversed(changes):
            if old[p] is None:
                p.unlink(missing_ok=True)
            else:
                write(p,old[p])
        reload_services()
        raise
    print("Registered", len(additions), "new recipients; verified routing without a call.")


QUEUE = BOX / "golive-crm/registration-queue"


def process_saved_leads(queue, fetch, apply):
    # Snapshot unique save tickets. Tickets created during this run stay in the
    # directory, so systemd activates the service again after it finishes.
    tickets = list(queue.glob("*.job"))
    if not tickets:
        return
    versions = set()
    for ticket in tickets:
        match = re.fullmatch(r"([a-zA-Z0-9_-]{1,64})\.([0-9]+)\.[0-9]+\.job", ticket.name)
        if match:
            versions.add((match[1], int(match[2])))
    leads = [lead for lead in fetch() if (lead["id"], lead["version"]) in versions]
    apply(scheduled_numbers(leads))
    for ticket in tickets:
        ticket.unlink(missing_ok=True)


def main():
    ap = argparse.ArgumentParser(description=__doc__)
    group = ap.add_mutually_exclusive_group(required=True)
    group.add_argument("--saved-leads", action="store_true", help="process durable lead-save events")
    group.add_argument("--number")
    group.add_argument("--lead")
    group.add_argument("--sync-scheduled", action="store_true")
    ap.add_argument("--prepare", action="store_true", help="install Aria whitelist support")
    ap.add_argument("--ending", help="require this last four when selecting a lead")
    ap.add_argument("--check", action="store_true", help="preview pending registrations without changes")
    args = ap.parse_args()
    if not args.check and os.geteuid() != 0:
        ap.error("administrator authentication is required")
    def requested():
        if args.number:
            raw = args.number
        else:
            leads = api("http://127.0.0.1:8791/api/leads?tag=schedule" if args.sync_scheduled else
                        "http://127.0.0.1:8791/api/leads",token=(BOX/"golive-crm/api.token").read_text().strip())["leads"]
            if args.sync_scheduled:
                return scheduled_numbers(leads)
            raw = next(l["values"]["phone"] for l in leads if l["id"] == args.lead)
        number = mobile(raw)
        if not number or (args.ending and not number.endswith(args.ending)):
            ap.error("unexpected recipient")
        return {number}
    if args.check:
        pending = requested() - configured_numbers()
        default = re.search(r'^ALLOWED_CALLEE=["\']?(\+861\d{10})["\']?\s*$', (ARIA/".env").read_text(), re.M).group(1)
        print("Pending recipient endings:", ", ".join(n[-4:] for n in sorted(pending-{default})) or "none")
        return
    try:
        with contextlib.ExitStack() as locks:
            for name in (".codex-deploy.lock", "codex-latency/mcp/call.lock"):
                fcntl.flock(locks.enter_context((BOX/"golive-telephony"/name).open("r")).fileno(), fcntl.LOCK_EX|fcntl.LOCK_NB)
            if args.saved_leads:
                def fetch():
                    token = (BOX/"golive-crm/api.token").read_text().strip()
                    return api("http://127.0.0.1:8791/api/leads?tag=schedule", token=token)["leads"]
                process_saved_leads(QUEUE, fetch, apply_numbers)
            else:
                apply_numbers(requested(),args.prepare or not args.sync_scheduled)
    except BlockingIOError:
        if not args.sync_scheduled or args.prepare:
            raise
        print("Registration deferred until the next check.")


if __name__ == "__main__":
    main()
