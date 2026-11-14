"""Download only the pinned public artifacts; runtime never downloads models."""
import hashlib
import json
import os
from pathlib import Path
import urllib.request


def fetch(directory):
    lock = json.loads(Path(__file__).with_name("model.lock.json").read_text())
    directory = Path(directory)
    directory.mkdir(parents=True, exist_ok=True)
    for name, digest in lock["files"].items():
        path = directory / name
        if path.exists():
            if hashlib.sha256(path.read_bytes()).hexdigest() != digest:
                raise ValueError("existing_model_checksum_mismatch")
            continue
        url = f"https://huggingface.co/{lock['repository']}/resolve/{lock['revision']}/{name}"
        with urllib.request.urlopen(url, timeout=60) as response:
            data = response.read(10_000_001)
        if len(data) > 10_000_000 or hashlib.sha256(data).hexdigest() != digest:
            raise ValueError("download_checksum_mismatch")
        temporary = path.with_suffix(path.suffix + ".download")
        try:
            with temporary.open("xb") as f:
                f.write(data)
                f.flush()
                os.fsync(f.fileno())
            temporary.chmod(0o644)
            os.replace(temporary, path)
        finally:
            temporary.unlink(missing_ok=True)
    print(json.dumps({"verified": True, "revision": lock["revision"]}))


if __name__ == "__main__":
    import argparse
    parser = argparse.ArgumentParser()
    parser.add_argument("--directory", default="models")
    fetch(parser.parse_args().directory)
