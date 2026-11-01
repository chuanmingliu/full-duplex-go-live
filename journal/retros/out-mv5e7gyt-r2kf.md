## What happened

Four and a half minutes, redialled from the box with the owner's prompt, on build `b1e3c88` (each synthesis account counted against its own allowance). Recorded and archived. 13 answered turns: p50 1309 ms, p90 1387, worst 1404. Twelve of the thirteen were over 1200; the thirteenth opened from the phrase cache. By the console's definition: p50 929, worst 1024.

## Why every turn was slow

One stage, on every turn: the chat model's first token came 577 to 631 ms after its response headers, against 234 at midday and 324 an hour and a half earlier. Nothing else moved: the wait 120, the recognizer 87, the headers 133, the first clause 127, the synthesizer 223.

It is the chat model's hour, not the build. Measured outside golive two minutes after the call (`llm -pool`, 12 rounds, twice): the fastest of eight copies had its first token at 707 and 712 ms at the median, and in 23 of 24 rounds every copy was over 550 ms. The same was measured at 23:53 the night before (602 ms) and gone by 00:08. More copies and more keys do nothing for it: all of them are slow together. ISS-018.

## The pool of two synthesis accounts

Every one of the 13 turns was asked of both accounts. The second account spoke first 7 times, the first 6; first audio between 171 and 327 ms, nothing refused, no account reached its limit. ISS-029.

## The murmurs

Still not exercised: no murmur was said over the assistant. Both interruptions were sentences. A bare listed word was said twice into a silence and answered as a turn each time.

## What I changed

Nothing in the code. The standing figures now show the target missed over the last 100 turns because of this hour.

## Still open

A second chat-model vendor in the race is the only thing on the list that touches this (decision 1). Until then a call between about 23:00 and midnight will miss the target on most turns, and a call at other hours will not show whether it is met at this one.
