# Call out-mvnta09v-1ptf — 2026-10-25 20:35, build 675c7af

A real call, 5½ minutes, quiet line, evening; the first on the ISS-055/056
focus rules and the fourth with the quotation stack.

## The quotation

- Prefetch during ringing: 4.5 s (quote-api v4, 5 lead vars), ready 30 s
  before the first question.
- **Default (t1)**: told directly, took 0, no prehook/posthook ✓ — but
  **delivered 4.6 s after the result was in**: the model's own sentence in
  the calling turn (26 chars, 4.2 s) held the floor and the readout waited
  for the gap.
- **Second quotation (t2, 三者 300万)**: the model's sentence (19 chars, 3.7 s)
  covered the whole 3.0 s computation; **no prehook** (the ack waited for the
  floor until the task finished: "the task finished while its ack waited for
  the floor"); the result then interrupted the model's sentence at a 1.5 s
  wait. Also: three quote_total calls in a row from the model within 1 s
  (items 14–16), two folded into t2.
- **t3, t4**: prehook said in place of the model (17 chars, 400 ms after the
  call) ✓, 3.5 s / 3.0 s, posthook + readout ✓.
- 10 tool calls, 4 async, 4 delivered, 0 superseded; memory v5.

**Fix (this build's successor):** the model's words in the turn that calls
the tool end at the clause in flight (`SoftStop` on the calling turn's pipe);
the ack or the ready result follows at the clause's end. And the prompt now
says the calling reply carries the tool call and no text at all. Test
`TestTheModelsWordsInTheCallingTurnEndAtTheClauseInFlight`.

## Focus — the new rules on a real call

- 0 quiet closes (ISS-056's path not taken).
- 8 barge-ins judged, 6 yielded to the caller's voice (primary 400–660 ms,
  others 0–0.38) — the caller's own interruptions, each with ≥ 400 ms of
  their voice; 2 kept.
- 7 "the caller's voice joined the utterance" on a free floor (200–300 ms of
  the caller in 560–1200 ms spans); none cut an answer that was playing.
  Whether these were the caller's short words or a sound with a flicker of
  the caller is not knowable from the labels alone; the free-floor rule
  stays at 160 ms.

## Latency — the evening

29 answered turns with a pause. Console out: **p50 633, p90 787, p95 797,
max 894**. Caller's last sound → first audio (out + 380): **p50 1013, p90
1167, p95 1177, max 1274**; 16 of 29 over 1000, 1 over 1200. Chat model
first token 95–566 from the request (median ~345: the evening, +60 vs
midday); synthesizer first audio 157–220. Scripted turns 162–236 out.

## Hangup

Goodbye net (6 chars) at 20:41:22 after the caller wound up; farewell whole.

## Follow-ups

- Next call: the calling turn should be silent (or one clause), the default
  read within ~0.5 s of the question, the prehook within ~0.5 s of a second
  quotation.
