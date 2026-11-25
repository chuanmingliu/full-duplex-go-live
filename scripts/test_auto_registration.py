import copy
import importlib.util
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location("registration", Path(__file__).with_name("add-outbound-recipient.py"))
registration = importlib.util.module_from_spec(spec)
spec.loader.exec_module(registration)


class EnrollmentTest(unittest.TestCase):
    def setUp(self):
        self.lead = {"tags":["schedule"], "outbound_consent":True, "values":{"phone":"13800000001"},
                     "call_plan":{"time":"11:15", "days":[3], "timezone":"Asia/Shanghai"}}

    def test_selects_consented_schedules_and_deduplicates_forms(self):
        rows = [self.lead]
        for phone in ["+8613800000001", "8613800000001", "13900000002", "bad"]:
            row = copy.deepcopy(self.lead)
            row["values"]["phone"] = phone
            rows.append(row)
        for key,value in [("outbound_consent",False),("deleted",True),("do_not_call",True),("tags",[]),("call_plan",{})]:
            row = copy.deepcopy(self.lead)
            row["values"]["phone"] = "13700000003"
            row[key] = value
            rows.append(row)
        self.assertEqual(registration.scheduled_numbers(rows), {"+8613800000001","+8613900000002"})

    def test_already_registered_and_default_need_no_service_changes(self):
        with tempfile.TemporaryDirectory() as tmp, patch.object(registration, "ARIA", Path(tmp)), patch.object(
                registration,"configured_numbers",return_value={"+8613800000001"}), patch.object(registration,"api") as api:
            (Path(tmp)/".env").write_text("ALLOWED_CALLEE=+8613900000002\n")
            registration.apply_numbers({"+8613800000001","+8613900000002"})
            api.assert_not_called()

    def test_idle_worker_does_not_read_crm(self):
        with tempfile.TemporaryDirectory() as tmp:
            registration.process_saved_leads(Path(tmp), self.fail, self.fail)

    def test_save_queue_recovers_and_skips_replaced_versions(self):
        with tempfile.TemporaryDirectory() as tmp:
            queue = Path(tmp)
            old = queue / "sample.1.111.job"
            current = queue / "sample.2.222.job"
            old.touch()
            current.touch()
            leads = [dict(self.lead, id="sample", version=2), dict(self.lead, id="other", version=1)]
            leads[1]["values"] = {"phone": "13900000002"}
            def busy(numbers):
                self.assertEqual(numbers, {"+8613800000001"})
                raise BlockingIOError()
            with self.assertRaises(BlockingIOError):
                registration.process_saved_leads(queue, lambda: leads, busy)
            self.assertTrue(current.exists())
            def apply(numbers):
                self.assertEqual(numbers, {"+8613800000001"})
                (queue / "other.1.333.job").touch()
            registration.process_saved_leads(queue, lambda: leads, apply)
            self.assertEqual([p.name for p in queue.iterdir()], ["other.1.333.job"])
            leads[1]["outbound_consent"] = False
            registration.process_saved_leads(queue, lambda: leads, lambda nums: self.assertEqual(nums, set()))
            self.assertEqual(list(queue.iterdir()), [])
