"""Synthetic-only regressions: no archive data belongs in this repository."""

import json
from pathlib import Path
import tempfile
import unittest

from extract import assign_splits, call_pairs, extract, parse_line, private_output_dir
from prompt import ASSISTANT_PREFILL, END_TOKEN, first_clause, prompt_for_pair, render_prompt, strip_opener


class PromptTests(unittest.TestCase):
    def test_clause_never_slices_an_overlong_phrase(self):
        self.assertIsNone(first_clause("这是一个超过十二个汉字的完整分句，还有后文。"))
        self.assertEqual(first_clause("可以帮您看看，再确认一下。"), "可以帮您看看，")
        self.assertEqual(first_clause("可以。"), "可以。")
        self.assertIsNone(first_clause("，后文。"))
        self.assertIsNone(first_clause("<think>隐藏推理</think>可以。"))
        self.assertIsNone(first_clause("好的，还有内容。"))

    def test_exact_opener_strip(self):
        self.assertEqual(strip_opener("好的，可以看看。", "好的，"), "可以看看。")
        self.assertEqual(strip_opener("好消息是可以。", "好的，"), "好消息是可以。")

    def test_raw_prompt_has_no_thinking_and_current_opener(self):
        pair = {"context": [], "caller": "还有吗？", "opener": "嗯，"}
        prompt = prompt_for_pair(pair)
        self.assertTrue(prompt.endswith(ASSISTANT_PREFILL + pair["opener"] + "\n"))
        self.assertEqual(prompt.count("<|im_start|>assistant"), 1)
        self.assertIn("<|im_start|>user\n还有吗？" + END_TOKEN, prompt)

    def test_budget_drops_whole_old_exchanges(self):
        context = [{"role": "user", "content": "旧问题" * 100}, {"role": "assistant", "content": "旧答复"},
                   {"role": "user", "content": "新问题"}, {"role": "assistant", "content": "新答复"}]
        result = render_prompt(context, "现在的问题", "嗯，", max_prompt_chars=350)
        self.assertNotIn("旧问题", result)
        self.assertNotIn("旧答复", result)
        self.assertIn("新问题", result)
        self.assertLessEqual(len(result), 350)
        with self.assertRaises(ValueError):
            render_prompt([], "长" * 800, "嗯，")


