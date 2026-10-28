"""Synthetic training contracts; no GPU, MCP process, model or call data access."""
import asyncio
from contextlib import asynccontextmanager, redirect_stderr, redirect_stdout
import io
import json
from pathlib import Path
import sys
import tempfile
from types import ModuleType, SimpleNamespace
import unittest
from unittest.mock import patch

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
import train as t


class SyntheticTokenizer:
    eos_token_id = 100_000

    def apply_chat_template(self, messages, tokenize=False):
        return "".join(message["content"] for message in messages)

    def encode(self, text):
        eos = text.endswith(t.END_TOKEN)
        if eos:
            text = text[:-len(t.END_TOKEN)]
        return [ord(char) for char in text] + ([self.eos_token_id] if eos else [])


class BoundaryMergingTokenizer(SyntheticTokenizer):
    def encode(self, text):
        # A real BPE tokenizer can merge across the prompt/completion boundary.
        return super().encode(text.replace("，乙", "\ufffe"))


class TemporaryCase(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix="talker-train-test-")
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name).resolve()

    def write_rows(self, *, separator="\n", completion=None):
        data = self.root / "data"
        data.mkdir(exist_ok=True)
        row = {"prompt": t.ASSISTANT_PREFILL + "嗯，" + separator,
               "completion": completion if completion is not None else "乙。" + t.END_TOKEN}
        (data / "train.jsonl").write_text(json.dumps(row) + "\n", encoding="utf-8")
        return data

    def tokenizer_patch(self, tokenizer):
        transformer = ModuleType("transformers")
        transformer.AutoTokenizer = SimpleNamespace(from_pretrained=lambda *a, **k: tokenizer)
        return patch.dict(sys.modules, {"transformers": transformer})


class TokenizationTests(TemporaryCase):
    def test_passthrough_preserves_raw_chatml_and_supervision(self):
        data = self.write_rows()
        with self.tokenizer_patch(SyntheticTokenizer()):
            report = t.check_tokenization(self.root / "model", data, 1024)
        self.assertEqual(report["rows"], {"train": 1, "valid": 0, "test": 0})
        self.assertEqual(report["prefix_mismatches"], 0)
        self.assertEqual(report["min_supervised_tokens"], 3)
        self.assertEqual(report["over_length"], 0)

    def test_double_chat_template_is_rejected(self):
        class DoubleTemplate(SyntheticTokenizer):
            def apply_chat_template(self, messages, tokenize=False):
                return "USER:" + super().apply_chat_template(messages, tokenize)
        with self.tokenizer_patch(DoubleTemplate()):
            with self.assertRaisesRegex(ValueError, "template_did_not_preserve_raw_chatml"):
                t.check_tokenization(self.root, self.write_rows(), 1024)

    def test_bpe_boundary_mismatch_is_counted(self):
        with self.tokenizer_patch(BoundaryMergingTokenizer()):
            report = t.check_tokenization(self.root, self.write_rows(separator=""), 1024)
        self.assertEqual(report["prefix_mismatches"], 1)

    def test_separator_prevents_synthetic_boundary_merge(self):
        with self.tokenizer_patch(BoundaryMergingTokenizer()):
            report = t.check_tokenization(self.root, self.write_rows(), 1024)
        self.assertEqual(report["prefix_mismatches"], 0)

    def test_overlength_rows_are_counted_instead_of_silently_truncated(self):
        with self.tokenizer_patch(SyntheticTokenizer()):
            report = t.check_tokenization(self.root, self.write_rows(), 4)
        self.assertEqual(report["over_length"], 1)

    def test_completion_must_have_eos(self):
        with self.tokenizer_patch(SyntheticTokenizer()):
            with self.assertRaisesRegex(ValueError, "raw_chatml_contract_missing"):
                t.check_tokenization(self.root, self.write_rows(completion="乙。"), 1024)


