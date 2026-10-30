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


def delivery(origin=1, child=2, **changes):
    row = dict(time="2026-10-29T20:00:01+08:00", level="INFO", session="sess_test",
               msg="tools: delivering a task's result", task="task_1",
               origin_turn=f"item_{origin}", turn=f"item_{child}",
               origin_to_result_start_ms=400)
    row.update(changes)
    return row


def tool_pair(**child_changes):
    origin = turn(1, first_content_present=False, first_content_ms=0)
    child = turn(2, response_to_speech=False, opened=False, opener_ms=0,
                 end_silence_ms=0, first_content_ms=0, turn_start_ms=0)
    child.update(child_changes)
    return origin, child


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

    def test_tool_result_zero_is_joined_once_with_original_audio_and_pause(self):
        origin, child = tool_pair()
        origin.update(first_audio_out_ms=42, end_silence_ms=600)
        rows = [child, delivery(origin_to_result_start_ms=450), origin]
        code, report = self.run_report(rows, "--gate", "--metric", "content", "--min-turns", "1")
        self.assertEqual(code, 0)
        self.assertEqual(report["counts"]["records"], 2)
        self.assertEqual(report["counts"]["non_speech"], 1)
        self.assertEqual(report["counts"]["answered"], 1)
        self.assertEqual(report["counts"]["tool_results_joined"], 1)
        self.assertEqual(report["first_audio"]["p95_ms"], 642)
        self.assertEqual(report["first_content"]["p95_ms"], 1050)
        self.assertEqual(report["first_content"]["n"], 1)
        self.assertEqual(report["tool_result"]["p95_ms"], 1050)
        self.assertEqual(report["tool_result_rows"][0]["end_silence_ms"], 600)
        self.assertEqual(self.run_report(rows), self.run_report(rows, text=True))

    def test_slow_tool_result_cannot_hide_behind_a_fast_filler(self):
        origin, child = tool_pair(first_content_ms=100)
        code, report = self.run_report([origin, delivery(origin_to_result_start_ms=900), child],
                                       "--gate", "--metric", "content", "--min-turns", "1")
        self.assertEqual(code, 1)
        self.assertEqual(report["first_audio"]["p95_ms"], 380)
        self.assertEqual(report["first_content"]["p95_ms"], 1380)

    def test_unplayed_or_ambiguous_child_cannot_fill_missing_content(self):
        cases = [dict(first_content_present=False), dict(first_audio_present=False),
                 dict(first_content_ms="bad"), dict(first_content_ms=-1),
                 dict(first_audio_out_ms=20, first_content_ms=0),
                 dict(response_to_speech=True), dict(end_silence_ms=380),
                 dict(first_content_present=None)]
        for changes in cases:
            with self.subTest(changes=changes):
                origin, child = tool_pair(**changes)
                _, report = self.run_report([origin, delivery(), child])
                self.assertEqual(report["counts"]["tool_results_joined"], 0)
                self.assertGreaterEqual(report["counts"]["missing"], 1)
                self.assertGreaterEqual(report["counts"]["unverified"], 1)
                self.assertEqual(report["tool_result"]["n"], 0)

    def test_interruption_after_result_audio_counts_but_unplayed_result_does_not(self):
        origin, child = tool_pair(truncated=True)
        _, report = self.run_report([origin, delivery(), child])
        self.assertEqual(report["counts"]["answered"], 1)
        self.assertEqual(report["counts"]["interrupted_answers"], 1)
        self.assertTrue(report["tool_result_rows"][0]["interrupted"])
        child["first_content_present"] = False
        _, report = self.run_report([origin, delivery(), child])
        self.assertEqual(report["counts"]["answered"], 0)
        self.assertEqual(report["counts"]["missing"], 1)
        origin["truncated"] = True
        child["first_content_present"] = True
        _, report = self.run_report([origin, delivery(), child])
        self.assertEqual(report["counts"]["tool_results_joined"], 0)
        self.assertEqual(report["counts"]["cancelled"], 1)
        self.assertEqual(report["tool_result"]["n"], 0)

    def test_missing_or_legacy_handoff_never_uses_wallclock_estimates(self):
        origin, child = tool_pair()
        legacy = delivery()
        del legacy["origin_to_result_start_ms"]
        older = dict(legacy)
        del older["origin_turn"]
        for rows in ([origin, child], [origin, delivery()], [origin, legacy, child],
                     [origin, older, child], [delivery(), child]):
            with self.subTest(records=len(rows)):
                code, report = self.run_report(rows, "--gate", "--min-turns", "1")
                self.assertEqual(code, 1)
                self.assertEqual(report["counts"]["tool_results_joined"], 0)
                self.assertEqual(report["first_content"]["n"], 0)

    def test_duplicate_or_conflicting_handoffs_are_not_guessed(self):
        origin, child = tool_pair()
        for links in ([delivery(), delivery()],
                      [delivery(), delivery(origin_to_result_start_ms=401)],
                      [delivery(), delivery(task="task_2")],
                      [delivery(), delivery(origin=3)]):
            with self.subTest(links=links):
                _, report = self.run_report([origin, *links, child])
                self.assertEqual(report["counts"]["tool_results_joined"], 0)
                self.assertEqual(report["counts"]["missing"], 1)
                self.assertGreaterEqual(report["counts"]["invalid_tool_links"], 1)
                self.assertEqual(report["tool_result"]["n"], 0)
        for duplicate in (origin, child):
            _, report = self.run_report([origin, delivery(), child, duplicate])
            self.assertEqual(report["counts"]["tool_results_joined"], 0)
            self.assertEqual(report["counts"]["duplicates"], 1)

    def test_task_identity_is_scoped_to_session(self):
        origin, child = tool_pair(first_content_ms=100)
        other_origin, other_child = tool_pair()
        other_origin.update(session="sess_other", end_silence_ms=280)
        other_child["session"] = "sess_other"
        _, report = self.run_report([origin, child, delivery(), other_origin, other_child,
                                    delivery(session="sess_other", origin_to_result_start_ms=300)])
        self.assertEqual(report["counts"]["tool_results_joined"], 2)
        self.assertEqual(report["counts"]["non_speech"], 2)
        self.assertEqual(report["first_content"]["n"], 2)
        self.assertEqual(report["first_content"]["p50_ms"], 730)
        self.assertEqual(report["counts"]["unverified"], 0)
        self.assertEqual(report["tool_result"]["n"], 2)
        self.assertEqual(report["tool_result"]["p50_ms"], 730)

    def test_existing_caller_content_is_not_replaced_by_later_tool_result(self):
        _, child = tool_pair()
        _, report = self.run_report([turn(1), delivery(origin_to_result_start_ms=5000), child])
        self.assertEqual(report["counts"]["tool_results_joined"], 0)
        self.assertEqual(report["first_content"]["p95_ms"], 880)
        self.assertEqual(report["tool_result"]["p95_ms"], 5380)

    def test_slow_result_fails_content_gate_even_after_fast_direct_content(self):
        # A pre-result sentence meets first-content timing; the result does not.
        origin = turn(1, first_content_ms=702)
        _, child = tool_pair(first_audio_out_ms=153, first_content_ms=153)
        rows = [origin, delivery(origin_to_result_start_ms=3015), child]
        for metric in ("content", "both"):
            code, report = self.run_report(rows, "--gate", "--metric", metric, "--min-turns", "1")
            self.assertEqual(code, 1)
            self.assertEqual(report["counts"]["answered"], 1)
            self.assertEqual(report["counts"]["non_speech"], 1)
            self.assertEqual(report["counts"]["tool_results_joined"], 0)
            self.assertEqual(report["first_content"]["n"], 1)
            self.assertEqual(report["first_content"]["p95_ms"], 1082)
            self.assertEqual(report["tool_result"]["n"], 1)
            self.assertEqual(report["tool_result"]["p95_ms"], 3548)
            self.assertEqual(report["gate"]["reasons"], ["tool_result: p95 3548 ms is not below 1200 ms"])
            self.assertEqual(report["tool_result_rows"][0]["total_ms"], 3548)
        self.assertEqual(self.run_report(rows, "--gate", "--min-turns", "1")[0], 0)

    def test_result_subset_does_not_need_twenty_extra_caller_samples(self):
        rows = [turn(i) for i in range(20)]
        _, child = tool_pair()
        child["turn"] = "item_result"
        rows.extend([delivery(child="result"), child])
        code, report = self.run_report(rows, "--gate", "--metric", "both")
        self.assertEqual(code, 0)
        self.assertEqual(report["first_content"]["n"], 20)
        self.assertEqual(report["tool_result"]["n"], 1)

    def test_result_gate_threshold_and_legacy_link_fail_closed(self):
        _, child = tool_pair()
        for offset, expected in ((819, 0), (820, 1)):
            code, _ = self.run_report([turn(1), delivery(origin_to_result_start_ms=offset), child],
                                      "--gate", "--metric", "both", "--min-turns", "1")
            self.assertEqual(code, expected)
        legacy = delivery()
        del legacy["origin_to_result_start_ms"]
        code, report = self.run_report([turn(1), legacy, child], "--gate", "--metric", "both", "--min-turns", "1")
        self.assertEqual(code, 1)
        self.assertEqual(report["first_content"]["p95_ms"], 880)
        self.assertEqual(report["tool_result"]["n"], 0)
        self.assertEqual(report["counts"]["unverified"], 1)

    def test_result_rows_do_not_echo_arbitrary_identifier_text(self):
        origin, child = tool_pair()
        link = delivery()
        for row in (origin, child, link):
            row["session"] = "private session text"
        origin["turn"] = link["origin_turn"] = "private caller text"
        child["turn"] = link["turn"] = "private result text"
        link["task"] = "private task text"
        _, report = self.run_report([origin, link, child])
        self.assertEqual(report["tool_result"]["n"], 1)
        self.assertNotIn("private", json.dumps(report))

    def test_offset_must_be_explicit_nonnegative_integer(self):
        for value in (-1, True, 1.5, "invalid", None):
            origin, child = tool_pair()
            _, report = self.run_report([origin, delivery(origin_to_result_start_ms=value), child])
            self.assertEqual(report["counts"]["tool_results_joined"], 0)
            self.assertEqual(report["counts"]["missing"], 1)

    def test_malformed_handoff_is_counted_without_echoing_private_fields(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "call.log"
            path.write_text('{"msg":"tools: delivering a task\'s result", "private":"do-not-print"\n')
            result = subprocess.run(["bash", str(SCRIPT), str(path), "--gate", "--json"],
                                    text=True, capture_output=True)
        self.assertEqual(result.returncode, 1)
        self.assertNotIn("do-not-print", result.stdout + result.stderr)
        self.assertEqual(json.loads(result.stdout)["counts"]["malformed"], 1)


if __name__ == "__main__":
    unittest.main()
