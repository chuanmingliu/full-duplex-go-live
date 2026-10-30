"""Synthetic WAV/annotation checks; these fixtures are not handset evidence."""

import copy
import hashlib
import json
from pathlib import Path
import subprocess
import tempfile
import unittest
import wave


SCRIPT = Path(__file__).with_name("handset_latency.py")


class HandsetLatencyTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.wav = self.root / "synthetic.wav"
        self.rate, self.frames = 8000, 20 * 32000 + 16000
        self.write_wav()
        self.meta = {
            "schema": "handset-acoustic-capture/v1", "call_id": "out-synthetic",
            "wav_sha256": hashlib.sha256(self.wav.read_bytes()).hexdigest(),
            "sample_rate": self.rate, "frames": self.frames,
            "channels": 1, "sample_format": "pcm16le", "capture_complete": True,
            "issues": [], "source": "acoustic_speakerphone_microphone",
            "placement_confirmed": True, "stop_reason": "user",
            "first_audio_context_frame": 128, "final_audio_context_frame": 128 + self.frames,
            "max_frames": self.rate * 720,
            "started_at": "2026-10-30T10:00:00.000Z", "ended_at": "2026-10-30T10:02:00.000Z",
            "audio_settings": {"echoCancellation": False, "noiseSuppression": False,
                               "autoGainControl": False},
            "audio_settings_at_end": {"echoCancellation": False, "noiseSuppression": False,
                                      "autoGainControl": False},
        }
        self.annotation = {
            "schema": "handset-acoustic-annotations/v1", "call_id": self.meta["call_id"],
            "wav_sha256": self.meta["wav_sha256"], "sample_rate": self.rate, "frames": self.frames,
            "review": {"method": "manual_acoustic", "complete_call": True,
                       "recording_covers_complete_call": True, "omitted_turns": 0, "unresolved_turns": 0},
            "turns": [{"turn_id": f"turn_{index + 1}", "reviewed": True, "status": "answered",
                       "caller_start_sample": index * 32000,
                       "caller_end_sample": index * 32000 + 4000,
                       "first_audio_sample": index * 32000 + 7200,
                       "first_content_sample": index * 32000 + 11200,
                       "quote_requested": False, "quote_result_sample": None} for index in range(20)],
        }

    def write_wav(self, channels=1, sample_width=2, rate=None, silent=False):
        with wave.open(str(self.wav), "wb") as out:
            out.setnchannels(channels)
            out.setsampwidth(sample_width)
            out.setframerate(rate or self.rate)
            sample = bytes(sample_width) if silent else bytes([1]) + bytes(sample_width - 1)
            out.writeframes(sample * channels * self.frames)

    def run_report(self, *flags, metadata=None, annotations=None):
        meta = self.root / "capture.json"
        ann = self.root / "annotations.json"
        meta.write_text(json.dumps(self.meta if metadata is None else metadata))
        ann.write_text(json.dumps(self.annotation if annotations is None else annotations))
        result = subprocess.run(["python3", "-B", str(SCRIPT), str(self.wav), str(meta), str(ann),
                                 "--json", *flags], text=True, capture_output=True)
        self.assertEqual(result.stderr, "", result.stderr)
        return result.returncode, json.loads(result.stdout)

    def assert_invalid(self, phrase, **kwargs):
        code, report = self.run_report(**kwargs)
        self.assertEqual(code, 2, report)
        self.assertFalse(report["gate"]["passed"])
        self.assertIn(phrase, " ".join(report["gate"]["reasons"]))
        self.assertNotIn("first_audio", report)

    def test_twenty_complete_rows_pass_only_supplied_boundaries(self):
        code, report = self.run_report()
        self.assertEqual(code, 0, report)
        self.assertTrue(report["gate"]["passed"])
        self.assertTrue(report["independent_acoustic_review_required"])
        self.assertEqual(report["assessment"], "provided_manual_acoustic_boundaries")
        self.assertEqual(report["first_audio"]["p95_ms"], 400)
        self.assertEqual(report["first_content"]["p95_ms"], 900)
        self.assertEqual(report["first_content"]["n"], 20)
        self.assertEqual(report["quote_result"]["n"], 0)

    def test_nineteen_rows_fail_without_fabricated_sample(self):
        self.annotation["turns"].pop()
        code, report = self.run_report()
        self.assertEqual(code, 1)
        self.assertEqual(report["first_audio"]["n"], 19)
        self.assertEqual(report["first_content"]["n"], 19)

    def test_exact_1200_boundary_fails_and_one_sample_below_passes(self):
        for turn in self.annotation["turns"]:
            turn["first_content_sample"] = turn["caller_end_sample"] + 9600
        code, report = self.run_report()
        self.assertEqual(code, 1)
        self.assertEqual(report["first_content"]["p95_ms"], 1200)
        self.assertEqual(report["first_content"]["at_or_above_target"], 20)
        for turn in self.annotation["turns"]:
            turn["first_content_sample"] -= 1
        code, report = self.run_report()
        self.assertEqual(code, 0)
        self.assertEqual(report["first_content"]["p95_ms"], 1199.875)

    def test_nearest_rank_p95_is_not_maximum(self):
        turn = self.annotation["turns"][-1]
        turn["first_content_sample"] = turn["caller_end_sample"] + 10400
        code, report = self.run_report()
        self.assertEqual(code, 0)
        self.assertEqual(report["first_content"]["p95_ms"], 900)
        self.assertEqual(report["first_content"]["max_ms"], 1300)
        self.assertEqual(report["first_content"]["at_or_above_target"], 1)

    def test_quote_result_can_fail_when_generic_content_is_fast(self):
        turn = self.annotation["turns"][3]
        turn.update(quote_requested=True, quote_result_sample=turn["caller_end_sample"] + 16000)
        code, report = self.run_report()
        self.assertEqual(code, 1)
        self.assertEqual(report["first_content"]["p95_ms"], 900)
        self.assertEqual(report["quote_result"]["p95_ms"], 2000)
        self.assertEqual(report["quote_result"]["n"], 1)
        self.assertEqual(report["caller_turns"], 20)
        self.assertEqual(len(report["turns"]), 20)

    def test_one_fast_quote_needs_no_twenty_quote_minimum(self):
        turn = self.annotation["turns"][3]
        turn.update(quote_requested=True, quote_result_sample=turn["caller_end_sample"] + 8000)
        code, report = self.run_report()
        self.assertEqual(code, 0, report)
        self.assertEqual(report["quote_result"]["p95_ms"], 1000)

    def test_incomplete_capture_and_gaps_cannot_pass(self):
        for changes in ({"capture_complete": False}, {"issues": ["missing_frames"]},
                        {"stop_reason": "limit"}, {"placement_confirmed": False},
                        {"first_audio_context_frame": 129}, {"max_frames": self.frames - 1}):
            with self.subTest(changes=changes):
                meta = {**self.meta, **changes}
                code, report = self.run_report(metadata=meta)
                self.assertEqual(code, 2, report)

    def test_actual_processing_must_be_explicitly_disabled(self):
        for key in self.meta["audio_settings"]:
            for value in (True, None, "false", 0):
                with self.subTest(key=key, value=value):
                    meta = copy.deepcopy(self.meta)
                    meta["audio_settings"][key] = value
                    self.assert_invalid("processing", metadata=meta)
        meta = copy.deepcopy(self.meta)
        del meta["audio_settings"]["echoCancellation"]
        self.assert_invalid("processing", metadata=meta)

    def test_server_or_simulator_source_is_not_handset_evidence(self):
        for source in ("application", "simulator", "box_recording", None):
            with self.subTest(source=source):
                self.assert_invalid("acoustic handset", metadata={**self.meta, "source": source})

    def test_final_processing_settings_must_also_be_present_and_disabled(self):
        for key in self.meta["audio_settings_at_end"]:
            with self.subTest(key=key):
                meta = copy.deepcopy(self.meta)
                meta["audio_settings_at_end"][key] = True
                self.assert_invalid("processing", metadata=meta)
        meta = copy.deepcopy(self.meta)
        del meta["audio_settings_at_end"]
        self.assert_invalid("processing", metadata=meta)

    def test_capture_hash_rate_frames_and_format_match_original_wav(self):
        for field, value in (("wav_sha256", "0" * 64), ("sample_rate", 16000),
                             ("frames", self.frames - 1), ("channels", 2),
                             ("sample_format", "float32")):
            with self.subTest(field=field):
                code, report = self.run_report(metadata={**self.meta, field: value})
                self.assertEqual(code, 2, report)

    def test_annotations_are_bound_to_call_and_recording(self):
        for field, value in (("wav_sha256", "0" * 64), ("sample_rate", 16000),
                             ("frames", self.frames - 1), ("call_id", "out-another")):
            with self.subTest(field=field):
                self.assert_invalid("Annotations do not match", annotations={**self.annotation, field: value})

    def test_complete_call_attestation_cannot_omit_unresolved_turns(self):
        for field, value in (("complete_call", False), ("recording_covers_complete_call", False),
                             ("omitted_turns", 1), ("unresolved_turns", 1),
                             ("method", "automatic_vad"), ("omitted_turns", False)):
            with self.subTest(field=field):
                ann = copy.deepcopy(self.annotation)
                ann["review"][field] = value
                code, report = self.run_report(annotations=ann)
                self.assertEqual(code, 2, report)

    def test_duplicate_turns_fail_instead_of_inflating_count(self):
        self.annotation["turns"][-1]["turn_id"] = "turn_1"
        self.assert_invalid("Duplicate")

    def test_missing_content_or_audio_fails_instead_of_dropping_turn(self):
        for field in ("first_content_sample", "first_audio_sample", "caller_end_sample"):
            with self.subTest(field=field):
                ann = copy.deepcopy(self.annotation)
                del ann["turns"][3][field]
                self.assert_invalid("boundaries", annotations=ann)

    def test_interrupted_unanswered_or_unreviewed_rows_cannot_be_excluded(self):
        for key, value in (("status", "interrupted"), ("status", "unanswered"), ("reviewed", False)):
            with self.subTest(value=value):
                ann = copy.deepcopy(self.annotation)
                ann["turns"][3][key] = value
                code, report = self.run_report(annotations=ann)
                self.assertEqual(code, 2, report)

    def test_quote_requires_an_explicit_valid_result_boundary(self):
        for result in (None, -1, self.frames, 1.5, True):
            with self.subTest(result=result):
                ann = copy.deepcopy(self.annotation)
                ann["turns"][3].update(quote_requested=True, quote_result_sample=result)
                self.assert_invalid("Quotation result", annotations=ann)
        self.annotation["turns"][0]["quote_result_sample"] = 12000
        self.assert_invalid("Non-quotation")

    def test_invalid_ranges_boolean_and_noninteger_indices_fail(self):
        for field, value in (("caller_start_sample", -1), ("caller_end_sample", 0),
                             ("first_audio_sample", 3999), ("first_content_sample", self.frames),
                             ("caller_end_sample", True), ("first_content_sample", 11200.0)):
            with self.subTest(field=field, value=value):
                ann = copy.deepcopy(self.annotation)
                ann["turns"][0][field] = value
                code, report = self.run_report(annotations=ann)
                self.assertEqual(code, 2, report)

    def test_caller_order_and_cross_turn_response_pairing_fail(self):
        ann = copy.deepcopy(self.annotation)
        ann["turns"][0], ann["turns"][1] = ann["turns"][1], ann["turns"][0]
        self.assert_invalid("chronological", annotations=ann)
        self.annotation["turns"][0]["first_content_sample"] = 32000
        self.assert_invalid("next caller")

    def test_stereo_unsupported_width_truncation_and_silence_fail(self):
        self.write_wav(channels=2)
        self.assert_invalid("mono PCM16")
        self.write_wav(sample_width=1)
        self.assert_invalid("mono PCM16")
        self.write_wav(silent=True)
        self.assert_invalid("no nonzero")
        self.write_wav()
        self.wav.write_bytes(self.wav.read_bytes()[:-100])
        self.assert_invalid("truncated")

    def test_provenance_timestamps_must_be_ordered_and_timezone_aware(self):
        for started, ended in (("2026-10-30T10:02:00Z", "2026-10-30T10:00:00Z"),
                               ("2026-10-30T10:00:00", "2026-10-30T10:02:00"),
                               ("bad", "2026-10-30T10:02:00Z")):
            with self.subTest(started=started):
                code, report = self.run_report(metadata={**self.meta, "started_at": started, "ended_at": ended})
                self.assertEqual(code, 2, report)

    def test_options_may_tighten_but_not_weaken_the_goal(self):
        self.assertEqual(self.run_report("--target-ms", "1500")[0], 2)
        self.assertEqual(self.run_report("--min-turns", "19")[0], 2)
        self.assertEqual(self.run_report("--target-ms", "900")[0], 1)
        self.assertEqual(self.run_report("--min-turns", "21")[0], 1)

    def test_no_transcript_device_label_or_arbitrary_identifier_is_printed(self):
        private = "private full conversation and device label"
        self.meta["audio_settings"]["label"] = private
        self.annotation["turns"][0]["transcript"] = private
        code, report = self.run_report()
        self.assertEqual(code, 0)
        self.assertNotIn(private, json.dumps(report))
        self.annotation["turns"][0]["turn_id"] = private
        code, report = self.run_report()
        self.assertEqual(code, 2)
        self.assertNotIn(private, json.dumps(report))

    def test_json_duplicate_keys_nonfinite_and_invalid_structure_fail(self):
        self.run_report()
        path = self.root / "annotations.json"
        for raw in ('{"schema":1,"schema":2}', '{"value":NaN}', '[]', '{'):
            path.write_text(raw)
            result = subprocess.run(["python3", "-B", str(SCRIPT), str(self.wav),
                                     str(self.root / "capture.json"), str(path), "--json"],
                                    text=True, capture_output=True)
            self.assertEqual(result.returncode, 2, result.stderr)
            self.assertFalse(json.loads(result.stdout)["gate"]["passed"])


if __name__ == "__main__":
    unittest.main()
