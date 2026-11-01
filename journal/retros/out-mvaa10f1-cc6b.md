# Call out-mvaa10f1-cc6b — 2026-10-16 09:16

## What happened

1.7 minutes, from the box, a quiet room, on `83f9a92` (the target-speaker
channel after the 23:45 retune). 9 answered turns: last sound → first audio
p50 886, worst 948, none over 1200; console p50 497, p95 566. No answer
cut, nothing vetoed, nothing let go by; the caller's voice enrolled from
the first sentence (660 ms), labels 0–40 ms behind, 8 utterances judged,
every one the caller's. A clean baseline for the channel.

The owner's remark: the assistant may have ignored the last few questions.

## What the log says

Every turn was answered with a complete completion. Two things fit the
remark:

- The last three answers were 28, 22 and 3 characters long, the last a
  three-character reply to a 33-character question. The whole question
  reached the model (6,144 cached prompt tokens, a full answer in ~550 ms);
  the brevity is the model's. To read with the transcript on the call page.
- Twice (09:17:17, 09:17:38) the caller spoke in pieces with ~200 ms pauses,
  the answer was begun on the words before a pause, two more characters
  followed, and the guess was kept because `sameWords` allowed any number
  of trailing sounds. If those two characters were a word, the answer was
  to a shorter question than was asked. ISS-037.

## What I changed

`sameWords` allows one trailing sound, not several (`TestSameWords`).
