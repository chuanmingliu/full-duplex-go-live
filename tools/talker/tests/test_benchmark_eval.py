"""Handcrafted fixtures only. Never load the private call archive in tests."""
import io
import json
from pathlib import Path
import sys
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import patch

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
import benchmark as b
import evaluate as e


class ClauseTests(unittest.TestCase):
    def test_requires_natural_boundary_or_eos(self):
        self.assertFalse(b.clause_state("可以安排时间")["valid_clause"])
        self.assertTrue(b.clause_state("可以安排时间", eos=True)["valid_clause"])
        self.assertFalse(b.clause_state("可以安排时间", eos=True, limited=True)["valid_clause"])
        self.assertTrue(b.clause_state("可以安排时间，", limited=True)["valid_clause"])

    def test_never_clips_long_first_clause(self):
        text = "甲" * 13 + "，后面。"
        state = b.clause_state(text)
        self.assertFalse(state["valid_clause"])
        self.assertEqual(state["clause"], "甲" * 13 + "，")
        self.assertTrue(state["long"])

    def test_punctuation_does_not_consume_character_budget(self):
        state = b.clause_state("甲" * 12 + "。")
        self.assertTrue(state["valid_clause"])
        self.assertEqual(state["clause_chars"], 12)

    def test_empty_and_thinking_are_invalid(self):
        self.assertFalse(b.clause_state("。", eos=True)["valid_clause"])
        self.assertFalse(b.clause_state("<think>秘密。", eos=True)["valid_clause"])


