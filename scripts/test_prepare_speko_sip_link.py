import json
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

from prepare_speko_sip_link import REMOTE_SCRIPT


class PrepareLinkTest(unittest.TestCase):
    def test_preserves_original_and_refuses_different_existing_files(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / "private").mkdir()
            original = {"golive_url": "http://127.0.0.1:9885", "codec": "pcma"}
            (root / "private/adapter.json").write_text(json.dumps(original))
            (root / "compose.json").write_text(json.dumps({"services": {"adapter": {
                "entrypoint": ["/opt/golive/adapter"], "volumes": ["./private:/private:ro"]}}}))
            payload = {"stack": directory, "token": "fixture-local-token"}

            def run():
                return subprocess.run([sys.executable, "-c", REMOTE_SCRIPT],
                                      input=json.dumps(payload), text=True, capture_output=True)

            for _ in range(2):
                result = run()
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertNotIn(payload["token"], result.stdout + result.stderr)
            self.assertEqual(json.loads((root / "private/adapter.json").read_text()), original)
            profile = root / "private/adapter-speko.json"
            self.assertEqual(json.loads(profile.read_text())["golive_url"], "http://127.0.0.1:8095")
            for path in (profile, root / "private/speko-adapter.env", root / "compose.speko.json"):
                self.assertEqual(path.stat().st_mode & 0o777, 0o600)
            payload["token"] = "different-token"
            self.assertNotEqual(run().returncode, 0)
            self.assertIn("fixture-local-token", (root / "private/speko-adapter.env").read_text())


if __name__ == "__main__":
    unittest.main()
