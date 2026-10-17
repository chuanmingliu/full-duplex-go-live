## What happened

Two and a quarter minutes at 15:44 on 10-14, placed through the lead service with the default opening line. Build `b5ad7c6`. Eleven answered turns: p50 1091, two over 1200 (1246 and 3657). Console: p50 711.

## The turns over

- 1246: the chat model, first token 627 ms after the headers; the afternoon slow spell seen on the call before (fastest of eight copies outside golive: 463 and 462 ms right after this call, so it was easing). Not a cause of its own.
- 3657: **a listed murmur, said as the answer ended, was answered as a turn.** The caller said "对对" over the last word of an answer. The engine held the floor, as it should, and did not interrupt. But the words arrived a moment after the audio finished, so the check "is the assistant speaking" said no, a guess was begun on "对对", held until the swallowed utterance was later confirmed as a turn, and said back to the caller 2.7 s later ("嗯，您再试一下"), who talked over it. First time the murmur-over-the-assistant path ran on a call, and it found this. Fixed: a floor hold counts as the assistant having the floor, and a swallowed utterance takes any guess begun on it with it. ISS-031.

## Else

Nine first segments asked of both accounts (7 and 2 first), nothing refused. The recorder kept the caller's track on the clock and left out 40 ms of repeated frames. One phrase-cache play.

## What I changed

The murmur fix above, with a test that reproduces the timing.
