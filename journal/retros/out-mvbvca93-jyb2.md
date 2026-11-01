# Call out-mvbvca93-jyb2 — 2026-10-17 12:00, build 1e35e09

A real call, the kept prompt, 107 s, midday. The first call with `end_call`
in the model's tools (`tools: on … end_call`). The caller hung up two
seconds after the last answer, without a goodbye, so the tool was never
asked for: nothing to read about the hangup on this call except that its
presence changed nothing else.

## What golive measured

Headline, **caller's last sound → first audio** (380 ms pause + the
console's figure):

| turn | at | begun before close | chat model first token | first clause | synth first audio | first audio | **last sound → first audio** |
| --- | --- | ---: | ---: | ---: | ---: | ---: | ---: |
| item_2 | 12:01:02 | 210 | 322 | 421 | 641 | 641 | **1021** |
| item_3 | 12:01:15 | 204 | 251 | 390 | 607 | 607 | **987** |
| item_4 | 12:01:23 | 138 | 431 | 548 | 760 | 760 | **1140** |
| item_6 | 12:01:30 | 200 | 338 | 448 | 620 | 620 | **1000** |
| item_8 | 12:01:39 | 162 | 331 | 406 | 625 | 625 | **1005** |
| item_12 | 12:01:55 | 111 | 209 | 287 | 467 | 467 | **847** |
| item_13 | 12:02:11 | 152 | 387 | 528 | 942 | 942 | **1322** |
| item_14 | 12:02:15 | 177 | 170 | 204 | 204 | 204 | **584** |

- 8 turns measured: p50 **1003**, max 1322 ms; **1 over 1200**. (Console's
  definition: p50 623, max 942.) Too few turns for a p95.
- The one over, item_13: the synthesizer's first audio 413 ms on a 7-character
  opening clause (the other segments 149–298), on top of a 387 ms first token.
  item_4 (1140) was the chat model, 431 ms.
- item_14 (584) opened from the phrase cache: first clause at 204 ms, no synthesis.
- Chat model, fastest of eight copies, 14 answers: 320–620 ms, median ~530.
  Synthesizer first audio, 22 segments: median ~185, max 413.
- Guesses: 12 held, 1 abandoned (the transcript moved on). Answers cut by the
  caller speaking: 6, 5 after audio had played (item_6, 9, 10, 11, 13); three
  further turns (9, 10, 11) were cut within 0.3–1.5 s of their first audio and
  have no latency line.
- Three more cuts were restarts by the caller themselves (a word, then the
  sentence) — the engine followed each at once.
- Line filled 4 times, 7020 ms of silence (`vad.line_fill_ms`).

## Focus

Enrolment by agreement worked again: 4 utterances offered, 4 agreeing, none
tentative after the second; 12 sounds over the answer judged, all yielded to
the caller's voice, 0 vetoed, 0 too late; 7 barge-ins judged, 0 kept.

## Open

- `end_call` not yet exercised: it needs a call that ends in a goodbye. Next
  real call, say goodbye and wait; on the simulator, Codex's customer-side
  hangup (desk n22) will do the same.
- The synthesizer's 400 ms openings on short first clauses recur (413 here,
  355–589 on 10-11); the Fish fallback waits 320 ms and did not fire — it
  would not have helped at 413 (Fish's own first audio is ~420).
