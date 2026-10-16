# Call out-mvaaww9l-jyt9 — 2026-10-16 09:41

## What happened

2.7 minutes, from the box, the caller alone in a quiet room, on `284aad8`.
11 answered turns: last sound → first audio p50 1030, worst 1255, one over
1200; console p50 594, p95 875. But: 3 of the caller's answers vetoed as
"another voice's", 3 of the caller's utterances let go by, 5 answers cut —
on a call with nobody else in the room.

## Why

The enrolment was taken from the caller's utterance at 15.7–18.0 s (1.3 s
of voice). Offline, against that enrolment, the caller's own later
utterances scored 0.07–0.25 (14 of 17 under the 0.28 threshold); against
the enrolment from the 09:16 call they scored up to 0.54–0.59. The voice is
consistent across calls; the one window was a bad sample. Then, with the
caller's windows labelled "not the caller", the tracker's own rule — the
first voice with 600 ms is the primary — enrolled *that* label as the
primary (`primary_speaker=1`), and every verdict came out inverted: the
caller became "another voice".

## What I changed

- The caller is pinned as the primary on this channel (`Tracker.Pin`).
- The enrolment is a kept profile (`focus_profile: owner`), the call starts
  enrolled from it, the first good utterance makes it when there is none.
- An enrolment that the next eight seconds of voice contradict (under 30%
  "the caller") is distrusted: the channel observes only for that call.

ISS-038. The first call on this build makes the profile; the second is the
test.
