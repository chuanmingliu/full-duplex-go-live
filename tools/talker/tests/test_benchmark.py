"""Only invented examples: no archive data belongs in this test suite."""
import contextlib
import io
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

from tools.talker import benchmark as bench


def sse(*events):
    return io.BytesIO(b"".join(
        b"data: " + json.dumps(event, ensure_ascii=False).encode() + b"\n\n"
        for event in events))


class Client:
    def __init__(self, response):
        self.response = response
        self.request = None

    def open(self, request, timeout=None):
        self.request = request
        return self.response


class CompletionTests(unittest.TestCase):
    def completion(self, *events, ticks=None):
        client = Client(sse(*events))
        clock = iter(ticks or [i/10 for i in range(len(events)+2)])
        with patch.object(bench, "http_client", return_value=client):
            result = bench.run_completion("http://127.0.0.1:8080", "synthetic",
                                          cache_prompt=False, clock=lambda: next(clock))
        return result, json.loads(client.request.data)

    def test_clause_latency_stops_at_first_boundary_not_eos(self):
        result, payload = self.completion(
            {"content": "可以", "tokens": [1]},
            {"content": "办理，", "tokens": [2]},
            {"content": "之后再说明。", "tokens": [3]},
            {"content": "", "stop": True, "stop_type": "eos",
             "timings": {"prompt_n": 100, "predicted_n": 4}})
        self.assertEqual(result["clause"], "可以办理，")
        self.assertEqual(result["ttft_ms"], 100)
        self.assertEqual(result["clause_ms"], 200)
        self.assertEqual(result["request_ms"], 500)
        self.assertEqual(result["valid_clause"], 1)
        self.assertFalse(payload["cache_prompt"])

    def test_token_arrival_precedes_displayable_utf8_text(self):
        result, _ = self.completion(
            {"content": "", "tokens": [1]},
            {"content": "可以。", "tokens": [2]},
            {"stop": True, "stop_type": "eos"})
        self.assertEqual(result["ttft_ms"], 100)
        self.assertEqual(result["first_text_ms"], 200)

    def test_eos_completes_a_short_unpunctuated_clause(self):
        result, _ = self.completion({"content": "可以办理"},
                                    {"stop": True, "stop_type": "eos"})
        self.assertEqual(result["clause_ms"], 200)
        self.assertEqual(result["valid_clause"], 1)

    def test_generation_limit_does_not_manufacture_a_clause(self):
        result, _ = self.completion({"content": "需要先查看完整说明后才能决定"},
                                    {"stop": True, "stop_type": "limit"})
        self.assertEqual(result["valid_clause"], 0)
        self.assertEqual(result["incomplete"], 1)
        self.assertEqual(result["clipped"], 1)
        self.assertIsNone(result["clause_ms"])

    def test_valid_boundary_before_limit_remains_a_complete_clause(self):
        result, _ = self.completion({"content": "可以办理，后续"},
                                    {"stop": True, "stop_type": "limit"})
        self.assertEqual(result["valid_clause"], 1)
        self.assertEqual(result["clipped"], 1)
        self.assertEqual(result["clause_ms"], 100)

    def test_long_first_clause_is_not_sliced_to_limit(self):
        text = "需要先查看完整的详细说明之后才能决定。"
        result, _ = self.completion({"content": text},
                                    {"stop": True, "stop_type": "eos"})
        self.assertEqual(result["clause"], text)
        self.assertEqual(result["long"], 1)
        self.assertEqual(result["complete_clause_ms"], 100)
        self.assertIsNone(result["clause_ms"])

    def test_special_output_and_truncated_context_fail(self):
        for content, truncated in [("<think>检查。", False), ("可以办理。", True)]:
            with self.subTest(truncated=truncated):
                result, _ = self.completion({"content": content},
                    {"stop": True, "stop_type": "eos", "truncated": truncated})
                self.assertEqual(result["valid_clause"], 0)
                self.assertIsNone(result["clause_ms"])

    def test_unknown_stop_word_is_not_eos(self):
        result, _ = self.completion({"content": "可以"},
            {"stop": True, "stop_type": "word", "stopping_word": "<|im_start|>"})
        self.assertEqual(result["incomplete"], 1)

    def test_end_token_stop_can_complete(self):
        result, _ = self.completion({"content": "可以"},
            {"stop": True, "stop_type": "word", "stopping_word": bench.END_TOKEN})
        self.assertEqual(result["valid_clause"], 1)

    def test_missing_terminal_event_is_an_error(self):
        with self.assertRaisesRegex(RuntimeError, "stream_without_terminal_event"):
            self.completion({"content": "可以。"})

    def test_server_error_is_not_echoed(self):
        with self.assertRaisesRegex(RuntimeError, "^server_stream_error$"):
            self.completion({"error": {"message": "invented-private-input"}})


