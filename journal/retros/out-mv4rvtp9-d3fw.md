## What happened

Two and a half minutes, redialled from the box with the owner's prompt, on build `3bed5d8`, in a quiet place. The first call recorded end to end. 14 answered turns: p50 865 ms, p90 920, worst 984, none over 1200. By the console's definition (turn judged over to first audio): p50 485, worst 604.

## What it verified

- **The hedge budget fix (`365e4f3`, ISS-021).** The synthesizer's first audio was slow enough to want a second request twice (261 ms without audio each time). Both were allowed; none was refused. The first request answered 10 and 17 ms later both times, so the hedge cost two requests and gained nothing, which is what it should do on a healthy minute.
- **Recording.** Both tracks are in the store (146.7 s) and play in the journal.

## Where the time went

Medians: 120 wait, 93 recognizer, 110 + 234 chat model, 103 first clause, 232 synthesizer. The synthesizer was steady: 27 first audios between 152 and 278 ms, no stall. The slowest turn (984) had the slowest first token (301 ms after the headers).

## The phone leg is still not measured

`echoprobe` over the recording found no echo of the assistant's voice at any delay up to 1.5 s (strongest significance 1.9 against a threshold of 6.0). A quiet handset cancels its own echo, so this line returns too little to time. The engine's own estimator took 3 readings (300, 620, 740 ms) against 14 failed attempts; with nothing in the audio to support them they are not a measurement. ISS-022, ISS-023. What is left is a recording made beside the handset, lined up with the box's.

## What else the call showed

- Pauses inside utterances: 10, of which 3 were 120 ms or longer and 2 between 100 and 119. Asking for the final at 100 would have split two more recognitions out of ten. Left at 120.
- Two interruptions; one was flagged as possibly the assistant's own voice coming back. One flag is not a pattern. ISS-025.
- Seven of the eight copies won a turn; one prepared answer was dropped because the caller carried on.

## Still open

14 turns is too few for a p95 of its own. The phone leg (above). The synthesizer's stalls did not appear on this call, so nothing new is known about them.
