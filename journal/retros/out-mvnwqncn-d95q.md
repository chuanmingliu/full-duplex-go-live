# Call out-mvnwqncn-d95q — 2026-10-25 22:12, build de62d5f, simulated (cooperative)

The first conversation through the PBX on the opener (`duplex.opener`): the
simulated customer (`cooperative`), dialled with `call sim`, 90 s.

## The opener on a conversation

- 9 answers opened from the phrase cache at the pause; 0 of the model's own
  openers had to be dropped (the owner's prompt does not make it say 好的).
- First audio after the turn was judged over: **0 ms on 8 of 9** (the opener
  plays as the pause ends → 380 ms from the caller's last sound), 142 ms on
  one. The answer's own first word (`first_content_ms`, after the opener):
  522–838 ms after the pause → **902–1218 ms from the last sound**, p50
  ~1041 (661 + 380); the content figure sits where the old first audio did,
  with one turn over 1200.
- 4 speculations abandoned of 9 held: the simulated customer talks over the
  assistant (4 barge-ins judged, 4 yielded). Nothing split, no quiet closes.

## What this says about the goal

By the owner's definition — caller's last sound → first audio, p95 — the
opener puts every turn at the pause (380 ms) on the bench (20 of 20, 22 of
22) and on this call (8 of 9 at 380, one at 522). The answer's own words
still arrive ~900 p50 / ~1080 p95 from the last sound: that is the figure the
chat-model levers (3.2–3.5) still work on, and the one to quote beside the
headline. A real call with a person on the line is still owed: the bench's
turns are fixed sentences and the simulated customer is a script.