class IntegrityTests(unittest.TestCase):
    def test_private_paths_reject_checkout_worktree_and_symlink(self):
        with tempfile.TemporaryDirectory() as temporary:
            base = Path(temporary)
            repo = base/"checkout"
            repo.mkdir()
            (repo/".git").write_text("gitdir: another-location")
            link = base/"link"
            link.symlink_to(repo, target_is_directory=True)
            for path in (repo/"data.jsonl", link/"data.jsonl"):
                with self.assertRaisesRegex(ValueError, "private_path_inside_git_checkout"):
                    bench.private_path(path)

    def test_atomic_outputs_are_private_and_parseable(self):
        with tempfile.TemporaryDirectory() as temporary:
            output = bench.private_dir(Path(temporary)/"results")/"rows.jsonl"
            bench.write_private(output, [{"n": 1}, {"n": 2}], jsonl=True)
            self.assertEqual(bench.read_jsonl(output), [{"n": 1}, {"n": 2}])
            self.assertEqual(output.stat().st_mode & 0o777, 0o600)
            self.assertEqual(output.parent.stat().st_mode & 0o777, 0o700)

    def test_private_network_has_no_redirect_or_remote_fallback(self):
        for url in ("https://cloud.invalid/v1", "http://127.0.0.1.evil.invalid", "http://user@127.0.0.1"):
            with self.subTest(url=url), self.assertRaises(ValueError):
                bench.local_url(url)
        self.assertEqual(bench.local_url("http://localhost:8080/v1/"), "http://127.0.0.1:8080/v1")
        with self.assertRaisesRegex(ValueError, "redirect_forbidden"):
            bench.NoRedirect().redirect_request(None)

    def test_prefix_contains_only_prior_context(self):
        prompt = ("<|im_start|>system\nsynthetic<|im_end|>\n"
                  "<|im_start|>user\nprevious<|im_end|>\n"
                  "<|im_start|>assistant\nprior response<|im_end|>\n"
                  "<|im_start|>user\ncurrent and opener<|im_end|>\n"
                  "<|im_start|>assistant\n")
        prefix = bench.prefix_for_prompt(prompt)
        self.assertIn("prior response", prefix)
        self.assertNotIn("current and opener", prefix)

    def test_failure_denominator_is_all_attempts(self):
        rows = [{"mode": "warm_no_cache", "error": 0, "valid_clause": 1,
                 "clause_ms": 290, "from_early_final_clause_ms": 301},
                {"mode": "warm_no_cache", "error": 1, "valid_clause": 0},
                {"mode": "warm_no_cache", "error": 0, "valid_clause": 1,
                 "clause_ms": 200, "from_early_final_clause_ms": 201}]
        result = bench.summarize(rows)["warm_no_cache"]
        self.assertEqual(result["attempts"], 3)
        self.assertEqual(result["valid_under_300ms"], 1)
        self.assertEqual(result["valid_under_300ms_all_attempts"]["rate"], .333333)

    def test_nearest_rank_percentile_and_nonfinite_filter(self):
        result = bench.distribution([1, 2, None, float("nan"), float("inf"), 100])
        self.assertEqual(result, {"n": 3, "p50": 2, "p95": 100, "max": 100})


if __name__ == "__main__":
    unittest.main()
