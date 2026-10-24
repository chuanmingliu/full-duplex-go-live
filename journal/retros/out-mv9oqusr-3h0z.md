# Call out-mv9oqusr-3h0z — 2026-10-15 23:20

## What happened

4 minutes, from the box, first call on `82dfd81` (the target-speaker service
on the focus channel, `focus: veto`), the English show on. The labels were
on time at last — 0–140 ms behind — and every judgment fired: 5 enrolments,
2 answers vetoed, 4 utterances let go by, 3 taken up again when the caller
joined in, 23 answers cut, 35 guesses abandoned.

The owner's verdict, from the journal: four of *their own* utterances were
marked "another voice; the answer was dropped unheard". Correct: that is a
false reject, and the call was still badly affected by the show.

## Why

Offline, against a clean 4 s enrolment of the caller's voice from the quiet
19:34 call: the caller's own utterances on a quiet line score 0.42–0.63 at
800 ms windows (p10 0.42, 2% under 0.35). On this call, with the show
playing under the caller's voice, every window is a mixture, and 1,130
voiced windows scored: 83 at or over 0.35, 3 at or over 0.45, none at 0.5.
The verifier reported what it heard — mostly not the enrolled voice — and
the engine read that as "another voice". A mixture of the caller and the
show is the caller talking.

Two things made it worse: the enrolment was extended with four later
utterances the labels had called the caller's (they were mixtures, so the
show entered the enrolment and then scored as the caller), and the veto and
barge-in rules took 70% "other" as decisive when the caller's share in a
mixture is routinely 30–50%.

## What I changed (next build)

- "Another voice" now means the caller is in none of it: every verdict (veto
  of an answer, keeping the floor over the answer, letting an open utterance
  go by) requires zero primary voice in the span, with `focus_veto_share`
  0.95. A mixture is never acted on.
- The enrolment is the first clean utterance only (`focusEnrolments` 1).
- The service's threshold goes to 0.28 (`EDGE_TSV_THRESHOLD`).

Expected: no answer of the caller's dropped; the show cutting in sometimes,
as before the verifier. The open question is no longer who is speaking but
how to take the show out of the audio: target-speaker *extraction* (speech
enhancement conditioned on the enrolment, what the Quail "voice focus" does)
is the next thing to evaluate, in daylight — whether one runs in real time
on this CPU decides it. Decision 11 stands, with this cost written in.