class PrivacyTests(unittest.TestCase):
    def test_output_inside_checkout_or_symlink_is_rejected(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            (root/"repo").mkdir()
            (root/"repo"/".git").write_text("gitdir: /tmp/not-used")
            (root/"alias").symlink_to(root/"repo")
            for path in (root/"repo"/"out.json", root/"alias"/"out.json"):
                with self.assertRaises(ValueError):
                    b.private_path(path)

    def test_private_modes(self):
        with tempfile.TemporaryDirectory() as tmp:
            out = b.private_dir(Path(tmp)/"results")
            b.write_private(out/"data.json", {"count": 3})
            self.assertEqual(out.stat().st_mode & 0o777, 0o700)
            self.assertEqual((out/"data.json").stat().st_mode & 0o777, 0o600)

    def test_home_repository_exception_requires_ignored_untracked_path(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp).resolve()
            (root/".git").mkdir()
            path = root/"private"/"results.json"
            ok = SimpleNamespace(returncode=0, stdout=b"")
            tracked = SimpleNamespace(returncode=0, stdout=b"private/results.json")
            not_ignored = SimpleNamespace(returncode=1, stdout=b"")
            with patch.object(b.Path, "home", return_value=root), patch.object(b.subprocess, "run", side_effect=[ok, ok]):
                self.assertEqual(b.private_path(path), path)
            for results in ([not_ignored, ok], [ok, tracked]):
                with patch.object(b.Path, "home", return_value=root), patch.object(b.subprocess, "run", side_effect=results):
                    with self.assertRaises(ValueError):
                        b.private_path(path)

    def test_inference_never_uses_remote_host(self):
        for endpoint in ("https://127.0.0.1:9", "http://example.org", "http://user@localhost:9"):
            with self.assertRaises(ValueError):
                b.local_url(endpoint)


class StreamTests(unittest.TestCase):
    def run_stream(self, payloads):
        wire = b"".join(("data: " + json.dumps(p) + "\n\n").encode() for p in payloads)
        response = io.BytesIO(wire)
        class Client:
            def open(self, request, timeout):
                self.request = json.loads(request.data)
                return response
        client = Client()
        clock = iter([0, *[i*.01 for i in range(1, len(payloads)+1)], .2])
        with patch.object(b, "http_client", return_value=client):
            row = b.run_completion("http://127.0.0.1:19", "synthetic", cache_prompt=False,
                                   clock=lambda: next(clock))
        return row, client.request

    def test_token_and_clause_are_distinct(self):
        row, payload = self.run_stream([
            {"content": "", "tokens": [1], "stop": False},
            {"content": "可以", "tokens": [2], "stop": False},
            {"content": "安排，", "tokens": [3], "stop": False},
            {"content": "", "stop": True, "stop_type": "eos",
             "timings": {"prompt_n": 12, "cache_n": 8, "predicted_n": 4}},
        ])
        self.assertEqual(row["ttft_ms"], 10)
        self.assertEqual(row["first_text_ms"], 20)
        self.assertEqual(row["clause_ms"], 30)
        self.assertEqual(row["clause"], "可以安排，")
        self.assertEqual(row["cached_tokens"], 8)
        self.assertFalse(payload["cache_prompt"])

    def test_eos_closes_unpunctuated_clause(self):
        row, _ = self.run_stream([
            {"content": "可以安排", "tokens": [1]},
            {"content": "", "stop": True, "stop_type": "eos"}])
        self.assertEqual(row["clause_ms"], 20)

    def test_leading_whitespace_does_not_end_the_first_clause(self):
        row, _ = self.run_stream([
            {"content": "\n  ", "tokens": [1]},
            {"content": "可以", "tokens": [2]},
            {"content": "办理。", "tokens": [3]},
            {"content": "", "stop": True, "stop_type": "eos"}])
        self.assertEqual(row["ttft_ms"], 10)
        self.assertEqual(row["first_text_ms"], 20)
        self.assertEqual(row["clause"], "可以办理。")
        self.assertEqual(row["clause_ms"], 30)
        self.assertEqual(row["complete_clause_ms"], 30)
        self.assertEqual(row["valid_clause"], 1)

    def test_limit_does_not_close_unpunctuated_clause(self):
        row, _ = self.run_stream([
            {"content": "可以安排", "tokens": [1]},
            {"content": "", "stop": True, "stop_type": "limit"}])
        self.assertIsNone(row["clause_ms"])
        self.assertEqual(row["clipped"], 1)

    def test_missing_terminal_event_fails(self):
        with self.assertRaises(RuntimeError):
            self.run_stream([{"content": "可以，", "tokens": [1]}])

    def test_prefix_excludes_latest_caller_and_opener(self):
        pair = {"context": [{"role": "user", "content": "以前的问题"},
                            {"role": "assistant", "content": "以前的回答"}],
                "caller": "新问题甲乙丙", "opener": "开场丁戊己"}
        prompt = b.prompt_for_pair(pair)
        prefix = b.prefix_for_prompt(prompt)
        self.assertNotIn(pair["caller"], prefix)
        self.assertNotIn(pair["opener"], prefix)
        self.assertTrue(prompt.startswith(prefix))
        with patch.object(b, "request_json", return_value={}) as request:
            b.prime_prefix("http://127.0.0.1:19", prompt, 1)
        payload = request.call_args.args[1]
        self.assertEqual(payload["n_predict"], 0)
        self.assertFalse(payload["cache_prompt"])
        self.assertEqual(payload["prompt"], prefix)


class SummaryTests(unittest.TestCase):
    def test_failed_requests_stay_in_denominator(self):
        rows = [dict(mode="warm_prefix", valid_clause=1, clause_ms=100,
                     from_early_final_clause_ms=101, ttft_ms=25, error=0),
                dict(mode="warm_prefix", valid_clause=0, error=1),
                dict(mode="warm_prefix", valid_clause=0, error=0, long=1)]
        result = b.summarize(rows)["warm_prefix"]
        self.assertEqual(result["attempts"], 3)
        self.assertEqual(result["valid_under_300ms_all_attempts"]["rate"], .333333)
        self.assertEqual(result["clause_ms"]["n"], 1)

    def test_wilson_bounds_and_empty(self):
        self.assertIsNone(b.wilson(0, 0)["rate"])
        self.assertLess(b.wilson(5, 5)["ci95_low"], 1)
        self.assertGreater(b.wilson(0, 5)["ci95_high"], 0)


class EvaluationTests(unittest.TestCase):
    def setUp(self):
        self.pairs = [{"id": str(i), "call_hash": "call-a", "split": "test", "context": [],
                       "caller": "可以预约吗", "opener": "关于预约，", "full_answer": "可以安排。"}
                      for i in range(4)]
        self.predictions = [{"id": "0", "mode": "warm_prefix", "repeat": 0, "error": 0,
                             "clause": "可以安排。", "valid_clause": 1, "complete": 1},
                            {"id": "1", "mode": "warm_prefix", "repeat": 0, "error": 1},
                            {"id": "2", "mode": "warm_prefix", "repeat": 0, "error": 0,
                             "clause": "可以安排", "valid_clause": 0, "complete": 0, "clipped": 1}]

    def test_all_held_out_attempts_including_missing_remain_denominator(self):
        rows, requests = e.prepare(self.pairs, self.predictions, "warm_prefix", 0)
        self.assertEqual(len(requests), 1)
        verdicts = [{"id": "0", **{key: True for key in e.CRITERIA}}]
        result = e.summarize(rows, verdicts)
        self.assertEqual(result["expected_held_out"], 4)
        self.assertEqual(result["missing_predictions"], 1)
        self.assertEqual(result["generation_errors"], 1)
        self.assertEqual(result["accepted_all_expected"]["rate"], .25)

    def test_judge_errors_not_silently_dropped(self):
        rows, _ = e.prepare(self.pairs, self.predictions, "warm_prefix", 0)
        result = e.summarize(rows, [{"id": "0", "judge_error": 1}])
        self.assertEqual(result["judge_errors"], 1)
        self.assertEqual(result["accepted_all_expected"]["rate"], 0)
        self.assertEqual(result["judge_coverage_complete"], 0)

    def test_pending_judgments_do_not_look_like_zero_acceptance(self):
        rows, _ = e.prepare(self.pairs, self.predictions, "warm_prefix", 0)
        result = e.summarize(rows, [])
        self.assertEqual(result["pending_judgments"], 1)
        self.assertIsNone(result["accepted_all_expected"]["rate"])
        self.assertEqual(result["confirmed_acceptance_lower_bound"], 0)

    def test_cloud_requires_two_separate_raw_data_flags(self):
        _, requests = e.prepare(self.pairs, self.predictions, "warm_prefix", 0)
        for cloud, private in ((False, False), (False, True), (True, False)):
            with self.assertRaises(ValueError):
                e.cloud_requests(requests, None, allow_cloud=cloud, allow_private=private)
        self.assertEqual(e.cloud_requests(requests, None, allow_cloud=True, allow_private=True), requests)

    def test_redacted_queue_requires_review_and_exact_ids(self):
        _, requests = e.prepare(self.pairs, self.predictions, "warm_prefix", 0)
        with self.assertRaises(ValueError):
            e.cloud_requests(requests, requests, allow_cloud=True, allow_private=False)
        reviewed = [{**r, "approved_redacted": True} for r in requests]
        self.assertEqual(e.cloud_requests(requests, reviewed, allow_cloud=True, allow_private=False), reviewed)
        with self.assertRaises(ValueError):
            e.cloud_requests(requests, [], allow_cloud=True, allow_private=False)

    def test_no_training_split_judging(self):
        with self.assertRaises(ValueError):
            e.select_pairs(self.pairs, "train")

    def test_boolean_schema_does_not_treat_strings_as_true(self):
        with self.assertRaises(ValueError):
            e.validate_verdict({key: "false" for key in e.CRITERIA})


if __name__ == "__main__":
    unittest.main()
