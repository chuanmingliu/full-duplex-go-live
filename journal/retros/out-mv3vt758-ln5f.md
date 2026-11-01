## What happened

Eight minutes on `f374c41`, the first call with two chat-model accounts and a connection for each copy of a completion. 52 answered turns: p50 882, p90 1070, p95 1097, two over 1200. The first call inside the target.

## The two slow turns, one per vendor

- **1354 ms: the chat model.** Every copy was slow at once, on both accounts: one stream opened in 115 ms and then said nothing for 700 ms; one took 814 ms to open and won. More keys do not help with this. ISS-018.
- **1336 ms: the synthesizer's first audio at 613 ms**; the other 51 turns were 152–239 ms.

## What else it showed

- The latency console accepted all of golive's events (367 sent, none refused): the first call with the corrected event ids. ISS-011 verified.
- The four copies each won about a quarter of the time (15, 20, 20, 13).
- 23 answers were cut short by the caller; the owner confirmed all were deliberate.
