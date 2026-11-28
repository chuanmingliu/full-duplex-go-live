import hashlib
import json
import unittest

from memory_import import episode_from_archive, import_episode


class ArchiveImportTest(unittest.TestCase):
    def test_projection_preserves_raw_evidence_and_timestamps(self):
        call = {"id": "abc123", "ended_at": "2026-01-01T01:00:00Z",
                "transcript": [{"who": "caller", "text": "重复价格", "t_s": 1.2},
                               {"who": "assistant", "text": "报价", "t_s": 2.4}],
                "turns": [{"n": 1, "total_ms": 900}], "quotation": [{"kind": "quote"}]}
        raw = json.dumps(call, ensure_ascii=False, indent=2) + "\r\n"
        e = episode_from_archive(raw, "real", ["/archive/audio.wav"])
        self.assertEqual(e["archive_json"], raw)
        self.assertEqual(e["archive_sha256"], hashlib.sha256(raw.encode()).hexdigest())
        self.assertEqual([v["kind"] for v in e["evidence"]], ["user", "assistant", "timing", "tool"])
        self.assertEqual(json.loads(e["evidence"][0]["text"])["t_s"], 1.2)
        self.assertNotIn("owner_done", e)

    def test_incomplete_and_unsupported_input(self):
        with self.assertRaises(ValueError):
            episode_from_archive('{"ended_at":"0001-01-01T00:00:00Z"}', "real")
        with self.assertRaises(ValueError):
            episode_from_archive('{"ended_at":"2026-01-01T00:00:00Z","transcript":[]}', "real")
        # Invalid remote URLs are rejected before reading any credential file.
        for url in ("http://example.com", "https://user:password@example.com", "https://example.com/?token=x"):
            with self.assertRaises(ValueError):
                import_episode(url, "/not-a-token", {})


if __name__ == "__main__":
    unittest.main()
