# Call out-mveluuk3-n4bu — 2026-10-19 09:58, build c3695fe

A real call, the kept prompt, 131 s, morning; the first on the caller-quiet
close (`focus_quiet_close_ms` 420). The caller hung up 13 s after the last
answer; no goodbye, `end_call` not asked for.

## What golive measured

Headline, caller's last sound → first audio (pause + console's figure):

| turn | pause | chat first token | first clause | synth first audio | first audio | **last sound → first audio** |
| --- | ---: | ---: | ---: | ---: | ---: | ---: |
| item_2 | 380 | 212 | 307 | 506 | 506 | **886** |
| item_3 | 380 | 65 | 226 | 455 | 455 | **835** |
| item_4 | 380 | −80 | −26 | 179 | 179 | **559** (cut) |
| item_7 | 420 | 369 | 440 | 657 | 657 | **1077** (quiet close) |
| item_8 | 380 | 159 | 261 | 487 | 488 | **868** |
| item_9 | 380 | 192 | 346 | 568 | 568 | **948** |
| item_10 | 380 | 290 | 395 | 596 | 596 | **976** |
| item_11 | 380 | 247 | 334 | 504 | 504 | **884** |
| item_12 | 380 | 224 | 304 | 460 | 460 | **840** |
| item_13 | 380 | 259 | 349 | 603 | 603 | **983** |
| item_16 | 380 | 461 | 572 | 832 | 832 | **1212** |
| item_18 | 380 | 560 | 650 | 650 | 650 | **1030** |
| item_19 | 380 | 250 | 333 | 546 | 546 | **926** |

- 13 turns: p50 **926**, max 1212; **1 over 1200** (by 12 ms: the chat
  model's first token at 461, the fastest of eight). Console's definition
  p50 546, max 832. Too few turns for a p95.
- Against the 09:37 call (p50 1052): every turn but one closed on the
  detector's 380 ms pause; no turn waited 600 or 900 ms. Whether that is the
  room (quieter this time: attributions 0.71–1.0 the caller's, 0.33 once)
  or the new close cannot be told from one call; the gate's floor stayed at
  −60 throughout.
- Guesses: 15 held, 3 abandoned. 3 barge-ins, all honoured; 2 cuts.

## The caller-quiet close, first time out

`quiet_closes=2`, both inside barge-ins over the answer (38.2 s and 39.5 s):
the caller interrupted, said three characters, paused, said five more. The
gate stayed open through the pause (over the assistant's own voice;
`others_ms=0`, nothing else voiced), the labels showed the caller quiet for
420 ms, the turn was taken — splitting the sentence at a pause the
detector's own 380 ms would also have split had the line been quiet. The
three-character guess was abandoned, the five-character part answered in
1077 (pause 420 + 657). So: it fired where designed, cost nothing against
the ordinary pause, and did not misfire on any of the eleven ordinary turns.

## Focus

Enrolment 6 offered, 6 agreeing. 16 judged, 0 vetoed, 0 too late.

## Open

- A noisy call with `quiet_closes` on ordinary turns is the real test of
  ISS-043; this call was clean.
- Two turns had the chat model at 461 and 560 ms (fastest of eight) at
  10:00 — the morning is not always quick either.
