# Simulated call out-mvfdwap8-zv98 — 2026-10-19 23:03, build 146c590

Run `sim-1d85fe88793c8e37094c95cc`, scenario `kb-claims-skeptic`, receiver
**905a255** (Codex's n21/n22/n23 commit), 300 s, the 23:00 hour.

## What golive measured

- 37 turns, last sound → first audio: p50 **929**, p95 1309, max 1326;
  **7 over 1200**. Console's definition p50 549, max 946. Chat model,
  fastest of eight, 47 answers: p50 452, p90 553, max 660 — the slow hour,
  milder than 10-12's 707. Synthesizer p50 189, p90 235, max 292.
- Guesses 44 held, 16 abandoned (the scenario's rapid follow-ups). 21
  answers cut by the customer, 16 after audio had played.
- Focus: 6 offered, enrolled; 49 judged, 0 vetoed, 0 too late; 21 barge-ins
  judged, 0 kept (one voice); `quiet_closes=13`, all inside barge-ins.
- 236.8 s of 300 filled with silence (the receiver's gateway sends no RTP
  while it listens).

## The hangups (what the run was for)

- **n21 works:** `remote_turn_count` 27 against `remote_speech_start_count`
  110 — the old counting would have ended the call at the 40th segment, ~100 s.
- **n22 reached, then lost:** `customer_goal_reached` at 299.9 s of the call,
  `__customer_farewell` started — and 88 ms later the run's own
  `max_duration_ms` (295 000) stop fired, cancelled the farewell, and the run
  ended `failed: Active Call control disconnected`, 0 valid gaps, every
  semantic check indeterminate. Neither side hung up on purpose.
- Told Codex over the network (23:10): a stop the receiver initiates must
  end the run `completed` with its own reason; a goal reached in the last
  seconds should still get its farewell (extend by the farewell wait).

## Open

- Codex's reply; then the same scenario again — the goal should come well
  before 295 s on `kb-claims-skeptic` (the customer took the whole call to
  be satisfied; its goal is worded strictly), or raise `max_duration_ms`.
- 7 over 1200 at 23:03: the hour, not the build; rerun in daytime for the
  latency figure.
