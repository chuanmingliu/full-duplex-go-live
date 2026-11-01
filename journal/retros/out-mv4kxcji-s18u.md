## What happened

Three minutes, redialled from the box with the owner's prompt, on build `c8a8477` (eight copies of each completion). The owner was on a metro. 21 answered turns: p50 806 ms, p90 979, two over 1200.

## The two slow turns

Both were the synthesizer's first audio — 938 and 646 ms against a usual ~190 — and both went unhedged. The second synthesis connection was wanted four times on this call and refused four times, by a budget I had set: "no more than 18 requests a minute for the account". The call made 22.5 a minute by itself (44 segments, 19 tasks abandoned at interruptions and dropped guesses), so there was never room. The vendor refused nothing.

## What I changed

- The budget now bounds what hedging adds (4 second requests a minute), not the account's total. `365e4f3`, ISS-021. Not yet verified on a call.

## What else the call showed

- All eight copies won turns (5, 5, 7, 8, 3, 6, 4, 2); first token median 323 ms.
- 51 pauses inside 25 utterances: asking for the final at 100 ms instead of 120 would split 18% more recognitions for 20 ms a turn. Left at 120.
- The first-sound echo estimate does not work on this line: 4 readings (60, 100, 300, 600 ms) against 49 failed attempts, 34 of them because the idle line was never quiet. ISS-022.
- 16 prepared answers were dropped because the caller carried on, and 13 answers were interrupted; on a metro, some of both are the noise.

## Still open

The phone leg is not measured. Calls are recorded from 11:05 on 10-12; the next call in a quieter place is the first chance to time it from the audio.
