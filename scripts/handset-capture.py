#!/usr/bin/env python3
"""Serve the local handset microphone recorder. Audio never enters this server."""

import argparse
from http.server import SimpleHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
import webbrowser

ASSETS = Path(__file__).resolve().parent.parent / "web" / "handset-capture"


class Handler(SimpleHTTPRequestHandler):
    def __init__(self, *args, **kwargs):
        super().__init__(*args, directory=str(ASSETS), **kwargs)

    def allowed_host(self):
        # Limit this local-only endpoint to its explicit loopback origin.
        expected = "127.0.0.1:" + str(self.server.server_port)
        if self.headers.get("Host") != expected:
            self.send_error(403, "Use the printed loopback URL")
            return False
        return True

    def do_GET(self):
        if not self.allowed_host():
            return
        super().do_GET()

    def do_HEAD(self):
        if not self.allowed_host():
            return
        super().do_HEAD()

    def list_directory(self, path):
        self.send_error(404, "File not found")
        return None

    def end_headers(self):
        self.send_header("Cache-Control", "no-store")
        self.send_header("X-Content-Type-Options", "nosniff")
        self.send_header("Referrer-Policy", "no-referrer")
        self.send_header("Permissions-Policy", "microphone=(self)")
        super().end_headers()

    def log_message(self, format, *args):
        pass


def port(value):
    number = int(value)
    if not 0 <= number <= 65535:
        raise argparse.ArgumentTypeError("port must be between 0 and 65535")
    return number


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--port", type=port, default=0,
                        help="loopback port (default:0 chooses an available port)")
    parser.add_argument("--open", action="store_true", help="open the recorder in the default browser")
    args = parser.parse_args()
    if not (ASSETS / "index.html").is_file():
        parser.error("recorder assets are missing: " + str(ASSETS))
    try:
        server = ThreadingHTTPServer(("127.0.0.1", args.port), Handler)
    except OSError as exc:
        parser.error("Cannot bind the local server: " + str(exc) + ". Try --port 0.")
    with server:
        server.daemon_threads = True
        url = "http://127.0.0.1:" + str(server.server_port) + "/"
        print(url, flush=True)
        print("Local microphone recorder. Start recording explicitly in the page. Ctrl-C stops this server.", flush=True)
        if args.open:
            webbrowser.open(url)
        try:
            server.serve_forever()
        except KeyboardInterrupt:
            pass


if __name__ == "__main__":
    main()