class PreparationTests(TemporaryCase):
    def model(self):
        source = self.root / "source"
        source.mkdir()
        (source / "config.json").write_text('{"model_type":"qwen3"}')
        (source / "tokenizer_config.json").write_text('{"chat_template":"ORIGINAL"}')
        (source / "chat_template.jinja").write_text("ORIGINAL")
        (source / "model.safetensors").write_bytes(b"synthetic model weights")
        output = self.root / "output"
        output.mkdir()
        return source, output

    def test_original_preserved_weights_linked_and_reuse_valid(self):
        source, output = self.model()
        prepared = t.prepare_model(source, output)
        self.assertTrue((prepared / "model.safetensors").is_symlink())
        self.assertEqual((source / "chat_template.jinja").read_text(), "ORIGINAL")
        self.assertEqual((output / "original-tokenizer/chat_template.jinja").read_text(), "ORIGINAL")
        self.assertEqual(json.loads((prepared / "tokenizer_config.json").read_text())["chat_template"], t.PASSTHROUGH_TEMPLATE)
        provenance = json.loads((output / "model-provenance.json").read_text())
        self.assertEqual(len(provenance["files"]), 4)
        self.assertTrue(all(len(record["sha256"]) == 64 for record in provenance["files"]))
        self.assertEqual(t.prepare_model(source, output), prepared)

    def test_source_mutation_blocks_reuse(self):
        source, output = self.model()
        t.prepare_model(source, output)
        (source / "model.safetensors").write_bytes(b"changed source")
        with self.assertRaisesRegex(ValueError, "source_model_changed"):
            t.prepare_model(source, output)

    def test_prepared_template_mutation_blocks_reuse(self):
        source, output = self.model()
        prepared = t.prepare_model(source, output)
        (prepared / "chat_template.jinja").write_text("changed template")
        with self.assertRaisesRegex(ValueError, "prepared_template_changed"):
            t.prepare_model(source, output)

    def test_prepared_weight_link_must_keep_its_original_target(self):
        source, output = self.model()
        prepared = t.prepare_model(source, output)
        alternate = self.root / "alternate.safetensors"
        alternate.write_bytes((source / "model.safetensors").read_bytes())
        weight = prepared / "model.safetensors"
        weight.unlink()
        weight.symlink_to(alternate)
        with self.assertRaisesRegex(ValueError, "prepared_weights_changed"):
            t.prepare_model(source, output)

    def test_prepared_weight_link_cannot_be_replaced_by_a_file(self):
        source, output = self.model()
        prepared = t.prepare_model(source, output)
        weight = prepared / "model.safetensors"
        weight.unlink()
        weight.write_bytes((source / "model.safetensors").read_bytes())
        with self.assertRaisesRegex(ValueError, "prepared_weights_changed"):
            t.prepare_model(source, output)

    def test_copied_model_metadata_mutation_blocks_reuse(self):
        source, output = self.model()
        prepared = t.prepare_model(source, output)
        (prepared / "config.json").write_text('{"model_type":"altered"}')
        with self.assertRaisesRegex(ValueError, "prepared_metadata_changed"):
            t.prepare_model(source, output)

    def test_tokenizer_json_must_keep_only_intended_template_override(self):
        source, output = self.model()
        prepared = t.prepare_model(source, output)
        config = prepared / "tokenizer_config.json"
        original = json.loads(config.read_text())
        for mutation in ({**original, "chat_template": "altered"}, {**original, "added_token": "altered"}):
            with self.subTest(mutation_fields=len(mutation)):
                config.write_text(json.dumps(mutation))
                with self.assertRaisesRegex(ValueError, "prepared_metadata_changed"):
                    t.prepare_model(source, output)

    def test_copied_metadata_cannot_be_replaced_by_a_symlink(self):
        source, output = self.model()
        prepared = t.prepare_model(source, output)
        config = prepared / "config.json"
        config.unlink()
        config.symlink_to(source / "config.json")
        with self.assertRaisesRegex(ValueError, "prepared_metadata_changed"):
            t.prepare_model(source, output)

    def test_preserved_serving_template_mutation_blocks_reuse(self):
        source, output = self.model()
        t.prepare_model(source, output)
        (output / "original-tokenizer/chat_template.jinja").write_text("altered")
        with self.assertRaisesRegex(ValueError, "original_tokenizer_changed"):
            t.prepare_model(source, output)

    def test_different_source_blocks_reuse(self):
        source, output = self.model()
        t.prepare_model(source, output)
        other = self.root / "other"
        other.mkdir()
        (other / "config.json").write_text("{}")
        with self.assertRaisesRegex(ValueError, "prepared_model_source_mismatch"):
            t.prepare_model(other, output)

    def test_missing_weights_rejected(self):
        source, output = self.model()
        (source / "model.safetensors").unlink()
        with self.assertRaisesRegex(ValueError, "model_weights_missing"):
            t.prepare_model(source, output)


