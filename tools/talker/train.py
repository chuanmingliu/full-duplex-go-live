#!/usr/bin/env python3
"""Prepare raw ChatML SFT and run it through the validated local MLX MCP API.

Run with the Python belonging to mlx-lm-lora[mcp]. All customer-bearing
results and diagnostics are written to private paths; stdout is a safe summary.
"""
from __future__ import annotations

import argparse
import asyncio
import contextlib
import hashlib
import importlib.metadata
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import traceback

PASSTHROUGH_TEMPLATE = "{{ messages | map(attribute='content') | join('') }}"
ASSISTANT_PREFILL = "<|im_start|>assistant\n<think>\n\n</think>\n\n"
END_TOKEN = "<|im_end|>"
TERMINAL = {"completed", "failed", "stopped"}
REQUIRED_TOOLS = {
    "list_training_modes", "validate_model_support", "inspect_dataset",
    "check_training_config", "start_training", "training_status", "training_log",
}


def private_output_dir(value: str | Path) -> Path:
    target = Path(value).expanduser().resolve()
    project = Path(__file__).resolve().parents[2]
    if target == project or project in target.parents:
        raise ValueError("private_output_inside_project")
    for root in (target, *target.parents):
        if not (root / ".git").exists():
            continue
        if root != Path.home():
            raise ValueError("private_output_inside_worktree")
        ignored = subprocess.run(["git", "-C", str(root), "check-ignore", "-q", str(target)], capture_output=True)
        tracked = subprocess.run(["git", "-C", str(root), "ls-files", "--", str(target)], capture_output=True)
        if ignored.returncode != 0 or tracked.returncode != 0 or tracked.stdout:
            raise ValueError("home_private_output_must_be_ignored_and_untracked")
    target.mkdir(parents=True, exist_ok=True, mode=0o700)
    target.chmod(0o700)
    return target


def write_private(path: Path, text: str) -> None:
    if path.is_symlink():
        raise ValueError("symlink_output_rejected")
    fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600)
    os.fchmod(fd, 0o600)
    with os.fdopen(fd, "w", encoding="utf-8") as stream:
        stream.write(text)


def save_json(path: Path, value: object) -> None:
    write_private(path, json.dumps(value, ensure_ascii=False, indent=2) + "\n")


def sha256(path: Path) -> str:
    h = hashlib.sha256()
    with path.open("rb") as stream:
        for chunk in iter(lambda: stream.read(8 << 20), b""):
            h.update(chunk)
    return h.hexdigest()


