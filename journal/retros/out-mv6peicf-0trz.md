## What happened

Five and a half minutes at 21:15, redialled from the box with the owner's prompt, on build `b1e3c88`. Recorded and archived. 47 answered turns: p50 884 ms, p90 1020, p95 1045, worst 1072, **none over 1200**. By the console's definition: p50 504, p90 640, p95 665.

It is the first call long enough for a p95 since the pool of two synthesis accounts went on, and the first on any build with no turn over the target in more than 40.

## Where the time went

Medians, from the caller's last sound: 120 waited before the final is asked for, 110 for it to come back, 125 to the chat model's headers, 260 more to its first token, 105 collecting the first clause, 207 for the synthesizer's first audio.

The chat model was in an ordinary evening: 260 ms after the headers here, against 123 at 14:28 and 598 at 23:14 the night before on this build. Outside golive straight after, the fastest of eight copies: 318 and 337 ms at the median, no round with every copy over 550.

## The slowest turns

Nothing over 1200. The five slowest (1072, 1065, 1045, 1029, 1020) are each a chat model a little slower than the call's median (first token 289 to 373 ms after the headers, against 260) on top of an ordinary everything else; none has a cause of its own.

## The pool of two synthesis accounts

33 first segments were asked of both accounts at once: the first account spoke first 17 times, the second 13. First audio 152 to 275 ms in those races. Nothing was refused; the session moved account four times as one filled its minute. About 21 synthesis requests a minute over the call (74 segments, 44 tasks abandoned at interruptions and dropped guesses), which one account could not have carried with a second request per turn. Two first audios outside a race were slow (402 and 572 ms) and neither made a turn late.

## Words already said

17 segments were played from memory; 13 turns opened that way and were among the fastest of the call (569 to 831 ms).

## Interruptions and murmurs

The caller spoke over the assistant 21 times; all 21 were other words than the listed murmurs, and the assistant gave way each time, 106 to 678 ms after it began to weigh the sound (once 1.6 s: the words were slow to arrive). 17 of the 47 answers were cut that way.

A listed murmur was said twelve times, each time into a silence, and each was answered as a turn with its head start. **No listed murmur was said over the assistant**, so that path is still not exercised on a call.

## What I changed

Nothing.

## Still open

- On this build, outside the 23:00 hour: 53 turns on two calls, none over. With none over, about 73 turns are needed before the share over can be said to be under 5%; one more call of this length does it.
- The 23:00 hour is unchanged and is the chat model's: decision 1, with three candidates measured this evening and none of them a fix.
- The echo still does not time the phone leg (15 measurements from 60 ms to 1.4 s, 41 not timed).