class MCPHarness:
    """Only the wire contracts used by train.run; never launches a subprocess."""
    def __init__(self, root, *, error_tool=None, verdict="Config looks good.\n\nconfig.yaml:\n{}", missing_tool=None):
        self.calls = []
        self.arguments = {}
        self.parameters = None
        self.error_tool = error_tool
        self.verdict = verdict
        self.missing_tool = missing_tool
        self.adapter = root / "adapters"
        self.adapter.mkdir()
        (self.adapter / "adapters.safetensors").write_bytes(b"synthetic adapter")
        self.terminal = {"id": "synthetic-run", "status": "completed", "exit_code": 0,
                         "iteration": 10, "adapter_path": str(self.adapter), "train_loss": 0.5,
                         "error": "SYNTHETIC_PRIVATE_ERROR"}

    def modules(self):
        harness = self
        class Session:
            def __init__(self, *args, **kwargs):
                pass
            async def __aenter__(self):
                return self
            async def __aexit__(self, *args):
                pass
            async def initialize(self):
                pass
            async def list_tools(self):
                return SimpleNamespace(tools=[SimpleNamespace(name=n) for n in t.REQUIRED_TOOLS if n != harness.missing_tool])
            async def call_tool(self, name, arguments):
                harness.calls.append(name)
                harness.arguments[name] = arguments
                text, data = "Passed.", {}
                if name == "check_training_config":
                    text = harness.verdict
                elif name == "start_training":
                    data = {"id": "synthetic-run"}
                elif name == "training_status":
                    data = harness.terminal
                if name == harness.error_tool:
                    return {"isError": True, "content": [{"type": "text", "text": "SYNTHETIC_PRIVATE_ERROR"}]}
                return {"content": [{"type": "text", "text": text}], "structuredContent": data}
        @asynccontextmanager
        async def stdio(parameters, errlog):
            harness.parameters = parameters
            yield (None, None)
        mcp = ModuleType("mcp")
        mcp.ClientSession = Session
        mcp.StdioServerParameters = lambda **kwargs: kwargs
        client = ModuleType("mcp.client")
        transport = ModuleType("mcp.client.stdio")
        transport.stdio_client = stdio
        return {"mcp": mcp, "mcp.client": client, "mcp.client.stdio": transport}