def prepare_model(source: Path, output: Path) -> Path:
    """Copy tokenizer metadata, link immutable weights, preserve serving template."""
    source = source.expanduser().resolve(strict=True)
    if not source.is_dir() or not (source / "config.json").is_file():
        raise ValueError("local_model_directory_required")
    prepared = output / "prepared-model"
    original = output / "original-tokenizer"
    provenance_path = output / "model-provenance.json"
    if prepared.exists():
        if prepared.is_symlink() or original.is_symlink():
            raise ValueError("prepared_directory_changed")
        provenance = json.loads(provenance_path.read_text())
        if provenance["source"] != str(source):
            raise ValueError("prepared_model_source_mismatch")
        for record in provenance["files"]:
            source_file = source / record["name"]
            prepared_file = prepared / record["name"]
            if sha256(source_file) != record["sha256"]:
                raise ValueError("source_model_changed")
            if source_file.suffix in {".safetensors", ".bin"}:
                if not prepared_file.is_symlink() or prepared_file.resolve() != source_file.resolve():
                    raise ValueError("prepared_weights_changed")
                continue
            if prepared_file.is_symlink() or not prepared_file.is_file():
                raise ValueError("prepared_metadata_changed")
            if source_file.name == "tokenizer_config.json":
                expected = json.loads(source_file.read_text())
                expected["chat_template"] = PASSTHROUGH_TEMPLATE
                if json.loads(prepared_file.read_text()) != expected:
                    raise ValueError("prepared_metadata_changed")
            elif source_file.name != "chat_template.jinja" and sha256(prepared_file) != record["sha256"]:
                raise ValueError("prepared_metadata_changed")
            if source_file.name == "tokenizer_config.json" or ("chat_template" in source_file.name and source_file.suffix == ".jinja"):
                preserved = original / source_file.name
                if preserved.is_symlink() or not preserved.is_file() or sha256(preserved) != record["sha256"]:
                    raise ValueError("original_tokenizer_changed")
        template_file = prepared / "chat_template.jinja"
        if template_file.is_symlink() or not template_file.is_file() or template_file.read_text() != PASSTHROUGH_TEMPLATE:
            raise ValueError("prepared_template_changed")
        return prepared
    prepared.mkdir(mode=0o700)
    original.mkdir(mode=0o700)
    records = []
    for path in sorted(source.iterdir()):
        if not path.is_file() or path.suffix not in {".json", ".jinja", ".model", ".txt", ".safetensors", ".bin"}:
            continue
        record = {"name": path.name, "sha256": sha256(path), "bytes": path.stat().st_size}
        records.append(record)
        destination = prepared / path.name
        if path.suffix in {".safetensors", ".bin"}:
            destination.symlink_to(path.resolve())
        else:
            shutil.copyfile(path, destination)
            destination.chmod(0o600)
        if path.name == "tokenizer_config.json" or ("chat_template" in path.name and path.suffix == ".jinja"):
            shutil.copyfile(path, original / path.name)
            (original / path.name).chmod(0o600)
    if not any(r["name"].endswith((".safetensors", ".bin")) for r in records):
        raise ValueError("model_weights_missing")
    config_path = prepared / "tokenizer_config.json"
    config = json.loads(config_path.read_text())
    config["chat_template"] = PASSTHROUGH_TEMPLATE
    save_json(config_path, config)
    write_private(prepared / "chat_template.jinja", PASSTHROUGH_TEMPLATE)
    save_json(provenance_path, {
        "version": 1, "source": str(source), "files": records,
        "source_manifest_sha256": hashlib.sha256(json.dumps(records, sort_keys=True).encode()).hexdigest(),
        "original_tokenizer_dir": str(original), "prepared_model": str(prepared),
        "template": "passthrough_raw_chatml", "weights": "symlinked_unmodified",
    })
    return prepared


def check_tokenization(model: Path, data: Path, max_seq_length: int) -> dict:
    """Fail closed on double templates, BPE boundary mismatch, or truncation."""
    from transformers import AutoTokenizer
    tokenizer = AutoTokenizer.from_pretrained(str(model), trust_remote_code=False, local_files_only=True)
    result = {"rows": {}, "max_sequence_tokens": 0, "max_prompt_tokens": 0,
              "min_supervised_tokens": None, "prefix_mismatches": 0, "over_length": 0}
    for split in ("train", "valid", "test"):
        path = data / f"{split}.jsonl"
        if not path.exists():
            result["rows"][split] = 0
            continue
        count = 0
        with path.open(encoding="utf-8") as stream:
            for line in stream:
                if not line.strip():
                    continue
                row = json.loads(line)
                prompt, completion = row["prompt"], row["completion"]
                if not isinstance(prompt, str) or not isinstance(completion, str):
                    raise ValueError("string_prompt_completion_required")
                if ASSISTANT_PREFILL not in prompt or not completion.endswith(END_TOKEN):
                    raise ValueError("raw_chatml_contract_missing")
                messages = [{"role": "user", "content": prompt}, {"role": "assistant", "content": completion}]
                full = tokenizer.apply_chat_template(messages, tokenize=False)
                prefix = tokenizer.apply_chat_template(messages[:1], tokenize=False)
                if full != prompt + completion or prefix != prompt:
                    raise ValueError("template_did_not_preserve_raw_chatml")
                full_ids, prefix_ids = tokenizer.encode(full), tokenizer.encode(prefix)
                result["prefix_mismatches"] += int(full_ids[:len(prefix_ids)] != prefix_ids)
                result["over_length"] += int(len(full_ids) > max_seq_length)
                supervised = len(full_ids) - len(prefix_ids)
                if supervised <= 0 or full_ids[-1] != tokenizer.eos_token_id:
                    raise ValueError("supervised_completion_or_eos_missing")
                result["max_sequence_tokens"] = max(result["max_sequence_tokens"], len(full_ids))
                result["max_prompt_tokens"] = max(result["max_prompt_tokens"], len(prefix_ids))
                old_min = result["min_supervised_tokens"]
                result["min_supervised_tokens"] = supervised if old_min is None else min(old_min, supervised)
                count += 1
        result["rows"][split] = count
    return result


def payload(result) -> dict:
    if isinstance(result, dict):
        return result
    return result.model_dump(mode="json", by_alias=True)


