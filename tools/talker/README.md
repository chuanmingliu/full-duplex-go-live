# Local TALKER experiment

Train Qwen3-0.6B to supply one short Chinese clause after the engine's cached
opener. This is an offline experiment; it does not change the live phone engine.
The measured interval starts when the early-final text and chosen opener are
available and ends at the first complete clause. ASR, synthesis and playback are
outside that interval. The CPU deployment target is below 300 ms.

## Privacy and artifacts

Keep archives, pairs, weights, adapters, predictions, judge requests and logs
outside the source checkout. On this Mac use
`$HOME/.local/share/golive-talker`; on the box use `$HOME/golive-talker`.
If the home directory is itself a Git repository, the private root must be
explicitly ignored and untracked. For a new private root:

```sh
umask 077
mkdir -p "$HOME/.local/share/golive-talker"
printf '*\n' > "$HOME/.local/share/golive-talker/.gitignore"
```

Scripts print aggregate results. Do not paste private diagnostics or examples
into chat, desk notes, commits or reviews. Never publish the resulting weights.
Training uses a local MCP server; no customer text is sent to a training API.
Cloud judgment requires separately confirmed permission to send excerpts.

## Extract

`call.json` plus `golive.log` are the authoritative structured inputs;
`transcript.txt` is a display rendering and is not parsed. Join the opener that
actually played to the same turn ID; a planned opener is insufficient. Keep
the early-final caller text, previous conversation, opener, short clause and
full answer privately. Exclude tool calls, revisions, interrupted/abandoned
answers, missing provenance and overlong first clauses. Do not manufacture a
short target by cutting a longer clause at character 12.

```sh
python3 tools/talker/extract.py \
  --archive "$HOME/.local/share/golive-talker/archive/calls" \
  --output "$HOME/.local/share/golive-talker/data-v2"
```

`train.jsonl`, `valid.jsonl`, `test.jsonl` are raw ChatML prompt/completion rows;
the corresponding `*.pairs.jsonl` retain provenance and full reference answers.
`extraction-report.json` has aggregate exclusion and split counts. Splits are
deterministic and disjoint by call, with exact duplicate removal. Real/simulated
labels remain unknown unless an authoritative private manifest supplies them.

## Train on Apple silicon

Use the Python environment containing `mlx-lm-lora-mcp`. This run uses
mlx-lm-lora 5.10.1, mlx-lm 0.32.2, mlx 0.32.4, transformers 5.20.2 and mcp 2.3.0.
The training script discovers and requires model/dataset validation tools;
missing or failed checks stop training. The prepared tokenizer preserves raw
ChatML, preventing the completion loader from applying a second chat template.
Every row is checked for the exact prompt token prefix, EOS and truncation.
Original model files are not modified.

```sh
TALKER_PY="$HOME/Documents/ChatGPT/RSI-loop/.venv-mlx-lm-lora/bin/python"
TALKER_MCP="$HOME/Documents/ChatGPT/RSI-loop/.venv-mlx-lm-lora/bin/mlx-lm-lora-mcp"
TALKER_BASE="$HOME/.cache/huggingface/hub/models--mlx-community--Qwen3-0.6B-bf16/snapshots/42096995f6402fde107068cf530136fe64b604f8"
"$TALKER_PY" tools/talker/train.py \
  --model "$TALKER_BASE" --data "$HOME/.local/share/golive-talker/data-v2" \
  --output "$HOME/.local/share/golive-talker/training-v2" \
  --runs-dir "$HOME/.local/share/golive-talker/runs" \
  --server-command "$TALKER_MCP" --epochs 5 --batch-size 4 --val-batches -1
```

`--validate-only` performs the mandatory checks without training. `--iters N`
sets an explicit budget. The verified backend drops incomplete batches; the
epoch calculation uses `floor(train_rows / batch_size)`. Batch 1 avoids dropped
rows. Saved requests record the row counts, iterations, dropped rows, package
versions and source checkpoint hashes. Training is completion-masked LoRA,
16 layers, rank 8, scale 16, learning rate 0.0001 and seed 42 by default.

## Export and build

The source and quantizer must both be llama.cpp
`15fa3c493bfcd040b5f4dcb29e1c998a0846de16` (`b8920`). The box build requires no
sudo and uses CPU only:

```sh
CMAKE="$HOME/edge/hfcli/bin/cmake" tools/talker/build_runtime.sh
```

