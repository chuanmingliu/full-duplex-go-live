# Call out-mvg0j9mq-8p1n — 2026-10-20 09:37, build 0fcd82e

A real call from the metro station, 229 s, morning; the first with the
hangup rules read from the lead service (version 1, 4 model rules, 3 the
engine's). The owner: "when the user says goodbye but does not hang up,
the AI's last response should be said completely, then hang up".

## The hangup — fired, farewell cut (ISS-046)

09:40:47 the caller said they'd leave it there; the model called `end_call`
(reason: the customer wants to pause and be contacted later) with an
eleven-character farewell (1.8 s of audio). **At 770 ms a sound over it —
the station, or the caller's "嗯" — was taken as a barge-in: the farewell
was cut at four characters**, a new turn was begun on the sound (13
characters, never spoken), and the hangup fired 700 ms later with the
player quiet. The caller heard "好的，那" and the click.

Fix (next build): once `end_call` is accepted the farewell is not
interruptible — `yieldFloor` keeps the floor, `beginGeneration` answers
nothing, `Interrupt` is ignored; the hangup waits for the whole farewell.
Test: `TestTheFarewellIsNotCutByASoundOverIt` (fails on 0fcd82e: one
truncation).

## What golive measured

15 turns with audio in a 229 s call of 66 items: 24 barge-ins (0 kept —
the caller's own short bursts), 18 quiet closes, 18 guesses abandoned.
Console's definition p50 562, p95 665, max 715. Last sound → first audio
on the measured turns 740–1035. The station's announcements: 48 sounds
judged, 0 vetoed, 0 too late.

## Open

- Verify the whole farewell on the next goodbye call.
- The short-burst pattern again (a syllable, 420 ms, another): 18 quiet
  closes and 18 abandoned guesses in one call. A longer quiet-close inside
  a barge-in (600) is the knob; the owner to say whether the assistant
  feels too eager.
