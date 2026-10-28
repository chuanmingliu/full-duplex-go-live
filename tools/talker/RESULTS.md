# Local TALKER — 2026-10-28

Qwen3-0.6B was fine-tuned, exported and measured. **The tested CPU configuration
does not deliver a valid short clause reliably below 300 ms.** No live engine
configuration was changed. Semantic cloud judging is pending clarification of
the request's simultaneous cloud-judge and local-only-customer-text requirements.
No customer excerpts were sent to an external judge.

## Dataset and training

- 137 archived calls; 1,789 turns inspected; 184 eligible pairs from 34 calls.
- Train: 159 pairs / 28 calls. Validation: 9 / 3. Test: 16 / 3.
- Actual early-final, played-opener and synthesized-segment provenance required.
  Tool, revised, interrupted, abandoned and invalid first-clause targets excluded.
- Whole-call/dialogue-group splits; exact duplicate removal. Real/simulated
  source labels are unknown, not guessed.
- Run `20261028-173100-sft-ce6c`, MLX LoRA, 16 layers, rank 8, scale 16,
  learning rate 0.0001, batch 4, seed 42, 195 iterations, exit 0.
- The backend drops incomplete batches: 156 of 159 train rows per pass and
  8 of 9 validation rows contribute to loss. All 16 held-out examples are generated.
- Selected checkpoint: iteration 50, minimum saved-checkpoint validation loss
  **2.749**. Validation losses at 1/50/100/150/195: 7.620/2.749/2.866/2.810/3.028.
  Final training loss: 0.158. No checkpoint selected using held-out generation.
- All 184 SFT rows passed exact-prefix, EOS and sequence-length checks.
- Base: `mlx-community/Qwen3-0.6B-bf16`, revision
  `42096995f6402fde107068cf530136fe64b604f8`.
- Export: fused BF16 -> F16 GGUF -> Q4_K_M; **396,704,448 bytes**;
  SHA256 `2fba55a3b80be3f71228a6e2026c7a23584b95284d21210de2a8bade71bf4c7e`.

## CPU measurement

Box: Intel Xeon E3-1535M v6, 4 physical / 8 logical cores; llama.cpp `b8920`,
revision `15fa3c493bfcd040b5f4dcb29e1c998a0846de16`; 4 inference threads,
GPU layers 0, device none, native CPU build. Benchmark ran on the box through
loopback HTTP. The clock includes prompt construction and request serialization,
excludes ASR/TTS, and uses the same compact prompt as training (528 characters
at the median, 729 at p95 in this holdout).

Warm modes: 16 held-out turns x 3 timing repetitions = 48 attempts. Cold: first
5 held-out turns x 3 fresh processes = 15 attempts. All modes had zero transport
errors. Repetitions are not independent quality samples.

| Model / condition | First-token p95, ms | Valid short-clause p95, ms | Valid clauses | Valid and <300 ms |
|---|---:|---:|---:|---:|
| Base / ideal prefix prefill | 290 | 623 | 33/48 | 21/48 |
| Tuned / ideal prefix prefill | 172 | 323 | 39/48 | 33/48 |
| Tuned / warm, no prompt cache | 1,287 | 1,369 | 39/48 | 0/48 |
| Tuned / fresh process, from launch | 2,249 | 2,324 | 12/15 | 0/15 |

First-token percentiles use all attempts; short-clause percentiles use only
structurally valid outputs. Nine tuned warm trials produced overlong clauses;
they remain failures in the success denominator. Counting the first completed
clause regardless of length, tuned ideal-prefix p95 is **428 ms**. Tuned
ideal-prefix medians: first token 119 ms, valid short clause 238 ms.
Fresh-process startup p95: 940 ms. OS file caches were not flushed.

The ideal-prefix mode precomputes the needed history before early final, never
the current caller or opener. Because history truncation can depend on current
utterance length, this is an optimistic bound until actual sequential cache
reuse is implemented and measured in the engine. The no-cache result is the
relevant bound when that prefill is unavailable. Production load was not isolated.

## Held-out quality

One generation per unique held-out turn (repeat 0), 16 turns / 3 calls:

| Model | Structurally valid | Cloud-judged | Semantic acceptance rate |
|---|---:|---:|---|
| Base | 11/16 (68.75%) | 0 | Unmeasured |
| Tuned | 13/16 (81.25%) | 0 | Unmeasured |

Structural validity is complete punctuation/EOS plus <=12 content characters;
it is not semantic acceptability. Private judge queues are ready. Reported
acceptance is null while judgments are pending; it is not reported as zero.

## Private artifacts and commands

Mac source: `/Users/chuanmingliu/golive-local-talker`, branch `codex/local-talker`.
Mac private root: `/Users/chuanmingliu/.local/share/golive-talker/`:

- `data-v2/`: SFT, paired examples and extraction counts.
- `training-v2/`: validated request, exact versions, source hashes and run summary.
- `runs/20261028-173100-sft-ce6c/`: logs and all adapter checkpoints.
- `models/selected-adapter/`: validation-selected checkpoint and selection record.
- `models/tuned/fused/`: trained BF16 model, original serving tokenizer.
- `models/tuned/talker-q4_k_m.gguf`: CPU artifact.
- `artifacts/{baseline,tuned}-{summary.json,predictions.jsonl}`: box results.
- `evaluation-{baseline,tuned}/`: numeric evaluation and private judge queues.

Box private root: `/home/chuanming/golive-talker/`:
`models/{baseline,tuned}-q4_k_m.gguf`, `data/test.pairs.jsonl`,
`results/{baseline,tuned}-v2/`, `llama.cpp/build/bin/llama-server`.

Reproduce extraction/training/export with [README.md](README.md). Re-run the box:

```sh
python3 ~/golive-talker/tools/benchmark.py \
  --pairs ~/golive-talker/data/test.pairs.jsonl \
  --output-dir ~/golive-talker/results/tuned-repeat \
  --server-bin ~/golive-talker/llama.cpp/build/bin/llama-server \
  --model ~/golive-talker/models/tuned-q4_k_m.gguf \
  --threads 4 --mode all --repeats 3 --cold-limit 5
```

## Next CPU candidate

Distill a small intent/continuation classifier that selects from an approved
set of short first clauses, then use cached audio. This removes autoregressive
decoding from the critical path. Train abstention and opener-compatibility
explicitly; let the cloud answer supply factual details. Its latency and
acceptability remain to be measured. A modest size reduction alone should not
be assumed to solve the ~1.3-second uncached prefill tail.

The trained BF16 model has also been handed off to the separate Mac-serving
owner; these CPU figures are not Mac serving measurements.
