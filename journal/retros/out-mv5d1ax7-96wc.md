## What happened

A minute and a half, redialled from the box with the owner's prompt, on build `e7e93eb`: the first call on the pool of two synthesis accounts, and the first since the owner's list of murmurs went in. Recorded and archived. 14 answered turns: p50 960 ms, p90 1066, worst 1307, one over 1200. By the console's definition: p50 580, worst 927.

## The slow turn, and why the call was slower than midday

The one turn over target (1307) was the chat model: its first token took 738 ms against a median of about 460. The whole call sat about 95 ms above the midday call's median (865), and the chat model's first token accounts for it: 324 ms after the headers against 234 at midday. That is the hour, as on earlier evenings; nothing in the build changed it. The synthesizer was steady: 24 first audios between 128 and 368 ms.

## The pool of two accounts

Both accounts spoke. Six first segments were asked of both: the first account won four, the second two. The other turns were not raced because an account had reached ten requests in the minute, and the session moved between accounts twice for the same reason. Nothing was refused.

Pacing the second account alone straight afterwards (30 requests a minute) had it refusing from the 28th second, 42 of 76 requests: its limit of about ten a minute is real, where the first account has carried 22 to 31. The pool treated the two alike, and with both counted full it would have left a busy call wherever it happened to be — on the small account, past its limit, losing sentences. That did not happen on this call. ISS-029.

## The murmurs

No murmur was said over the assistant on this call, so that path is still unexercised. All seven interruptions were sentences, each recognised as one 0.1 to 0.4 s after the caller began. A listed word said into a silence came up three times and was answered each time, with its head start. ISS-027.

## What I changed

- Each account is counted against its own allowance, written beside its key (20 for the first, 8 for the second: the vendor's page gives a topped-up account 20 a minute and a free one 10, and the second behaves like a free one); when both are full the session goes to the roomier one; and a request an account refuses is asked of the other at once, room or no room. ISS-029.
- The journal now counts segments asked of both accounts, and which account spoke first.

## Still open

14 turns is too few for a p95. The chat model's slow hours are decision 1 (a second vendor). The murmur-over-the-assistant path wants a call where it is tried.
