import os
import shlex
from pathlib import Path
import tempfile
import unittest

from configure_speko import save_key


class ConfigureSpekoTest(unittest.TestCase):
    def test_opaque_keys_and_paste_whitespace(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "speko.env"
            for key in ("sk_speko_mock", "sk-other_mock", "opaque123", "~mock+/value=="):
                with self.subTest(key=key):
                    save_key(path, "\u00a0 \t" + key + "\r\n\u00a0")
                    line = next(line for line in path.read_text().splitlines()
                                if line.startswith("SPEKO_API_KEY="))
                    value = line.split("=", 1)[1]
                    self.assertEqual(shlex.split(value), [key])
                    # Match the service loader's paired-quote stripping.
                    self.assertEqual(value[1:-1], key)

    def test_bad_keys_leave_file_unchanged(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "speko.env"
            save_key(path, "initial-token")
            original = path.read_text()
            for key in ("", "\u00a0 \n", "Bearer token", "'token'", "a b", "a\x00b",
                        "a\nb", "$(echo unsafe)", "token;command"):
                with self.subTest(key=key):
                    with self.assertRaises(ValueError):
                        save_key(path, key)
                    self.assertEqual(path.read_text(), original)

    def test_update_preserves_settings_and_tokens(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "speko.env"
            path.write_text("# private config\nOTHER=value\nSPEKO_API_KEY=old\nGOLIVE_AUTH_TOKEN=stable\n")
            save_key(path, "sk_speko_mock1")
            content = path.read_text()
            self.assertIn("OTHER=value", content)
            self.assertIn("GOLIVE_AUTH_TOKEN=stable", content)
            self.assertIn("GOLIVE_ADAPTER_TOKEN=stable", content)
            self.assertEqual(content.count("SPEKO_API_KEY="), 1)
            self.assertEqual(os.stat(path).st_mode & 0o777, 0o600)
            save_key(path, "sk_speko_mock2")
            self.assertNotIn("mock1", path.read_text())

    def test_new_file_and_invalid_input(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "private/speko.env"
            save_key(path, "sk_speko_mock")
            values = dict(line.split("=", 1) for line in path.read_text().splitlines())
            self.assertEqual(values["GOLIVE_AUTH_TOKEN"], values["GOLIVE_ADAPTER_TOKEN"])
            self.assertGreaterEqual(len(values["GOLIVE_AUTH_TOKEN"]), 32)
            original = path.read_text()
            with self.assertRaises(ValueError):
                save_key(path, "sk_speko_invalid\nOTHER=value")
            self.assertEqual(path.read_text(), original)
            link = Path(directory) / "link.env"
            link.symlink_to(path)
            with self.assertRaises(ValueError):
                save_key(link, "sk_speko_mock")


if __name__ == "__main__":
    unittest.main()
