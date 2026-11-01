## What happened

Under a minute, redialled from the box, the first call on eight copies of each completion (`9f2ae31`). 4 answered turns: 688, 702, 1029, 865 ms. Too short to say anything about p95.

## What it was for

Three new readings, of which two worked:

- **Eight copies**: winners were copies 2, 4, 5, 8, 8; first token 198–375 ms.
- **Pauses inside utterances**: 15 in 5 utterances. At 120 ms seven of them split a recognition; at 100 it would be ten.
- **Echo return time**: no measurement at all, on a line that echoes at −28 dB, and nothing in the log to say why.

## What I changed

- Every failed echo attempt now logs its reason, and the summary counts them. `c8a8477`, ISS-022.
