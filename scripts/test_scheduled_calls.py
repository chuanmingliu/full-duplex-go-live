import importlib.util
import json
from pathlib import Path
import sqlite3
import tempfile
import unittest
import datetime as dt

spec = importlib.util.spec_from_file_location("scheduler", Path(__file__).with_name("scheduled-calls.py"))
scheduler = importlib.util.module_from_spec(spec)
spec.loader.exec_module(scheduler)


class ScheduledCallsTest(unittest.TestCase):
    def setUp(self):
        temp = tempfile.TemporaryDirectory()
        self.addCleanup(temp.cleanup)
        self.home = Path(temp.name)
        for path, value in {"aria/.env": 'ALLOWED_CALLEE="+8613800000000"', "golive-crm/api.token": "test",
                            ".config/golive/aria-call-defaults.json": '{"crm_template":"sample"}'}.items():
            file = self.home / path
            file.parent.mkdir(parents=True, exist_ok=True)
            file.write_text(value)
        self.db = sqlite3.connect(self.home / "state")
        self.addCleanup(self.db.close)
        self.db.execute("CREATE TABLE attempts(slot TEXT PRIMARY KEY,lead TEXT,state TEXT,call_id TEXT)")
        self.minute = dt.datetime(2026, 11, 29, 10, 50, tzinfo=scheduler.SHANGHAI)
        self.lead = {"id": "fixture", "values": {"phone": "13800000000", "purpose":"follow-up"}, "tags": ["schedule"],
                     "outbound_consent": True, "call_plan": {"time": "10:50", "timezone": "Asia/Shanghai", "days": [0]}}
        self.calls = []
        self.busy = False
        self.uncertain = False
        self.revoke = False

    def request(self, url, token=None, body=None):
        if url.startswith(scheduler.CRM + "/api/leads"):
            return {"leads": [self.lead]}
        if url.startswith(scheduler.CRM + "/api/call-purposes/"):
            self.assertNotEqual(self.lead["values"]["purpose"], "normal")
            return {"content": "Instructions for " + url.rsplit("/", 1)[1]}
        if url.endswith("/api/call"):
            self.assertTrue(body["dry_run"])
            return {"ok": True, "dry_run": True, "render": {"template": "sample", "instructions": "test"}, "greeting": "hello"}
        if url.endswith("/healthz"):
            return {"status": "ok", "sessions": int(self.busy)}
        if url.endswith("/brain"):
            if self.revoke:
                self.lead["outbound_consent"] = False
            return {"backend": "local", "endpoint": "ws://127.0.0.1:8765/v1/realtime"}
        self.assertEqual(url, scheduler.ORIGINATOR + "/api/outbound/dial")
        if self.lead["values"]["purpose"] == "normal":
            self.assertEqual(body["instructions"], "[golive:prompt-source=crm:v1]\ntest")
            self.assertEqual(body["greeting"], "hello")
        else:
            self.assertIn("Instructions for " + self.lead["values"]["purpose"], body["instructions"])
        self.calls.append(body["to"])
        if self.uncertain:
            raise TimeoutError
        return {"ok": True, "call_id": "synthetic-call"}

    def tick(self):
        scheduler.dispatch(self.home, self.db, self.request, lambda: self.minute)

    def test_due_once_then_next_week(self):
        self.tick()
        self.tick()
        self.assertEqual(self.calls, ["+8613800000000"])
        self.assertEqual(self.db.execute("SELECT call_id FROM attempts").fetchone()[0], "synthetic-call")
        self.minute += dt.timedelta(days=7)
        self.tick()
        self.assertEqual(len(self.calls), 2)

    def test_mismatch_busy_revoked_and_missed(self):
        self.minute -= dt.timedelta(minutes=1)
        self.tick()
        self.minute += dt.timedelta(minutes=2)
        self.tick()
        self.minute -= dt.timedelta(minutes=1)
        self.lead["values"]["phone"] = "13900000000"
        self.tick()
        self.lead["values"]["phone"] = "13800000000"
        self.busy = True
        self.tick()
        self.busy = False
        self.revoke = True
        self.tick()
        self.assertEqual(self.calls, [])

    def test_each_purpose_reaches_the_dial(self):
        for purpose in ("normal", "follow-up", "reminder", "survey"):
            self.lead["values"]["purpose"] = purpose
            self.tick()
            self.minute += dt.timedelta(days=7)
        self.assertEqual(len(self.calls), 4)

    def test_additional_recipient(self):
        self.lead["values"]["phone"] = "13900000000"
        path = self.home / ".config/golive/scheduled-recipients.json"
        path.write_text(json.dumps(["+8613900000000"]))
        self.assertEqual(scheduler.recipient(self.home), "+8613800000000")
        self.tick()
        self.assertEqual(self.calls, ["+8613900000000"])

    def test_uncertain_is_not_retried(self):
        self.uncertain = True
        with self.assertRaises(TimeoutError):
            self.tick()
        self.tick()
        self.assertEqual(len(self.calls), 1)
