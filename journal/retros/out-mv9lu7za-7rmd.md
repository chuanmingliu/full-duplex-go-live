# Call out-mv9lu7za-7rmd — 2026-10-15 21:59

## What happened

3.7 minutes, from the box, first call on `8210200` (floor tracking, the gate
record, the diarizer keeping up), with an English-language show playing in
the caller's background. 13 answered turns: last sound → first audio p50
1011, 7 of 30 measured waits over 1200; console p50 537, p95 765. The chat
model was in its slow hour (fastest copy's first token 345–645 ms).

The caller's verdict: the background had a plainly negative effect. The log
says how.

## The answer kept being cut

30 "turn truncated"; 20 utterances opened *over* the assistant's speech at
exactly 320 ms of "speech" — the signature of a steady level, not a person
starting to talk. The hold then saw the recognizer produce words (its
Chinese guesses at the English show, or at the assistant's own echo, which
returns at −28 dB on this line) and ruled "not a backchannel": cut. 47
guesses held, 28 abandoned as "the transcript moved on": the show's words
kept extending the caller's sentences.

What the gate could not tell, the focus channel could: of the 6 utterances it
judged, two had **no voice at all** (voiced 0 ms — the cut was on echo or
noise) and one was **entirely another voice** (the show). Its labels were
280–520 ms behind — inside the 600 ms hold.

## What worked

- No 30 s utterance (longest 7 s). The floor was raised once (−57 → −44) and
  the utterance closed half a second later. Floor tracking is for a rumble;
  a show is a voice with gaps and rightly does not trigger it.
- The gate record is in the journal: this is the first call with the Gate
  lane. Level p50 −57/−38 dB (quietest/loudest frame per 100 ms), open 44%
  of the call.
- Fish asked twice (MiniMax past 320 ms), MiniMax spoke first both times.

## Also

- One label lag read −12.8 s after the diarizer skipped chunks: the skip
  advanced its frame count by the wrong amount (fixed next build).
- The floor-raise line said `over_ms=0`: it logged the unset config value,
  not the default in use (fixed).
- No tool call (same prompt as before).

## What I changed (next build)

ISS-034: a sound over the answer is judged by the speaker labels before the
words are allowed to cut it. The tracker now tells the assistant's own
return apart (the voice heard mostly while the assistant speaks) and reads
the span through its masks with that voice taken out: the caller's voice
yields, another voice or the echo alone keeps the floor, labels not in yet
are waited for up to `focus_barge_in_wait_ms` (900). The kept utterance goes
by without a turn unless the caller joins in. `focus` goes to `veto`.