On the Mac, select a saved checkpoint using validation loss only. The converter
Python must have PyTorch, transformers, numpy and sentencepiece.

```sh
python3 tools/talker/select_checkpoint.py \
  --run "$HOME/.local/share/golive-talker/runs/$TALKER_RUN_ID" \
  --output "$HOME/.local/share/golive-talker/models/selected-adapter"
TALKER_ADAPTERS="$HOME/.local/share/golive-talker/models/selected-adapter"
```

```sh
python3.12 tools/talker/export.py \
  --base-model "$TALKER_BASE" --adapters "$TALKER_ADAPTERS" \
  --mlx-python "$TALKER_PY" --convert-python python3.12 \
  --llama-cpp "$HOME/.local/share/golive-talker/runtime/llama.cpp" \
  --quantizer /opt/homebrew/bin/llama-quantize \
  --output "$HOME/.local/share/golive-talker/models/tuned"
```

The export fuses into a separate checkpoint using the original serving
tokenizer, converts F16 with llama.cpp, then quantizes to Q4_K_M. The manifest
records commands, source revision, GGUF size and SHA256. Omitting `--adapters`
exports the untrained baseline for the same measurement protocol.

## Measure on the CPU box

Copy scripts, the private held-out pairs and the GGUF to the box. Run there;
measurements through SSH from the Mac would include the network.

```sh
python3 "$HOME/golive-talker/tools/benchmark.py" \
  --pairs "$HOME/golive-talker/data/test.pairs.jsonl" \
  --output-dir "$HOME/golive-talker/results/tuned" \
  --server-bin "$HOME/golive-talker/llama.cpp/build/bin/llama-server" \
  --model "$HOME/golive-talker/models/tuned-q4_k_m.gguf" \
  --threads 4 --mode all --repeats 3 --cold-limit 5
```

The benchmark owns temporary loopback-only server processes with no GPU or
other model offload. It distinguishes:

- `warm_prefix`: model resident, the required conversation prefix computed before
  early final; neither the current caller nor opener is precomputed. This is an
  ideal prefill bound, not a demonstrated sequential cache transition: the
  prompt's history truncation can depend on the current utterance's length.
- `warm_no_cache`: model resident, entire compact prompt evaluated on request.
- `cold_process`: new server/model process for each request, no model warmup;
  separately reports startup and launch-to-clause time. OS caches are not flushed.

`from_early_final_clause_ms` includes prompt construction and request
serialization. A complete clause needs punctuation or true EOS; a generation
limit never turns a fragment into a clause. TTFT counts the first generated
token, even when its UTF-8 bytes are not yet displayable. Structural validity
is a length/format check and is not a judgment of semantic acceptability.
Report failures in the denominator and sample counts beside all percentiles.
Repeated trials measure timing variability, not independent quality examples.

## Held-out judgment

```sh
python3 tools/talker/evaluate.py \
  --pairs "$HOME/.local/share/golive-talker/data-v2/test.pairs.jsonl" \
  --predictions "$TALKER_PREDICTIONS" \
  --output-dir "$HOME/.local/share/golive-talker/evaluation" \
  --mode warm_prefix --repeat 0
```

By default this creates a private judge queue and reports pending semantic
judgments. It never silently treats pending items as failures or successes.
For an authorized cloud judge, supply `--judge-url`, `--judge-model`, the key
through the named environment variable, `--allow-cloud-judge` and
`--allow-private-excerpts`. Alternatively provide an explicitly reviewed
redacted queue or import offline judgments. Regex masking does not prove
anonymity. The judge returns booleans for natural continuation, relevance and
grounding; all three must pass. Reports contain counts and confidence intervals.

## Checks and references

```sh
python3 -m unittest discover -s tools/talker -p 'test*.py' -v
python3 -m unittest discover -s tools/talker/tests -p 'test*.py' -v
```

- [ConvFill paper](https://arxiv.org/abs/2511.07397): this experiment implements
  the short initial talker clause, not streaming reasoner-knowledge infill.
- [Qwen3-0.6B](https://huggingface.co/Qwen/Qwen3-0.6B): non-thinking generation.
- [llama.cpp server](https://github.com/ggml-org/llama.cpp/blob/15fa3c493bfcd040b5f4dcb29e1c998a0846de16/tools/server/README.md):
  native streaming completion and cache protocol.
