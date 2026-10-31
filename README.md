# Local Talker

This branch maintains the local short-clause Talker experiment: extract eligible
conversation examples, fine-tune Qwen3-0.6B, export a GGUF model, and measure how
quickly it produces one complete Chinese clause after a cached opener.

**Scope:** `codex/local-talker` in `full-duplex-go-live`. Talker generation timing
excludes ASR, TTS and handset playback. The experiment is not wired into the live
phone engine. See the [full workflow](tools/talker/README.md) and
[recorded results and limitations](tools/talker/RESULTS.md).

## Owned code

| Path | Responsibility |
| --- | --- |
| `tools/talker/extract.py`, `prompt.py` | Evidence-based example extraction and matching training/serving prompts |
| `tools/talker/train.py`, `select_checkpoint.py` | Validated MLX LoRA training and validation-loss checkpoint selection |
| `tools/talker/export.py`, `build_runtime.sh` | Model export and pinned llama.cpp runtime build |
| `tools/talker/benchmark.py`, `evaluate.py` | Timing, structural checks and separately authorized semantic judgment |
| `tools/talker/test_extract.py`, `tools/talker/tests/` | Synthetic regression tests |

The rest of this repository is shared golive history. This branch does not own
production telephony deployment or the simulator gateway.

## Build and check

Training requires an Apple-silicon MLX environment; export additionally uses
PyTorch, Transformers and the pinned llama.cpp converter/quantizer. The runbook
records the tested versions and full commands. Runtime/quantizer revision:
`15fa3c493bfcd040b5f4dcb29e1c998a0846de16` (`b8920`).

From this branch's checkout:

```sh
python3 -m unittest discover -s tools/talker -p 'test*.py' -v
python3 -m unittest discover -s tools/talker/tests -p 'test*.py' -v
```

For the CPU box, the existing build helper is:

```sh
CMAKE="$HOME/edge/hfcli/bin/cmake" tools/talker/build_runtime.sh
```

Do not substitute CPU results for native Metal results or compare differently
prepared prompt-cache conditions as if they were equivalent.

## Serving and private state

The observed Mac service uses native llama.cpp/Metal at port 18081, model alias
`local-talker`. Health: `GET http://127.0.0.1:18081/health`. Its existing launcher
is outside Git at `~/.local/share/golive-talker/mac-service/serve.py`; exact
arguments and PID are in that directory's private `service.json`, with output in
`server.log`. Start that helper only after checking the port is unused. Stop only
the verified matching process; it is detached and has no boot autostart in the
recorded setup.

Keep datasets, call evidence, adapters, model weights, predictions and logs under
`~/.local/share/golive-talker` on Mac or `~/golive-talker` on the box, outside Git.
Preserve the model hash, export manifest, runtime version and launch arguments
as one rollback unit. A new checkout does not deploy or restart the model.

The recorded CPU experiment did not reliably produce valid clauses below
300 ms. Mac measurements were faster but semantic acceptance remained
unmeasured in the committed report. Neither result proves the project's
1200 ms end-to-end handset target.