class ExtractTests(unittest.TestCase):
    @staticmethod
    def turn(n=1, **updates):
        return {"id": str(n), "n": n, "caller_text": "最后的版本", "assistant_text": "可以看看，再确认。",
                "interrupted": False, **updates}

    @staticmethod
    def event(**updates):
        return {"delegations": [{"reason": "speculative", "transcript": "早期的话", "revision": "0", "time": "2026-01-01T00:00:01Z"}],
                "early_final": {"text": "早期的话", "time": "2026-01-01T00:00:00.9Z"},
                "segments": ["可以看看，再确认。"], "completions": [{"time": "2026-01-01T00:00:02Z"}],
                "openers": ["嗯，"], "complete": True, "bad": set(), **updates}

    def test_logfmt_go_escapes(self):
        event = parse_line(r'time=2026-01-01T00:00:00Z msg="synthetic" text="中文\n\x01\"" turn=t1')
        self.assertEqual(event["text"], "中文\n\x01\"")

    def test_early_final_and_actual_opener_are_used(self):
        pairs, _ = call_pairs({"turns": [self.turn()]}, {"1": self.event()}, "hash", "unknown", 6, 768)
        self.assertEqual(len(pairs), 1)
        self.assertEqual(pairs[0]["caller"], "早期的话")
        self.assertEqual(pairs[0]["opener"], "嗯，")
        self.assertEqual(pairs[0]["clause"], "可以看看，")
        self.assertEqual(pairs[0]["full_answer"], "可以看看，再确认。")

    def test_revised_tool_interrupted_or_missing_opener_are_not_gold(self):
        for bad in ({"revised"}, {"tool_turn"}, {"abandoned"}, {"backchanneled"}):
            pairs, _ = call_pairs({"turns": [self.turn()]}, {"1": self.event(bad=bad)}, "hash", "real", 6, 768)
            self.assertEqual(pairs, [])
        for event, turn in ((self.event(openers=[]), self.turn()), (self.event(complete=False), self.turn()),
                            (self.event(), self.turn(interrupted=True))):
            pairs, _ = call_pairs({"turns": [turn]}, {"1": event}, "hash", "real", 6, 768)
            self.assertEqual(pairs, [])

    def test_context_cannot_include_future_or_tool_turns(self):
        call = {"turns": [self.turn(1), self.turn(2), self.turn(3)]}
        events = {"1": self.event(), "2": self.event(bad={"tool_turn"}), "3": self.event()}
        events["3"]["delegations"][0]["time"] = "2026-01-01T00:00:03Z"
        events["3"]["early_final"]["time"] = "2026-01-01T00:00:02.9Z"
        pairs, _ = call_pairs(call, events, "hash", "unknown", 6, 768)
        self.assertEqual([len(p["context"]) for p in pairs], [0, 2])
        events["1"]["completions"][0]["time"] = "2026-01-01T00:00:04Z"
        pairs, _ = call_pairs(call, events, "hash", "unknown", 6, 768)
        self.assertEqual([len(p["context"]) for p in pairs], [0, 0])

    def test_early_final_and_segment_provenance_fail_closed(self):
        for event in (self.event(early_final=None), self.event(segments=["不同的输出。"]),
                      self.event(early_final={"text": "另一个问题", "time": "2026-01-01T00:00:00.9Z"}),
                      self.event(early_final={"text": "早期的话", "time": "2025-12-31T23:59:50Z"})):
            pairs, _ = call_pairs({"turns": [self.turn()]}, {"1": event}, "hash", "unknown", 6, 768)
            self.assertEqual(pairs, [])

    def test_split_is_stable(self):
        first = [{"call_hash": str(index)} for index in range(30)]
        second = list(reversed([dict(pair) for pair in first]))
        assign_splits(first, "seed")
        assign_splits(second, "seed")
        self.assertEqual({p["call_hash"]: p["split"] for p in first},
                         {p["call_hash"]: p["split"] for p in second})
        self.assertEqual({p["split"] for p in first}, {"train", "valid", "test"})

    def test_outputs_private_and_project_refused(self):
        with self.assertRaises(ValueError):
            private_output_dir(Path(__file__).parent / "private-data")
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            archive = root / "archive"
            for index in range(20):
                folder = archive / str(index)
                folder.mkdir(parents=True)
                call = {"id": f"synthetic-{index}", "session": f"s{index}", "started_at": str(index),
                        "simulated": True, "turns": [self.turn()]}
                (folder / "call.json").write_text(json.dumps(call))
                records = [
                    {"msg": "asr: final for the words so far", "text": f"合成问题{index}", "time": "2026-01-01T00:00:00.9Z"},
                    {"msg": "think: delegating", "turn": "1", "reason": "speculative", "revision": 0, "transcript": f"合成问题{index}", "time": "2026-01-01T00:00:01Z"},
                    {"msg": "speak: opened the answer from the phrase cache at the pause", "turn": "1", "text": "嗯，"},
                    {"msg": "think: backend complete", "turn": "1", "tool_calls": 0},
                    {"msg": "speak: segment synthesized", "turn": "1", "text": "可以看看，再确认。"},
                ]
                for record in records:
                    record["session"] = call["session"]
                (folder / "golive.log").write_text("".join(json.dumps(r) + "\n" for r in records))
            output = root / "data"
            first = extract(archive, output)
            saved = (output / "pairs.jsonl").read_bytes()
            second = extract(archive, output)
            self.assertEqual(first, second)
            self.assertEqual(saved, (output / "pairs.jsonl").read_bytes())
            self.assertEqual(output.stat().st_mode & 0o777, 0o700)
            for path in output.iterdir():
                self.assertEqual(path.stat().st_mode & 0o777, 0o600)
            self.assertEqual(first["counts"]["pairs_kept"], 20)
            self.assertNotIn("合成问题", json.dumps(first))
            self.assertTrue(all(first["splits"][s]["pairs"] for s in ("train", "valid", "test")))
            groups = [set(json.loads(line)["call_hash"] for line in (output / f"{s}.pairs.jsonl").read_text().splitlines())
                      for s in ("train", "valid", "test")]
            self.assertFalse(groups[0] & groups[1] or groups[0] & groups[2] or groups[1] & groups[2])
            # A replay with a new call identity may not cross into another split.
            duplicate = archive / "duplicate"
            duplicate.mkdir()
            call = json.loads((archive / "0" / "call.json").read_text())
            call["id"] = "synthetic-duplicate"
            (duplicate / "call.json").write_text(json.dumps(call))
            (duplicate / "golive.log").write_bytes((archive / "0" / "golive.log").read_bytes())
            deduped = extract(archive, output)
            self.assertEqual(deduped["counts"]["pairs_kept"], 20)
            self.assertEqual(deduped["counts"]["duplicate_pairs_dropped"], 1)


if __name__ == "__main__":
    unittest.main()
