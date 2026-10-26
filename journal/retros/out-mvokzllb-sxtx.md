# Call out-mvokzllb-sxtx — 2026-10-26 09:31, build 9abbdf3

A real call, the owner on the line, quiet, ~4 minutes: the first daytime
sample of real size on the opener (25 answered turns with a pause).

## From the caller's last sound

- **First audio**: p50 380, p90 600, p95 677, max 1157. 22 of 25 turns opened
  at the pause (380); the three without an opener were turns the assistant
  was still speaking into (a result being read, an answer still playing) and
  the one that followed a backchanneled opener within 1.5 s — their first
  audio was the content itself (606, 777) — plus two 600 ms patience waits.
- **The answer's own first word**: **p50 839, p90 986, p95 1077, max 1192**;
  4 of 25 over 1000: the two patience-wait turns (600 + 477, 600 + 592), the
  no-opener turn at 1157 (model first token 471) and one at 1077. Chat model
  first token 101–471 ms after the close (median ~250).
- 25 openers of 7 kinds (好 ×5, 嗯 ×5, 嗯嗯 ×5, 明白 ×3, 对 ×3, 行 ×2, 好的 ×2);
  2 became backchannels (the owner went on mid-sentence; no cut), 1 skipped
  on the fragment that followed; 3 model duplicates dropped; 8 answers cut by
  the owner's own interruptions (6 barge-ins judged, 2 kept as backchannels);
  4 utterances judged another voice's and let go (ISS-058's rule held: 0
  dropped at the quiet close).
- Quotation: 5 tool calls, 3 async, 3 delivered; goodbye net ended the call.

## The owner heard

- **"Some opener has repeating playback"**: the engine said 嗯， and the model
  began 嗯嗯，好的，; the duplicate strip took one 嗯 off and left 嗯，嗯，好的，.
  Fixed: openers are stripped longest-first and repeatedly (`stripOpeners`,
  tested).
- **"We need a smooth opener"**: the join between the cached opener and the
  model's clause is a cut between two renderings. The opener now keeps 120 ms
  of its tail with a 60 ms fade instead of an 80 ms hard cut. The prosody
  seam itself remains; the model is already told to continue from the opener.

## Against the goal

First audio p95 677 (the patience waits), the answer's own first word p95
1077 on 25 live turns; the over-1000 turns are the 600 ms patience waits and
one no-opener turn, not the vendors. With the opener on, the patience wait
(`patient_silence_ms` 700 → the 600 ms close) is now the largest term on the
content figure — lever 3.4.
