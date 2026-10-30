#!/usr/bin/env python3
"""The recorder server exposes only its static assets on loopback."""

import http.client
import importlib.util
from pathlib import Path
import tempfile
import threading
import unittest

spec = importlib.util.spec_from_file_location(
    "handset_capture", Path(__file__).with_name("handset-capture.py"))
capture = importlib.util.module_from_spec(spec)
spec.loader.exec_module(capture)


class LocalRecorderServer(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.root = Path(self.temp.name)
        assets = self.root / "assets"
        assets.mkdir()
        (assets / "index.html").write_text("recorder fixture")
        (assets / "folder").mkdir()
        (self.root / "private.txt").write_text("outside assets")
        self.original_assets = capture.ASSETS
        capture.ASSETS = assets
        self.server = capture.ThreadingHTTPServer(("127.0.0.1", 0), capture.Handler)
        self.worker = threading.Thread(target=self.server.serve_forever, daemon=True)
        self.worker.start()

    def tearDown(self):
        self.server.shutdown()
        self.server.server_close()
        self.worker.join()
        capture.ASSETS = self.original_assets
        self.temp.cleanup()

    def request(self, path, host=None, method="GET"):
        connection = http.client.HTTPConnection(*self.server.server_address, timeout=3)
        try:
            connection.request(method, path, headers={} if host is None else {"Host": host})
            response = connection.getresponse()
            return response.status, dict(response.getheaders()), response.read()
        finally:
            connection.close()

    def test_loopback_page_has_no_cache(self):
        self.assertEqual(self.server.server_address[0], "127.0.0.1")
        status, headers, body = self.request("/")
        self.assertEqual((status, body), (200, b"recorder fixture"))
        self.assertEqual(headers["Cache-Control"], "no-store")
        self.assertEqual(headers["Permissions-Policy"], "microphone=(self)")

    def test_other_host_is_rejected(self):
        for method in ("GET", "HEAD"):
            self.assertEqual(self.request("/", "untrusted.example", method)[0], 403)

    def test_no_directory_inventory_or_parent_file(self):
        for path in ("/folder/", "/../private.txt", "/%2e%2e/private.txt"):
            status, _, body = self.request(path)
            self.assertEqual(status, 404)
            self.assertNotIn(b"outside assets", body)


if __name__ == "__main__":
    unittest.main()
