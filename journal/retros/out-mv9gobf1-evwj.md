# Call out-mv9gobf1-evwj — 2026-10-15 19:34

## What happened

Under two minutes, from the box, first call on `a4b674c` (working memory
and the background quotation tool on; Fish fallback; focus observe).
10 answered turns, last sound → first audio: p50 949, worst 1612, one over
1200. Console (turn judged over → first audio): p50 448, p95 722, max 1012.
The chat model was in a slow spell: first token of the fastest of eight
copies 382–612 ms (427 at the median) against ~250 at midday.

## The one turn over: 1612 ms

The caller's sentence grew across two pauses ("…发过来的？" → "…怎么帮" →
"…怎么帮我？"): two speculative answers were thrown away as the transcript
moved on, the third was begun 121 ms after the last final with no head
start, waited 600 ms of patience, and then the chat model took 612 ms to its
first token. Nothing golive did wrong; it is the price of a sentence said in
three pieces in the model's slow hour.

## What was not exercised

- **No tool call.** `tool_calls=0` on every completion; the model never
  asked for `quote_total` or `update_memory`, so the task pool, the delivery
  at a gap and the memory block are still unverified on a call. The owner's
  prompt for this call (9,489 characters) says nothing about quoting; a call
  that asks for a price on a prompt that knows the tool is what it needs.
- **Fish never asked**: no first audio past 320 ms (157–252 ms, both accounts
  speaking, nothing refused).

## Focus: the labels were late once

2 utterances judged, both the primary speaker. One had the labels 480 ms
behind, the other **1,660 ms behind and judged after its answer's first audio**
(`heard=true`). The diarizer's compute runs at about real time on this CPU,
so a long utterance (2.2 s voiced) lets it fall behind and it does not catch
up. In `veto` mode that utterance could not have been vetoed in time. See
ISS-032. `observe` stays until the lag on long utterances is bounded.

## What I changed

Nothing in the engine.
