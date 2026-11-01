# Call out-mvonnls7-8wwz — 2026-10-26 10:46, build 9158f10

A real call, the owner on the line, quiet, 5 min 20 s: the first live sample
on the opener's third round (patience opener, backchannelled openers end
their turn). **First reported as met; it is not** — the figures were first
read from the log's `first_audio_out_ms`/`first_content_ms`, which count
from the turn's close, without the 380 ms pause that `call-latency.sh` adds
back. Corrected 22:15 the same day.

## From the caller's last sound

- **First audio**: p50 380, p95 380, max 419 — 18 of 18 answered turns
  opened at the pause from the cache.
- **The answer's own first word**: **p50 937, p95 1069, max 1281; 5 of 18
  over 1000.** On 14 of the 18 turns the content figure is exactly the
  pause plus the opener's length (440–650): the model's first clause was
  ready before the opener finished, so the opener's own length is the
  whole of the wait after the pause — and at the 650 cap that is 1030.
  (Raw log figures, from the close: content p50 557 / p95 689.)
- Chat model first token 316–537 ms on the turns sampled; backend complete
  536–1061 ms. 8 hedged lanes, the first copy answered on lanes 1–8 in turn.
- 22 speculations held, 7 abandoned (the owner's mid-thought pauses), 10
  openers became backchannels (none cut the owner), 5 answers cut by the
  owner's own interruptions, 0 patience waits, 0 utterances dropped as
  another voice.
- Quotation: 6 tool calls, 3 async, 3 delivered; the goodbye net ended the
  call on the owner's closing.

## The owner heard: "the second quotation on 三者险 stuck, then failed"

The quote API answered both changes (4.0 s and 3.5 s, `status=quote`). What
the owner heard was the chain around it and a cut readout:

| from the owner's last sound | the engine |
| ---: | --- |
| +0.0 s | opener at the pause |
| +0.6 s | model: a 22-char sentence **and `update_memory`** — not the quote |
| +0.6 → +3.7 s | the continuation waits for that sentence to finish playing (`waited_ms=3119`) |
| +4.4 s | second model call → `quote_total` |
| +4.8 → +7.5 s | the ack plays |
| +7.9 s | quote API back; readout starts at +8.0 |
| +12.6 s | the owner speaks over it; **readout cut at 4.7 s played** — the total unsaid |
| +28 s | the owner asks about 三者险 again; the model answers with no tool call, no figure |

Two causes, both fixed on this build's successor:

1. **The two-step change.** The profile's rule said "改一两项时直接调用
   quote_total 带 config", but the tool's own description said "调用前请确保已用
   update_memory 记录" — the model followed the description, twice. Now the
   engine folds a `config` into the working memory before the task takes its
   snapshot (`recordConfigChange`: one call, memory bumped once, a repeat
   folds), and the description says so. Saves the second model call and the
   wait for the filler to play: ~3.3 s of the 8 s.
2. **The cut readout, lost.** The result note was trimmed from the history
   (`limit_chars=2000`, 11 messages dropped four seconds after delivery) and
   the readout turn's truncation left no trace, so the next question had no
   figure to draw on. Now the last delivered result stays in the memory block
   under the working memory ("最近一次报价结果"), and if the readout turn is
   cut the block says what the caller heard and asks the model to repeat the
   total before anything else (`noteReadoutCut`).

Not changed: the quote API's 3.5–4.0 s (lead service → quotesvc → rating,
`api=4`); the ack covers it. A speculative re-quote the moment the `quote`
key changes would overlap it with the filler; left for the owner's call.

## Against the goal

From the caller's last sound: first audio p95 380, content **p95 1069** on
18 live turns, daytime. Not met. What binds now is not a vendor but the
opener's rendered length: 380 + opener (p95 650). Changed after this call:
the opener's tail is 80 ms (was 120) and its cap 540 (was 650), a floor of
920; in a good hour the clause is ready inside that on most turns.
