# Call out-mvglt706-8e2i — 2026-10-20 19:32, build df22ed2

A real call from the metro, announcements and noise, 103 s, evening;
`hangup-rules` v2 from the panel.

## The hangup — whole farewell, line dropped

19:34:14.9 the model called `end_call` with reason **客户拒绝两次** — the
"refused twice" case from the panel, in the tool's words — and a
fourteen-character farewell; the farewell played to its end (17.58); the
hangup came 701 ms after (ISS-046 verified: nothing over it cut it);
`the gateway was told to hang up status=200`. The model used the tool
this time, so the goodbye net (ISS-047) was not needed.

## The noise

Enrolled from the first utterance (3.3 s of voice, tentative) and firm by
the second; of six candidates offered, three disagreed with the enrolment
later in the call (`agreeing=3` of 4, 5, 6) — the clean-tail offers picked
up the station on utterances that ran into an announcement; the agreeing
group held and the primary stayed the caller. 17 sounds judged, 0 vetoed,
0 too late; 6 barge-ins, 8 quiet closes; 6 guesses abandoned. Two long
answers cut (7.7 s and 5.0 s in) by the caller.

## What golive measured

11 turns, last sound → first audio: p50 **1060**, max 1318, **2 over 1200**
(chat model first token 537 on one, a 701 ms first clause on the other;
the evening). Console's definition p50 613, max 898. One turn answered
from a 1060 ms pause with the answer already prepared (0 ms after close).

## Open

- The enrolment offers on a noisy line: a candidate whose span ran into an
  announcement is offered and outvoted; harmless so far (3 of 6), but a
  call that starts in an announcement could seat the wrong voice. Offer
  only spans the labels say are voiced by one speaker — once the service
  reports per-candidate agreement, which it does.
