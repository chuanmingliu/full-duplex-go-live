## What happened

Seven minutes on `f374c41`, the same build as the call half an hour before that had met the target. This one missed: 41 answered turns, p95 1344, five over 1200. The two calls together: 93 turns, p95 1336, seven over. The goal was not met on that build.

## The five slow turns, two causes

- **The synthesizer stalled for eight seconds at 22:26.** First audio at 1284 and 1763 ms on consecutive turns, and a second segment that had not arrived after 3.3 s: the silence in the middle of an answer that the caller said "嗯" into. The chat model and the recognizer were normal in those seconds. ISS-017.
- **Four of the five were answers to "嗯。"**, begun from nothing when the pause ran out. Every turn without a head start on both calls, twelve of 93, was a two or three character turn. ISS-016.

## What I changed

- A final asked for in a pause is no longer subject to the minimum length for a guess. `c9a0738`.
- Short answer segments are remembered and played from memory. `4b029b3`.
- A second synthesis connection when the first is slow. `c9a0738`.

## Lesson

One call inside the target is not the target met. Two slow turns in 52 is consistent with a true rate anywhere from under 1% to about 13%, and the next call showed it.
