# Call out-mvaaww9l-jyt9 — 2026-10-16 09:41

## What happened

2.7 minutes, from the box, the owner's own voice throughout, on `284aad8`.
11 answered turns; console p50 594, p95 875. But 3 of the owner's answers
were vetoed as "another voice's", 3 of their utterances let go by, 5 answers
cut, and `primary_speaker=1` at the end of the call: **the channel was
inverted** — it had enrolled "not the caller" as the primary.

## Why, in two parts

1. The enrolment from the first utterance (15.7–18.0 s, 1.3 s of voice)
   was a poor sample of the owner's voice: against it, the owner's other
   utterances on the same call scored 0.07–0.25 (two scored 0.44–0.51).
   Against the 09:16 call's enrolment they scored 0.54–0.59 on several and
   0.12–0.24 on others. One utterance is not enough to represent a voice on
   a phone line.
2. The tracker then applied its own rule — the first voice with 600 ms of
   speech is the primary — to the service's labels, and that voice was
   speaker 1, "not the caller". From then on every verdict was the opposite
   of the truth, and the zero-caller guard protected the wrong speaker.

## What I changed

- A target-speaker service's speaker 0 is the caller by definition: the
  tracker is pinned to it on enrolment and neither enrols nor hands over on
  its own (`Tracker.Pin`).
- A sanity check on the enrolment: eight seconds of voice after it that is
  under 30% the caller's means the enrolment is the wrong voice, and the
  channel observes only for the rest of the call.
- **A kept voice profile** (`focus_profile: owner`): the service keeps the
  owner's embedding across calls (`~/edge/tsv/profiles/owner.npy`, made from
  28 s of the owner's voice over 14 utterances of the two quiet calls), and
  a call starts enrolled from it before the caller has spoken; a line with no
  profile yet saves its first enrolment as one. Against that profile, **all
  19 of this call's utterances** score 0.33–0.61 at the median with every
  window over the threshold — the single-utterance enrolment recognised two
  of them.

The profile is the owner's voice embedding, 192 numbers, kept on the box
only; it is not audio and cannot be turned back into any.
