#!/usr/bin/env python3
"""Fuse a local TALKER LoRA, convert with llama.cpp, and quantize privately."""
from __future__ import annotations

import argparse
import hashlib
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys


def private_dir(value: str) -> Path:
    path = Path(value).expanduser().resolve()
    project = Path(__file__).resolve().parents[2]
    if path == project or project in path.parents:
        raise ValueError("model artifacts must be outside the source repository")
    for root in (path, *path.parents):
        if not (root / ".git").exists():
            continue
        if root != Path.home():
            raise ValueError("model artifacts must be outside git worktrees")
        ignored = subprocess.run(["git", "-C", str(root), "check-ignore", "-q", str(path)])
        tracked = subprocess.run(["git", "-C", str(root), "ls-files", "--", str(path)],
                                 capture_output=True, text=True, check=True)
        if ignored.returncode or tracked.stdout.strip():
            raise ValueError("external artifacts must be git-ignored and untracked")
    path.mkdir(mode=0o700, parents=True, exist_ok=True)
    path.chmod(0o700)
    return path


def sha256(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        for chunk in iter(lambda: stream.read(8 * 1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--base-model", required=True, help="Original local BF16 Qwen3 checkpoint")
    parser.add_argument("--adapters", help="Trained adapter directory; omit only for the base-model baseline")
    parser.add_argument("--llama-cpp", required=True, help="Pinned llama.cpp source directory")
    parser.add_argument("--quantizer", required=True, help="llama-quantize from the same revision")
    parser.add_argument("--mlx-python", default=sys.executable)
    parser.add_argument("--convert-python", default=sys.executable)
    parser.add_argument("--quantization", choices=["Q4_K_M", "Q8_0", "Q5_K_M"], default="Q4_K_M")
    parser.add_argument("--output", required=True)
    args = parser.parse_args()
    os.umask(0o077)
    out = private_dir(args.output)
    base = Path(args.base_model).expanduser().resolve()
    source = Path(args.llama_cpp).expanduser().resolve()
    if not (base / "config.json").is_file() or not (source / "convert_hf_to_gguf.py").is_file():
        raise ValueError("local model or llama.cpp conversion source is missing")
    config = json.loads((base / "config.json").read_text())
    if config.get("model_type") != "qwen3" or config.get("quantization"):
        raise ValueError("export expects an unquantized Qwen3 base checkpoint")
    model = base
    commands = []

    def run(command: list[str], label: str) -> None:
        commands.append(command)
        log_path = out / (label + ".log")
        with log_path.open("w") as log:
            result = subprocess.run(command, stdout=log, stderr=subprocess.STDOUT)
        log_path.chmod(0o600)
        if result.returncode:
            raise RuntimeError(f"{label} failed (exit {result.returncode}); private log: {log_path}")

    if args.adapters:
        model = out / "fused"
        if model.exists():
            raise ValueError("fused destination already exists; use a new output directory")
        # Use the original tokenizer, not the training-only passthrough template.
        run([args.mlx_python, "-m", "mlx_lm.fuse", "--model", str(base),
             "--adapter-path", str(Path(args.adapters).expanduser().resolve()),
             "--save-path", str(model)], "fuse")
        for name in ("tokenizer_config.json", "tokenizer.json", "special_tokens_map.json", "chat_template.jinja"):
            if (base / name).is_file():
                shutil.copy2(base / name, model / name)
    fp16 = out / "talker-f16.gguf"
    quantized = out / f"talker-{args.quantization.lower()}.gguf"
    if fp16.exists() or quantized.exists():
        raise ValueError("GGUF destination already exists; use a new output directory")
    run([args.convert_python, str(source / "convert_hf_to_gguf.py"), str(model),
         "--outfile", str(fp16), "--outtype", "f16"], "convert")
    run([args.quantizer, str(fp16), str(quantized), args.quantization], "quantize")
    revision = subprocess.run(["git", "-C", str(source), "rev-parse", "HEAD"],
                              check=True, capture_output=True, text=True).stdout.strip()
    report = {"base_model": str(base), "adapters": args.adapters, "llama_cpp_revision": revision,
              "quantization": args.quantization, "commands": commands,
              "gguf": str(quantized), "bytes": quantized.stat().st_size,
              "sha256": sha256(quantized), "baseline": not bool(args.adapters)}
    (out / "export.json").write_text(json.dumps(report, indent=2) + "\n")
    for item in out.rglob("*"):
        if item.is_file() and not item.is_symlink():
            item.chmod(0o600)
    print(json.dumps(report, indent=2))
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except (ValueError, RuntimeError) as exc:
        print(str(exc), file=sys.stderr)
        raise SystemExit(2)
