# Call out-mv9gx9fj-ydv1 — 2026-10-15 19:41

## What happened

Two minutes, from the box, on `a4b674c`, with background noise on the
caller's side. 9 answered turns after the first 40 seconds: last sound →
first audio p50 924, worst 1174, none over 1200. Console: p50 544, p95 710.
Before that, the opening line was cut at 2.4 s and the assistant said
nothing for about 35 seconds. That is the call's finding.

## The 35 seconds of nothing

- 0.3 s into the opening line an utterance opened as a barge-in. The echo
  guard warned it looked like the assistant's own voice returning (echo
  floor −39 dB, line floor −60), the floor was held 600 ms, and the words
  then in hand ("对，嗯，这") were judged not a backchannel: the opening was
  cut. Those are a caller's words, so the cut itself was right.
- **The utterance then never closed.** The line was "active" for 29 of the
  next 30 seconds — the detector's energy gate never saw 380 quiet ms — and
  the settled close (`settled_close_ms`) could not fire either, because the
  recognizer kept producing new hypotheses from the background sound every
  half second to a second: "no new words" was never true. The utterance was
  ended by the 30 s cap (`vad.max_speech_ms`), an answer was begun on it and
  cut at once by the next utterance, and that one was taken by the settled
  close after 3.5 s. First audio heard at about 0:35.
- The speaker-focus channel knew. Over those 29 "active" seconds the
  diarizer labelled **2,160 ms of voice** (coverage 0.89, all the primary
  speaker): what kept the gate open was not speech. The labels were 3,440
  ms behind by the end (ISS-032: the diarizer runs at real time and does
  not catch up inside a long stretch).

So the energy detector and the recognizer both fail on a noisy line in the
same direction — both call noise speech — and the one witness that does not
is on a side channel that is not consulted for ending turns. ISS-033.

## Also

- Two speculative answers abandoned (transcript moved on), ten held. No
  tool call again (same prompt as 19:34; it does not mention quoting).
- Fish not asked; synthesis first audio 170–250 ms on both accounts.
- The chat model: first token of the fastest copy 310–550 ms (slow hour).

## What I changed

Nothing yet. Proposed: a *voice-quiet close* — when the focus channel has
labelled no voice for about 800 ms after the last voiced frame while the
detector still says active and words are in hand, take the turn; and the
diarizer server skipping queued chunks so its labels stay within ~500 ms
of the audio on a long utterance.
