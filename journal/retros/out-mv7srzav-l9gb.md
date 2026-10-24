## What happened

Two minutes at 15:37 on 10-14, the first call placed through the lead service's API: template `car-renewal` v1 rendered for a sample lead (9,360 characters, a different and shorter prompt than the page's 13,116, with an 11-character opening line from the `start` variable). Build `b5ad7c6`, the first call on the new recorder. Seven answered turns: p50 1067, worst 1371, one over 1200. Console: p50 687, worst 991.

## The slow turn

The chat model: first token 691 ms after the headers (814 from the request), against 290 at this call's median. Measured outside golive straight after, the fastest of eight copies was 806 ms at the median, with every copy over 550 in 9 rounds of 10 — the 23:00 pattern, at 15:40 in the afternoon. First time seen by day. ISS-018.

## Else

Both synthesis accounts spoke (7 and 2 first), nothing refused, first audio 188–265. Recorder: the session record says the caller's track was kept on the clock (ISS-030, first call with the fix); the journal applied no correction. The CRM recorded the call with its prompt's hash and the journal's call id.

## What I changed

Nothing.

## Still open

Whether the afternoon slow spell recurs. The lead service's calls use whatever `start` they are given as the opening line; the owner's real line is not in the service yet.
