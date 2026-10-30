"""The real retro CLI must retain cached openers and show content latency."""
import json
from pathlib import Path
import re
import subprocess
import sys
import tempfile
import unittest


SCRIPT = Path(__file__).with_name("sim-retro.py")
SID = "sess_a1b2"
CALL = "out-test"


def turn(index, **changes):
    row = dict(time="2026-10-29T21:00:00+08:00", level="INFO", session=SID,
               msg="turn: response latency", turn=f"item_{index}",
               response_to_speech=True, first_audio_present=True,
               first_content_present=True, opened=True,
               first_audio_out_ms=0, first_content_ms=500, opener_ms=460,
               end_silence_ms=380, asr_final_ms=100, turn_start_ms=-100,
               llm_first_token_ms=300, first_segment_ms=400,
               tts_first_audio_ms=500, speculative=True, truncated=False)
    row.update(changes)
    return row


class SimRetroTests(unittest.TestCase):
    def retro(self, rows, json_log=False, report=None):
        records = [dict(time="2026-10-29T21:00:00+08:00", level="INFO", session=SID,
                        msg="session started", call=CALL, version="abcdef0"),
                   dict(time="2026-10-29T21:00:00+08:00", level="DEBUG", session=SID,
                        msg="asr: final", text="PRIVATE_TRANSCRIPT_DO_NOT_PRINT")]
        records.extend(rows)
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "golive.log"
            if json_log:
                lines = [json.dumps(row) for row in records]
            else:
                lines = [" ".join(f"{key}={json.dumps(value) if isinstance(value, (str, bool)) else value}"
                                  for key, value in row.items()) for row in records]
                # slog leaves token-like identifiers and timestamps unquoted.
                for key in ("time", "session", "call", "version", "turn"):
                    lines = [line.replace(f'{key}="', f'{key}=') for line in lines]
                    lines = [re.sub(rf'({key}=[^ ]+)"(?= |$)', r'\1', line) for line in lines]
            path.write_text("\n".join(lines) + "\n")
            md = Path(directory) / "retro.md"
            sim_dir = Path(directory) / "sim"
            command = [sys.executable, "-B", "-I", str(SCRIPT)]
            if report is not None:
                (sim_dir / "data").mkdir(parents=True)
                (sim_dir / "data/dials.jsonl").write_text(json.dumps(dict(call=CALL, run="sim-test")) + "\n")
                (sim_dir / "tokens.env").write_text("SIM_CONTROL_TOKEN=fixture-not-a-real-token\n")
                payload = Path(directory) / "report.json"
                payload.write_text(json.dumps(report))
                shim = Path(directory) / "fixture.py"
                shim.write_text("import io, pathlib, runpy, sys, urllib.request\n"
                                "payload = pathlib.Path(sys.argv.pop(1)).read_bytes()\n"
                                "urllib.request.urlopen = lambda *a, **k: io.BytesIO(payload)\n"
                                "sys.argv.pop(0)\n"
                                "runpy.run_path(sys.argv[0], run_name='__main__')\n")
                command = [sys.executable, "-B", "-I", str(shim), str(payload), str(SCRIPT)]
            result = subprocess.run(command + [CALL, "--log", str(path), "--sim-dir", str(sim_dir),
                                                "--md", str(md)], capture_output=True, text=True)
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertEqual(md.read_text().rstrip(), result.stdout.rstrip())
        self.assertNotIn("PRIVATE_TRANSCRIPT_DO_NOT_PRINT", result.stdout)
        return result.stdout

    def test_zero_ms_openers_and_slow_content_are_both_measured(self):
        rows = [turn(i, first_content_ms=500 if i < 21 else 1020) for i in range(23)]
        rows += [turn(i, opened=False, opener_ms=0, first_audio_out_ms=600,
                      first_content_ms=600) for i in (23, 24)]
        out = self.retro(rows)
        self.assertIn("First audio: **25** measured turns", out)
        self.assertIn("first audio p50 **380**, p95 **980**", out)
        self.assertIn("First content: **25** measured turns", out)
        self.assertIn("first content p50 **880**, p95 **1400**", out)
        self.assertIn("## What the customer's report says", out)
        self.assertIn("## Open", out)

    def test_cancelled_opener_and_unsolicited_turns_are_separate(self):
        out = self.retro([turn(0), turn(1, response_to_speech=False),
                          turn(2, first_content_present=False, truncated=True),
                          turn(3, first_content_ms=900, truncated=True)])
        self.assertIn("First audio: **2** measured turns", out)
        self.assertIn("First content: **2** measured turns", out)
        self.assertIn("non-speech 1; cancelled 1", out)

    def test_pause_is_per_turn_and_console_numbers_do_not_include_it(self):
        out = self.retro([turn(0, first_audio_out_ms=100, end_silence_ms=280),
                          turn(1, first_audio_out_ms=100, end_silence_ms=580)])
        self.assertIn("first audio p50 **530**", out)
        self.assertIn("Console's definition: p50 100, max 100", out)

    def test_legacy_estimates_are_labelled(self):
        row = turn(0)
        for key in ("response_to_speech", "first_audio_present", "first_content_present", "opened"):
            del row[key]
        out = self.retro([row])
        self.assertIn("Legacy/ambiguous records are estimates", out)
        self.assertIn("First audio: **1** measured turns", out)

    def test_json_log_and_text_log_have_same_latency_section(self):
        rows = [turn(0), turn(1, first_content_ms=1200)]
        self.assertEqual(self.retro(rows), self.retro(rows, json_log=True))

    def test_complete_session_retains_strict_tool_handoff(self):
        rows = [turn(1, first_content_present=False, first_content_ms=0),
                dict(time="2026-10-29T21:00:01+08:00", session=SID,
                     msg="tools: delivering a task's result", task="task_1",
                     origin_turn="item_1", turn="item_2", origin_to_result_start_ms=700),
                turn(2, response_to_speech=False, opened=False, opener_ms=0,
                     end_silence_ms=0, first_content_ms=0)]
        out = self.retro(rows)
        self.assertIn("First content: **1** measured turns", out)
        self.assertIn("first content p50 **1080**", out)
        self.assertIn("answered 1; non-speech 1", out)
        self.assertIn("1 linked caller responses; 0 unverified links", out)
        self.assertEqual(out, self.retro(rows, json_log=True))

    def test_report_free_text_is_not_published(self):
        private = "PRIVATE_TRANSCRIPT_DO_NOT_PRINT"
        out = self.retro([turn(0)], report={
            "failures": [private],
            "action_outcomes": [{"id": private, "result": "completed", "reason": private, "at_ms": 12},
                                {"id": private, "result": private, "at_ms": private}],
        })
        self.assertIn("Failures: 1", out)
        self.assertIn("Scripted action observations: 2", out)
        self.assertIn("completed=1", out)
        self.assertIn("unknown=1", out)

    def test_result_after_preamble_is_reported_without_an_extra_caller(self):
        rows = [turn(1, first_content_ms=702),
                dict(time="2026-10-29T21:00:01+08:00", session=SID,
                     msg="tools: delivering a task's result", task="task_1",
                     origin_turn="item_1", turn="item_2", origin_to_result_start_ms=3015),
                turn(2, response_to_speech=False, opened=False, opener_ms=0,
                     end_silence_ms=0, first_audio_out_ms=153, first_content_ms=153)]
        out = self.retro(rows)
        self.assertIn("First content: **1** measured turns", out)
        self.assertIn("first content p50 **1082**", out)
        self.assertIn("Linked tool results: **1** measured results", out)
        self.assertIn("result content p50 **3548**, p95 **3548**", out)
        self.assertIn("answered 1; non-speech 1", out)
        self.assertIn("explanatory lead-in", out)

    def test_receiver_gaps_require_valid_numeric_application_boundary_evidence(self):
        def gap(value, **changes):
            row = dict(valid=True, evidence="application_audio_boundary", value_ms=value)
            row.update(changes)
            return row
        out = self.retro([turn(0)], report={"assistant_response_gaps": [
            gap(900), gap(1100), gap(20000, valid=False), gap(20000, evidence="generation"),
            gap(-10), gap(True), gap(1200.5), gap("30000"),
        ]})
        self.assertIn("2 valid, p50 1000, p95 1100, max 1100 ms", out)


if __name__ == "__main__":
    unittest.main()