def structured(result: dict) -> dict:
    value = result.get("structuredContent", result.get("structured_content", {}))
    return value if isinstance(value, dict) else {}


def result_text(result: dict) -> str:
    return "\n".join(c.get("text", "") for c in result.get("content", []) if c.get("type") == "text")


async def run(args, output: Path, prepared: Path) -> dict:
    from mcp import ClientSession, StdioServerParameters
    from mcp.client.stdio import stdio_client
    runs_dir = private_output_dir(args.runs_dir)
    mcp_output = private_output_dir(output / "mcp")
    params = StdioServerParameters(
        command=args.server_command,
        args=["--transport", "stdio", "--runs-dir", str(runs_dir)],
        # Preserve the venv path: resolving its Python symlink loses site-packages.
        env={**os.environ, "MLX_LM_LORA_PYTHON": sys.executable,
             "HF_HUB_DISABLE_PROGRESS_BARS": "1", "TOKENIZERS_PARALLELISM": "false"},
    )
    with (output / "mcp-server.log").open("a", encoding="utf-8") as server_log:
        async with stdio_client(params, errlog=server_log) as (reader, writer):
            async with ClientSession(reader, writer) as session:
                await session.initialize()
                available = {t.name for t in (await session.list_tools()).tools}
                if not REQUIRED_TOOLS.issubset(available):
                    raise ValueError("required_mcp_validation_tools_missing")
                calls = 0

                async def call(name, arguments=None):
                    nonlocal calls
                    value = payload(await session.call_tool(name, arguments or {}))
                    calls += 1
                    save_json(mcp_output / f"{calls:04d}-{name}.json", value)
                    if value.get("isError", value.get("is_error", False)):
                        raise ValueError(f"mcp_tool_failed_{name}")
                    return value

                await call("list_training_modes")
                await call("validate_model_support", {"model": str(prepared)})
                checked = check_tokenization(prepared, Path(args.data).expanduser().resolve(), args.max_seq_length)
                save_json(output / "tokenizer-check.json", checked)
                if not checked["rows"]["train"] or checked["prefix_mismatches"] or checked["over_length"]:
                    raise ValueError("tokenizer_contract_failed")
                # Installed SFT iterator sorts by length and drops a partial
                # minibatch. Do not call ceil(rows/batch) an epoch.
                batches_per_epoch = checked["rows"]["train"] // args.batch_size
                if not batches_per_epoch:
                    raise ValueError("training_rows_below_batch_size")
                iters = args.iters if args.iters is not None else batches_per_epoch * args.epochs
                extra = {"mask_prompt": True, "sft_loss_type": "nll", "seed": args.seed,
                         "steps_per_report": args.report_every, "steps_per_eval": args.eval_every,
                         "save_every": min(args.save_every, iters), "val_batches": args.val_batches,
                         "grad_checkpoint": args.grad_checkpoint,
                         "lora_parameters": {"rank": args.rank, "scale": args.scale, "dropout": args.dropout}}
                request = {"model": str(prepared), "data": str(Path(args.data).expanduser().resolve()),
                           "train_mode": "sft", "train_type": "lora", "iters": iters,
                           "batch_size": args.batch_size, "learning_rate": args.learning_rate,
                           "num_layers": args.num_layers, "max_seq_length": args.max_seq_length,
                           "quantize_bits": 0, "fuse": False, "extra_config": json.dumps(extra)}
                versions = {}
                for package in ("mlx-lm-lora", "mlx-lm", "mlx", "mcp", "transformers", "tokenizers"):
                    versions[package] = importlib.metadata.version(package)
                save_json(output / "train-request.json", {"request": request, "python": sys.executable,
                          "server_command": args.server_command, "runs_dir": str(runs_dir), "versions": versions,
                          "iteration_basis": "explicit" if args.iters is not None else "epochs_times_floor_rows_over_batch",
                          "batches_per_epoch": batches_per_epoch,
                          "dropped_training_rows_per_pass": checked["rows"]["train"] % args.batch_size,
                          "dropped_validation_rows": checked["rows"]["valid"] % args.batch_size,
                          "epochs": args.epochs if args.iters is None else None})
                await call("inspect_dataset", {k: request[k] for k in ("data", "model", "train_mode", "extra_config")})
                config_result = await call("check_training_config", request)
                verdict = result_text(config_result)
                if not verdict.startswith("Config looks good."):
                    raise ValueError("mcp_config_verdict_failed")
                write_private(output / "checked-config.txt", verdict)
                warning_count = verdict.split("\n\nconfig.yaml:", 1)[0].count("\n- ")
                if args.validate_only:
                    return {"status": "validated", "iterations": iters, "rows": checked["rows"],
                            "config_warning_count": warning_count, "output": str(output), "prepared_model": str(prepared)}
                started = structured(await call("start_training", {**request, "name": args.name}))
                run_id = started.get("id")
                if not isinstance(run_id, str) or not run_id:
                    raise ValueError("mcp_start_missing_run_id")
                save_json(output / "started-run.json", started)
                while True:
                    status = structured(await call("training_status", {"run_id": run_id}))
                    if status.get("id") != run_id:
                        raise ValueError("mcp_status_run_id_mismatch")
                    if status.get("status") in TERMINAL:
                        await call("training_log", {"run_id": run_id, "lines": 200})
                        break
                    await asyncio.sleep(args.poll_seconds)
                safe = {key: status[key] for key in ("id", "status", "exit_code", "iteration", "train_loss", "val_loss", "adapter_path", "log_path") if key in status}
                safe.update(output=str(output), prepared_model=str(prepared), config_warning_count=warning_count)
                adapter_dir = Path(status.get("adapter_path") or "")
                artifacts = list(adapter_dir.glob("*.safetensors")) if adapter_dir.is_dir() else []
                safe["adapter_files"] = len([p for p in artifacts if p.stat().st_size > 0])
                safe["successful"] = bool(status.get("status") == "completed" and status.get("exit_code") == 0
                                          and safe["adapter_files"] and status.get("iteration", 0) >= iters)
                return safe


