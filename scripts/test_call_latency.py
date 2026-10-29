"""Acceptance checks for the public latency command, using synthetic timings."""
import json
from pathlib import Path
import subprocess
import tempfile
import unittest


SCRIPT = Path(__file__).with_name("call-latency.sh")


def turn(index, **changes):
    row = dict(time="2026-10-29T20:00:00+08:00", level="INFO",
               msg="turn: response latency", session="sess_test", turn=f"item_{index}",
               response_to_speech=True, first_audio_present=True,
               first_content_present=True, opened=True,
               first_audio_out_ms=0, first_content_ms=500, opener_ms=460,
               end_silence_ms=380, asr_final_ms=100, turn_start_ms=-100,
               llm_first_token_ms=300, first_segment_ms=400,
               tts_first_audio_ms=500, speculative=True, truncated=False)
    row.update(changes)
    return row


class CallLatencyTests(unittest.TestCase):
    def run_report(self, rows, *flags, text=False):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "call.log"
            if text:
                lines = [" ".join(f"{key}={json.dumps(value, ensure_ascii=False)}"
                                  for key, value in row.items()) for row in rows]
            else:
                lines = [json.dumps(row) for row in rows]
            path.write_text("\n".join(lines) + "\n")
            result = subprocess.run(["bash", str(SCRIPT), str(path), "--json", *flags],
                                    text=True, capture_output=True)
        self.assertNotEqual(result.returncode, 2, result.stderr)
        return result.returncode, json.loads(result.stdout)

    def test_cached_audio_does_not_hide_slow_content(self):
        rows = [turn(i, first_content_ms=500 if i < 23 else 1020) for i in range(25)]
        code, report = self.run_report(rows, "--gate", "--metric", "content")
        self.assertEqual(code, 1)
        self.assertEqual(report["first_audio"]["p95_ms"], 380)
        self.assertEqual(report["first_content"]["p95_ms"], 1400)
        self.assertEqual(self.run_report(rows, "--gate")[0], 0)

    def test_target_is_strict_and_non_gate_remains_a_report(self):
        rows = [turn(i, first_content_ms=820) for i in range(20)]
        self.assertEqual(self.run_report(rows, "--gate", "--metric", "content")[0], 1)
        self.assertEqual(self.run_report(rows)[0], 0)
        for row in rows:
            row["first_content_ms"] = 819
        self.assertEqual(self.run_report(rows, "--gate", "--metric", "content")[0], 0)
        for row in rows:
            row.update(opened=False, opener_ms=0, first_audio_out_ms=820, first_content_ms=820)
        self.assertEqual(self.run_report(rows, "--gate")[0], 1)

    def test_no_opener_slow_turns_and_interrupted_answers_are_counted(self):
        rows = [turn(i) for i in range(23)]
        for i in (23, 24):
            row = turn(i, opened=False, opener_ms=0, first_audio_out_ms=1020,
                       truncated=True)
            del row["first_content_ms"]
            rows.append(row)
        code, report = self.run_report(rows, "--gate")
        self.assertEqual(code, 1)
        self.assertEqual(report["first_content"]["n"], 25)
        self.assertEqual(report["first_content"]["p95_ms"], 1400)

    def test_cancelled_opener_only_is_not_an_answer(self):
        rows = [turn(i) for i in range(20)]
        rows.append(turn(20, first_content_present=False, truncated=True,
                         first_content_ms=460))
        code, report = self.run_report(rows, "--gate")
        self.assertEqual(code, 0)
        self.assertEqual(report["counts"]["cancelled"], 1)
        self.assertEqual(report["counts"]["answered"], 20)

    def test_missing_audio_cannot_become_zero_ms_success(self):
        rows = [turn(i) for i in range(20)]
        rows.append(turn(20, first_audio_present=False, first_content_present=False))
        code, report = self.run_report(rows, "--gate")
        self.assertEqual(code, 1)
        self.assertEqual(report["counts"]["missing"], 1)

    def test_missing_content_is_not_replaced_by_cached_opener(self):
        rows = [turn(i) for i in range(20)]
        del rows[-1]["first_content_ms"]
        code, report = self.run_report(rows, "--gate")
        self.assertEqual(code, 1)
        self.assertEqual(report["counts"]["missing"], 1)

    def test_empty_greeting_and_tool_result_inputs_cannot_pass(self):
        for rows in ([], [turn(1, response_to_speech=False)],
                     [turn(1, response_to_speech=False, llm_first_token_ms=700)]):
            code, report = self.run_report(rows, "--gate")
            self.assertEqual(code, 1)
            self.assertEqual(report["counts"]["answered"], 0)

    def test_text_and_json_have_identical_statistics(self):
        rows = [turn(i, end_silence_ms=280 if i % 2 else 380) for i in range(20)]
        self.assertEqual(self.run_report(rows, "--gate"),
                         self.run_report(rows, "--gate", text=True))

    def test_too_few_samples_fail_and_override_is_explicit(self):
        rows = [turn(0)]
        self.assertEqual(self.run_report(rows, "--gate")[0], 1)
        self.assertEqual(self.run_report(rows, "--gate", "--min-turns", "1")[0], 0)

    def test_legacy_report_preserves_audio_but_cannot_certify_origin(self):
        rows = [turn(i, opener_ms=0, first_audio_out_ms=500) for i in range(20)]
        for row in rows:
            for key in ("response_to_speech", "first_audio_present", "first_content_present",
                        "opened", "first_content_ms", "end_silence_ms"):
                del row[key]
        code, report = self.run_report(rows)
        self.assertEqual(code, 0)
        self.assertEqual(report["first_audio"]["p95_ms"], 880)
        self.assertEqual(report["first_content"]["p95_ms"], 880)
        self.assertEqual(self.run_report(rows, "--gate")[0], 1)

    def test_duplicate_samples_cannot_inflate_sample_count(self):
        code, report = self.run_report([turn(0)] * 20, "--gate")
        self.assertEqual(code, 1)
        self.assertEqual(report["counts"]["answered"], 1)
        self.assertEqual(report["counts"]["duplicates"], 19)

    def test_malformed_content_does_not_use_non_opener_fallback(self):
        rows = [turn(i) for i in range(20)]
        rows[-1].update(opened=False, opener_ms=0, first_content_ms="invalid")
        code, report = self.run_report(rows, "--gate")
        self.assertEqual(code, 1)
        self.assertEqual(report["counts"]["missing"], 1)

    def test_legacy_cancelled_opener_does_not_invent_an_answer(self):
        row = turn(1, truncated=True, first_content_ms=460)
        for key in ("response_to_speech", "first_audio_present", "first_content_present", "opened"):
            del row[key]
        _, report = self.run_report([row])
        self.assertEqual(report["counts"]["answered"], 0)
        self.assertEqual(report["first_content"]["n"], 0)

    def test_legacy_greeting_heuristic_cannot_hide_an_unverified_turn(self):
        rows = [turn(i) for i in range(20)]
        ambiguous = turn(20, asr_final_ms=0, llm_first_token_ms=0,
                         first_audio_out_ms=2000, first_content_ms=2100)
        for key in ("response_to_speech", "first_audio_present", "first_content_present", "opened"):
            del ambiguous[key]
        rows.append(ambiguous)
        code, report = self.run_report(rows, "--gate")
        self.assertEqual(code, 1)
        self.assertEqual(report["counts"]["unverified"], 1)

    def test_historical_journal_fixture_keeps_the_established_audio_numbers(self):
        fixture = SCRIPT.parent.parent / "internal/journal/testdata/call.log"
        result = subprocess.run(["bash", str(SCRIPT), str(fixture), "380", "--json"],
                                text=True, capture_output=True, check=True)
        report = json.loads(result.stdout)
        self.assertEqual(report["first_audio"]["n"], 21)
        self.assertEqual(report["first_audio"]["p50_ms"], 806)
        self.assertEqual(report["first_audio"]["p95_ms"], 1285)

    def test_malformed_latency_record_fails_closed_without_echoing_it(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "call.log"
            path.write_text('{"msg":"turn: response latency", "private":"do-not-print"\n')
            result = subprocess.run(["bash", str(SCRIPT), str(path), "--gate", "--json"],
                                    text=True, capture_output=True)
        self.assertEqual(result.returncode, 1)
        self.assertNotIn("do-not-print", result.stdout + result.stderr)
        self.assertEqual(json.loads(result.stdout)["counts"]["malformed"], 1)


if __name__ == "__main__":
    unittest.main()
