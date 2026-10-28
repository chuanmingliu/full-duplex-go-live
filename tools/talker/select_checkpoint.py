#!/usr/bin/env python3
"""Select the lowest validation-loss saved LoRA, without consulting test data."""
import argparse
import hashlib
import json
from pathlib import Path
import re
import shutil

from export import private_dir


def select(run: Path, output: Path) -> dict:
    if (output / "adapters.safetensors").exists():
        raise ValueError("selected checkpoint already exists; use a new output directory")
    log = (run / "train.log").read_text(errors="replace")
    losses = [(int(step), float(loss)) for step, loss in
              re.findall(r"Iter\s+(\d+):\s+Val loss\s+([0-9.eE+-]+)", log)]
    final_steps = [int(step) for step in re.findall(r"Iter\s+(\d+):", log)]
    if not final_steps:
        raise ValueError("no completed training iterations in log")
    final = max(final_steps)
    candidates = []
    for step, loss in losses:
        weights = run / "adapters" / f"{step:07d}_adapters.safetensors"
        if not weights.exists() and step == final:
            weights = run / "adapters" / "adapters.safetensors"
        if weights.is_file() and weights.stat().st_size and 0 <= loss < float("inf"):
            candidates.append((loss, step, weights))
    if not candidates:
        raise ValueError("no validation loss matches a saved checkpoint")
    loss, step, source = min(candidates)
    destination = output / "adapters.safetensors"
    shutil.copyfile(source, destination)
    shutil.copyfile(run / "adapters" / "adapter_config.json", output / "adapter_config.json")
    report = {"criterion": "minimum validation loss among saved checkpoints; no test data",
              "iteration": step, "validation_loss": loss, "run": str(run),
              "source": str(source), "selected_adapter": str(output),
              "sha256": hashlib.sha256(destination.read_bytes()).hexdigest(),
              "validation_curve": [{"iteration": s, "loss": v} for s, v in losses]}
    (output / "selection.json").write_text(json.dumps(report, indent=2) + "\n")
    for path in output.iterdir():
        if path.is_file():
            path.chmod(0o600)
    return report


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--run", required=True)
    parser.add_argument("--output", required=True)
    args = parser.parse_args()
    print(json.dumps(select(Path(args.run).expanduser().resolve(), private_dir(args.output)), indent=2))