class ValidationGateTests(TemporaryCase):
    def run_harness(self, harness, *, validate_only=False, report=None):
        output = self.root / "output"
        output.mkdir(exist_ok=True)
        options = ["--model", str(self.root / "model"), "--data", str(self.root / "data"),
                   "--output", str(output), "--runs-dir", str(self.root / "runs"), "--iters", "10"]
        if validate_only:
            options.append("--validate-only")
        args = t.parser().parse_args(options)
        checked = report or {"rows": {"train": 9, "valid": 5, "test": 4}, "prefix_mismatches": 0, "over_length": 0}
        with patch.dict(sys.modules, harness.modules()), patch.object(t, "check_tokenization", return_value=checked), patch.object(t.importlib.metadata, "version", return_value="synthetic-version"):
            return asyncio.run(t.run(args, output, self.root / "prepared-model"))

    def test_required_validation_order_then_start_and_exact_run_monitoring(self):
        harness = MCPHarness(self.root)
        result = self.run_harness(harness)
        self.assertEqual(harness.calls, ["list_training_modes", "validate_model_support", "inspect_dataset", "check_training_config", "start_training", "training_status", "training_log"])
        self.assertTrue(result["successful"])
        self.assertNotIn("error", result)
        self.assertEqual(harness.arguments["training_status"], {"run_id": "synthetic-run"})
        self.assertEqual(harness.parameters["env"]["MLX_LM_LORA_PYTHON"], sys.executable)
        self.assertTrue(json.loads(harness.arguments["inspect_dataset"]["extra_config"])["mask_prompt"])

    def test_validate_only_never_starts(self):
        harness = MCPHarness(self.root)
        result = self.run_harness(harness, validate_only=True)
        self.assertEqual(result["status"], "validated")
        self.assertNotIn("start_training", harness.calls)

    def test_protocol_success_without_exact_config_verdict_blocks_start(self):
        harness = MCPHarness(self.root, verdict="Config has problems: invalid setting")
        with self.assertRaisesRegex(ValueError, "mcp_config_verdict_failed"):
            self.run_harness(harness)
        self.assertNotIn("start_training", harness.calls)

    def test_each_required_tool_error_stops_before_start(self):
        for tool in ("validate_model_support", "inspect_dataset", "check_training_config"):
            with self.subTest(tool=tool):
                case = self.root / tool
                case.mkdir()
                harness = MCPHarness(case, error_tool=tool)
                with self.assertRaisesRegex(ValueError, "mcp_tool_failed_" + tool):
                    self.run_harness(harness)
                self.assertEqual(harness.calls[-1], tool)
                self.assertNotIn("start_training", harness.calls)

    def test_missing_required_tool_blocks_all_tool_calls(self):
        harness = MCPHarness(self.root, missing_tool="inspect_dataset")
        with self.assertRaisesRegex(ValueError, "required_mcp_validation_tools_missing"):
            self.run_harness(harness)
        self.assertEqual(harness.calls, [])

    def test_prefix_failure_blocks_inspect_and_start(self):
        harness = MCPHarness(self.root)
        report = {"rows": {"train": 8, "valid": 4, "test": 4}, "prefix_mismatches": 1, "over_length": 0}
        with self.assertRaisesRegex(ValueError, "tokenizer_contract_failed"):
            self.run_harness(harness, report=report)
        self.assertEqual(harness.calls, ["list_training_modes", "validate_model_support"])

    def test_exit_zero_without_adapter_is_not_success(self):
        harness = MCPHarness(self.root)
        (harness.adapter / "adapters.safetensors").unlink()
        self.assertFalse(self.run_harness(harness)["successful"])


class PrivacyTests(TemporaryCase):
    def test_raw_exception_reaches_private_file_only(self):
        output = self.root / "output"
        argv = ["train.py", "--model", str(self.root / "model"), "--data", str(self.root / "data"), "--output", str(output)]
        stdout, stderr = io.StringIO(), io.StringIO()
        with patch.object(sys, "argv", argv), patch.object(t, "prepare_model", side_effect=ValueError("SYNTHETIC_PRIVATE_ERROR")), redirect_stdout(stdout), redirect_stderr(stderr):
            self.assertEqual(t.main(), 1)
        self.assertNotIn("SYNTHETIC_PRIVATE_ERROR", stdout.getvalue() + stderr.getvalue())
        self.assertEqual(json.loads(stdout.getvalue())["status"], "failed")
        diagnostics = output / "error-private.txt"
        self.assertIn("SYNTHETIC_PRIVATE_ERROR", diagnostics.read_text())
        self.assertEqual(diagnostics.stat().st_mode & 0o777, 0o600)
        self.assertEqual(output.stat().st_mode & 0o777, 0o700)

    def test_symlink_output_rejected(self):
        target = self.root / "target"
        target.write_text("unchanged")
        alias = self.root / "alias"
        alias.symlink_to(target)
        with self.assertRaisesRegex(ValueError, "symlink_output_rejected"):
            t.write_private(alias, "new")
        self.assertEqual(target.read_text(), "unchanged")


if __name__ == "__main__":
    unittest.main()
