# Call out-mvfzqdsc-fz8a — 2026-10-20 09:14, build bc154ae

A real call from the metro station, 119 s, morning; ended with a goodbye.
The first call with all three of last night's fixes at once: the
caller-quiet close (ISS-043), the clean-tail enrolment (ISS-044), the
gateway hangup (ISS-045).

## The hangup — verified

09:16:29.8 the caller's goodbye; the model's four-character farewell with
`end_call`; the backend not resumed; farewell played (first audio 462 ms);
09:16:31.5 `the assistant hung up` → **`the gateway was told to hang up
status=200`** on `/command/<call id>` — the call id is the gateway's
dialog id, as hoped. Line dropped (owner).

## The noise — handled

- Enrolled from the first utterance (09:14:51, 1.3 s of voice, 18 s into
  the call); six candidates, six agreeing by the end.
- **Five utterances judged "another voice" and let go by with no turn** —
  the station's announcements and bystanders; twice the caller's voice
  joined one and it was taken up. One barge-in kept (not the caller's).
  On the 19:25 call the same sounds cut an answer and were answered.
- `quiet_closes=11`, all inside the caller's interruptions over the answer:
  the caller speaks in short bursts over the assistant (12 barge-ins,
  1–5 characters each), and each burst is answered 420 ms after it ends.
  Nine guesses abandoned as the bursts ran on — the cost of answering
  fast to someone who keeps talking; no turn lost.
- Zero vetoes, zero too late; 25 judged.

## What golive measured

18 turns with audio, last sound → first audio: p50 **913**, max 1193,
**0 over 1200** (console's definition p50 501, max 773). Chat model first
token 91–507 ms (fastest of eight). 8 answers cut by the caller.

## Open

- The short-burst pattern (a word, 420 ms, another word) turns one caller
  sentence into three turns and three cut answers. A longer quiet-close
  *inside a barge-in* (say 600) would merge them at the price of 180 ms on
  those turns; worth trying if the owner finds the assistant too eager
  when interrupted.
