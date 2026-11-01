## What happened

The first call placed by writing "call" in the session: golive redialled the owner's number with the prompt it had kept from the page dial two minutes earlier (`e517e3a`, ISS-020). 15 answered turns, none over 1200, worst 1143.

## What it showed

- The redial carried the same 13,116 characters as the page dial: same hash, `639dcecf24dd`.
- The median was 943 ms against 841 on the call 26 minutes before, on the same engine. The chat model's first token was slower in these two minutes (median 464 ms against 343): it was the vendor's busy hour, 23:35. Nothing in golive changed between the two.
- No synthesizer stall: worst first audio 409 ms.

## Lesson

Compare builds within the same half hour. The chat model's hour moves the median by more than most changes do.
