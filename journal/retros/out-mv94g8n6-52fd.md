# Call out-mv94g8n6-52fd — 2026-10-15 13:52

## What happened

Under a minute, from the box, first call on `e6d5deb` (Fish Audio as the
synthesizer's stall fallback; speaker focus in `observe`). 3 answered turns:
994, 570, 753 ms from last sound to first audio; none over 1200. Console:
614, 190, 373.

## Systematic

Nothing; too short to say. The focus side channel judged 2 of the 3
utterances, both the primary speaker, with the labels 360 ms behind the
audio on both. Fish was never asked (no first audio past 320 ms).

## What I changed

Nothing in the engine. The journal learned to show the focus verdicts
(`13653bd`), and the recording TTL went to 30 days (`fb96b80`).
