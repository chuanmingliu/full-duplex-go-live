# Call out-mvel3zdh-5z7r — 2026-10-19 09:37, build 35de29b

A real call, the kept prompt, 95 s, morning. `end_call` available; the
caller hung up 7 s after the last answer without a goodbye, so again not
asked for.

## What golive measured

Headline, caller's last sound → first audio (pause + console's figure):

| turn | at | pause | chat first token | first clause | synth first audio | first audio | **last sound → first audio** |
| --- | --- | ---: | ---: | ---: | ---: | ---: | ---: |
| item_2 | 09:37:48 | 380 | 332 | 434 | 630 | 630 | **1010** |
| item_3 | 09:37:57 | 380 | 193 | 273 | 436 | 436 | **816** |
| item_4 | 09:38:10 | 380 | 294 | 400 | 672 | 672 | **1052** |
| item_5 | 09:38:14 | 380 | 318 | 399 | 613 | 613 | **993** (cut) |
| item_6 | 09:38:22 | 380 | 348 | 472 | 681 | 682 | **1062** |
| item_7 | 09:38:28 | 600 | 129 | 267 | 501 | 501 | **1101** |
| item_10 | 09:38:40 | 900 | 279 | 365 | 624 | 625 | **1525** |
| item_11 | 09:38:47 | 600 | 180 | 269 | 464 | 465 | **1065** |
| item_12 | 09:38:54 | 600 | −202 | −133 | 41 | 41 | **641** |

- 9 turns: p50 **1052**, max 1525; **1 over 1200**. Console's definition
  p50 613, max 682. Too few turns for a p95.
- The one over, item_10, is turn-taking, not the pipeline: the transcript
  read as unfinished (a run of repeated words), two guesses were abandoned
  as it moved on, the turn was taken only after the 900 ms wait, and the
  answer took 625 from there. Four turns waited 600 ms (mid-thought); on
  item_12 the answer prepared during the wait was ready 41 ms after the
  close (641 from last sound).
- Chat model, fastest of eight, 12 answers: 251–499, median ~420.
  Synthesizer, 20 segments: 158–418, median ~210.
- Guesses: 9 held, 2 abandoned. 1 answer cut by the caller (after 616 ms
  played). Line filled twice, 5.6 s.

## Focus

Enrolment by agreement: 3 offered, 3 agreeing; 7 sounds judged, 0 vetoed,
0 too late; 1 barge-in, honoured.

## The noise (the owner: "background noise still impacts the call")

Where it showed, in the gate's own words: the 9 s utterance at 55.5 s
(item_10) was open with the line never under the bar, 6.3 s of it judged
voiced, and the labels put half of its 8.2 voiced seconds on a source that
was not the caller; the recognizer's hypothesis kept growing with repeated
words, so two guesses were thrown away and the turn was taken only by the
900 ms patience. Three more turns (7, 11, 12) ended by the settled close —
600 ms of unchanged words — because the line never gave the detector its
380 ms; on item_11 two seconds were "voiced" for one character. One
barge-in (item_5 → 6) was flagged by the echo check and then judged on 200
ms of labels split 0.5/0.5. Nothing was vetoed and no turn was lost; the
cost was 300–900 ms of waiting per noisy turn and one 1.5 s turn.

Fix in the next build (ISS-043): the turn is taken on the *caller's* quiet
by the focus labels, 420 ms, when the line itself never goes quiet.

## Open

- `end_call` still unexercised on a real line: the call needs a goodbye.
- Verify the caller-quiet close on the next noisy call: `quiet_closes` in
  `focus: this call`, and the pause column at 420 on those turns.
