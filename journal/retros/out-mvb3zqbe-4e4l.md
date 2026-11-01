# Simulated call out-mvb3zqbe-4e4l — 2026-10-16 23:14, build d0bb53d

The first multi-turn conversation with the simulated customer (run
`sim-9ba890d55ead04990cdfa0a9`, scenario `renewal-long-1`, drafted from the
31-turn real call of 10-13 and allowed 40 assistant turns / 290 s). Both
line fillers were in: ours (`vad.line_fill_ms` 100, d0bb53d) and the
receiver's (Codex a4f77d5). It ran 144 s and ended with the customer's
gateway sending BYE (ISS-039).

## What golive measured

From the turn closing (add the 380 ms pause for last sound → first audio):

| turn | at | begun before close | chat model first token | first clause | synth first audio | first audio |
| --- | --- | ---: | ---: | ---: | ---: | ---: |
| item_4 | 23:15:18 | 292 | 365 | 478 | 630 | **630** |
| item_9 | 23:15:41 | 246 | 471 | 628 | 828 | **829** |
| item_14 | 23:16:01 | 293 | 370 | 447 | 622 | **622** |
| item_15 | 23:16:05 | 163 | 295 | 419 | 655 | **655** |
| item_19 | 23:16:20 | 297 | 255 | 381 | 605 | **605** |
| item_24 | 23:16:50 | 161 | 457 | 594 | 816 | **816** |
| item_28 | 23:17:08 | 262 | 485 | 552 | 732 | **732** |

- 7 turns measured: p50 **655**, worst 829 (+pause: 1035 / 1209). Under 40
  turns, so no p95 worth quoting; one turn (829 + 380 = 1209) over 1200.
- Chat model, fastest of eight copies, 20 answers: p50 656, max 792 — the
  23:00 hour. Synthesizer, 22 segments: p50 200, p90 256, max 505. Every
  turn was begun 160–300 ms before it closed (the early final held on the
  simulated line as on the phone).
- Guesses: 19 held, 6 abandoned. Answers cut by the customer speaking: 12
  (one after 12 s of audio). The customer talks over the assistant
  whenever its own model decides to; 61 customer utterances against 30.
- The line went quiet 10 times; 96.9 s of silence filled.

## What the customer's report says

- source `a4f77d5`, `deepseek-chat`; assistant utterances 30, customer 61,
  overlap 280 ms. `answered_once`, `greeting_once`, `call_limits` pass;
  `clean_termination` indeterminate (the BYE was its gateway's, not ours);
  the task expectations indeterminate until judged. Failure recorded:
  "Active Call control disconnected".

## What it says about the optimisation

- The simulated line reproduces the real-line result stage for stage: the
  recognizer and synthesizer as on the phone, the chat model the largest
  term and the only one that moves with the hour.
- 12 cut answers in 144 s is the customer's doing, not a judgement error:
  every cut followed real words from it. The `backchannel` effect is the
  test that tells a listening sound from a real cut-in.

## Open

- ISS-039: the receiver's gateway ends the call at ~145 s (Codex).
- Run the same scenario plain / `barge_in` / `backchannel` outside the
  23:00 hour, and compare builds on it.