def parser() -> argparse.ArgumentParser:
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument("--model", required=True, help="Local unmodified BF16 Qwen checkpoint")
    p.add_argument("--data", required=True, help="Private train/valid/test JSONL directory")
    p.add_argument("--output", required=True, help="Private runner output directory outside worktrees")
    p.add_argument("--runs-dir", default="~/.local/share/golive-talker/runs")
    p.add_argument("--server-command", default="mlx-lm-lora-mcp")
    p.add_argument("--name", default="local-talker")
    p.add_argument("--iters", type=int)
    p.add_argument("--epochs", type=int, default=5, help="Used only without --iters; floor(train_rows/batch)*epochs (backend drops remainder)")
    p.add_argument("--batch-size", type=int, default=4)
    p.add_argument("--num-layers", type=int, default=16)
    p.add_argument("--learning-rate", type=float, default=1e-4)
    p.add_argument("--max-seq-length", type=int, default=1024)
    p.add_argument("--rank", type=int, default=8)
    p.add_argument("--scale", type=float, default=16.0)
    p.add_argument("--dropout", type=float, default=0.0)
    p.add_argument("--seed", type=int, default=42)
    p.add_argument("--report-every", type=int, default=10)
    p.add_argument("--eval-every", type=int, default=50)
    p.add_argument("--save-every", type=int, default=50)
    p.add_argument("--val-batches", type=int, default=10)
    p.add_argument("--poll-seconds", type=float, default=10.0)
    p.add_argument("--grad-checkpoint", action="store_true")
    p.add_argument("--validate-only", action="store_true")
    return p


def main() -> int:
    args = parser().parse_args()
    os.umask(0o077)
    output = None
    try:
        if args.batch_size <= 0 or args.epochs <= 0 or (args.iters is not None and args.iters <= 0) or not 0 < args.poll_seconds <= 60:
            raise ValueError("positive_training_budget_required")
        output = private_output_dir(args.output)
        # Tokenizer imports and MCP diagnostics must never reach console output.
        with (output / "runner.log").open("a", encoding="utf-8") as log:
            with contextlib.redirect_stdout(log), contextlib.redirect_stderr(log):
                prepared = prepare_model(Path(args.model), output)
                summary = asyncio.run(run(args, output, prepared))
        save_json(output / "summary.json", summary)
        print(json.dumps(summary, ensure_ascii=False))
        return 0 if summary.get("status") == "validated" or summary.get("successful") else 1
    except Exception:
        if output:
            write_private(output / "error-private.txt", traceback.format_exc())
        print(json.dumps({"status": "failed", "private_diagnostics": str(output / "error-private.txt") if output else None}))
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
