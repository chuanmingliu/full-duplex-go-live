# omni — one model for the turn, and a floor head after it

Branch `omni-s2s`. The plan the owner agreed on 2026-10-22 (decisions: an
M3 Mac with 128 GB; deployment on Metal; a duplex *head* on Qwen3-Omni rather
than a new base; the talker trained on the MiniMax track of the archive).

```
archive (audio.wav, call.json, prompts/)
   │
   ▼  omni/data        sim call → turn records + floor labels (this directory)
   ▼  omni/train       mlx backend (the Mac)  ·  unsloth backend (CUDA, when it can)
   ▼  omni/eval        WER / speaker similarity / first-audio offline; golivefdb online
   ▼  omni/serve       MLX server on the Mac, OpenAI chat+audio contract; vllm-omni /
                       sglang-omni manifests kept for parity only
   ▼  golive           internal/provider/omni, profile "engine": "omni"
```

## Milestones

| # | Milestone | Gate |
| --- | --- | --- |
| M0 | `omni/data` reads an archived call into turn + floor records | one call → shards, checksums, scenario tag; test passes on a synthetic call |
| M1 | Base Qwen3-Omni thinker on the Mac (MLX): zero-shot WER on 50 sim turns vs Tencent | number in the journal |
| M2 | Thinker LoRA (4-bit) end to end on the Mac | WER improves on phone audio; spoken-QA held-out not worse |
| M3 | Talker + MTP LoRA on MLX (the port; also the serving path) | owner's voice; speaker similarity above the two MiniMax accounts' mutual similarity |
| M4 | MLX server behind `engine: omni`; `golivefdb` and `golivebench` A/B vs cascade | first audio from last sound; FDB metrics not worse |
| M5 | Sim-call acceptance set; first real call on the test line | p95 on 40 turns; retro |
| B0 | Floor head on the thinker's streaming hidden states, trained on the floor labels | — |

## What `omni/data` produces (M0)

`PYTHONPATH=. python3 -P -m omni.data.build --archive ~/golive-archive --out ~/golive-omni/v1`

```
v1/
  turns.jsonl            one line per answered turn (below)
  floor.jsonl            one line per call: 80 ms frame labels
  audio/<call>/NN.caller.wav     the caller's words of the turn, mono 16 kHz
  audio/<call>/NN.assistant.wav  the answer as it was heard (cut short if interrupted)
  prompts/<sha>.txt      copied from the archive
  manifest.json          counts, bytes, SHA-256 of every file, the gates that dropped turns
```

A turn record:

```json
{"call_id": "...", "turn": 7, "sim": true, "scenario": "kb-busy-bargainer", "build": "df22ed2",
 "prompt_sha": "639dcecf24dd",
 "caller":    {"start_s": 41.2, "end_s": 43.9, "text": "...", "wav": "audio/<call>/07.caller.wav"},
 "assistant": {"start_s": 44.8, "end_s": 48.1, "text": "...", "wav": "audio/<call>/07.assistant.wav",
               "interrupted": false, "cache_hit": false},
 "history":   [{"who": "assistant", "text": "..."}, {"who": "caller", "text": "..."}],
 "latency_ms": 884}
```

Floor labels, per 80 ms frame of the call: `silence`, `listen` (caller alone),
`wait` (caller done, answer not yet heard), `speak` (assistant alone),
`yield` (caller over the assistant and the assistant stopped),
`hold` (caller over the assistant and the assistant carried on — a murmur,
a hesitation, another voice). `wait` and `hold` are the frames a duplex head
has to get right; everything else the gate already knows.

Gates (a dropped turn is counted in the manifest with its reason): no caller
text, no assistant text, no recording, a turn the log could not place
(`last_sound_s < 0`), a caller segment under 200 ms.

**Real calls are excluded unless `--include-real`.** The archive marks a
simulated call by its id in the desk's dials file; until that join exists in
`call.json`, `--sim-ids <file>` takes the list. Nothing in `v1/` leaves the
box except by the owner's copy to the Mac.

Recordings made before build `b5ad7c6` have the caller's track running late
(ISS-030); `omni.data.archive.realign` applies the journal's correction from
`audio_in` marks before cutting segments.

## Recipes (M2+)

`omni/train/recipes/*.yaml`: dataset path, `targets` (`thinker`, `talker`,
`floor_head`), rank, steps, eval set. One format for both backends:
`python -m omni.train --backend mlx --recipe recipes/thinker-lora.yaml`.
