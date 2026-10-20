# Call out-mvfeat7d-nmvx — 2026-10-19 23:14, build 146c590

A real call, 119 s, the 23:00 hour, ending in a goodbye on purpose.

## The hangup

The owner said goodbye at 23:16:31.8; at 32.1 the model answered a
three-character farewell with `end_call` (first token 417 ms); the backend
was not resumed (the ISS-041 fix held); the farewell played, first audio
402 ms; `session: the assistant hung up` at 33.4, 714 ms after the player
went quiet. **The phone line stayed up** — the owner had to hang up (ISS-045).
Closing golive's socket is not a hangup to Active Call; its controller's
command is. Fix in the next build: `hangup_url`, a POST of the hangup
command to Active Call's command route for the call before the socket closes.

## What golive measured

10 turns with audio (many more cut: 14 barge-ins, 11 guesses abandoned —
a fast exchange of two- to five-character utterances at 23:16:16–23). Last
sound → first audio, from the turns with a figure: 540+380, 736+420,
796+420, 402+420 (the farewell: 822); console p50 562, max 955. The slow
hour. Focus: enrolled; 27 judged, 0 vetoed; `quiet_closes=12`, all inside
barge-ins; 1 barge-in kept (another voice — the first `keep` on a real call).

## Open

- The next goodbye call: `session: the gateway was told to hang up …
  status=200` and the line dropping within ~1 s of the farewell.
