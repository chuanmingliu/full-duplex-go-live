# Active Call real-call review, 2026-11-02

Call `out-mvykfqfn-wibn`, 09:13:58–09:15:59 Asia/Shanghai, used the original
GoLive Active Call production engine **f1fd9a6**. The owner requested a retry
after missing `out-mvykct0y-gklg`; the missed call was verified ended before
the single retry. The owner said **done** before this analysis. The preset
opener configuration remains active; Talker is disabled. The `078a4a9` focus
wakeup change is committed but **was not deployed for this call**.

## Normal replies

These are caller-last-sound → server output measurements, with endpoint
silence included once. First content excludes the preset opener but does
not certify semantic completion or acoustic handset audibility.

| Measurement | Previous real call `out-mvxr0ein-wwqh` | This call |
| --- | ---: | ---: |
| Production build | f1fd9a6 | f1fd9a6 |
| Ordinary response samples, quote origins excluded | 28 | 8 |
| Ordinary content median | 989 ms | 1,641.5 ms |
| Ordinary content p95 | 1,135 ms | 2,149 ms |
| Ordinary content maximum | 1,260 ms | 2,149 ms |
| Ordinary responses at/above 1,200 ms | 1 | 6 |
| First audio p95, including presets and quote requests | 458 ms | 383 ms |

The comparison is observational: different conversations and times, not a
matched replay. With eight samples the current p95 equals its maximum. Two
ordinary answers were interrupted after first content and remain counted.
The canonical conversational metric has the same eight samples in this call;
there is no separate spoken quote acknowledgment to remove. The strict gate
fails for latency and for fewer than 20 qualifying responses, despite having
no missing, malformed, duplicate or unverified output records.

The main delay is before the main LLM's first token. Turn/model-start → first
token spans **740–1,664 ms**, median **1,284 ms**. First token → substantive
audio output is **198–271 ms**. These stages do not independently distinguish
network transport, provider queueing and model inference.

The slowest ordinary turn (`item_9`, 2,149 ms) decomposes into:

- 236 ms from caller-last-sound to the engine turn/model start;
- 1,664 ms from that start to first token;
- 249 ms from first token to substantive output.

Presets still start near 380 ms; making those phrases shorter would not solve
the dominant first-token delay. Existing early/speculative starts are already
visible, including one turn beginning 115 ms before the caller's last sound.
Zero first-segment/TTS fields in preset turns do not mean zero synthesis cost.

Adding the owner's optional 50 ms telephony/handset allowance gives an
**estimated 2,199 ms p95**. No matched acoustic handset capture was verified.

## Interruptions

Two explicit `barge_in=true`, `speaking=true` cuts were paired without missing
origins. VAD-open → server cut took **112 ms** and **358 ms**, median **235 ms**.
The previous call had 15 comparable cuts, median 207 ms and p95/max 694 ms.
Two events are insufficient to claim that the interruption tail improved.

Both opens followed 320 ms of accumulated voiced input. That counter is not
an independently measured wall-clock onset interval and is not silently added
to the interruption measurement. Neither server cut nor its same-millisecond
truncation log establishes when buffered audio stopped at the handset.

- **112 ms:** the first three-character hypothesis arrived at 112 ms; the
  existing focus/backchannel checks released the floor and cut immediately.
- **358 ms:** prefeeding supplied 640 ms of audio; a one-character partial
  arrived after 34 ms. At 253 ms the engine requested a final while a
  continuation recognizer listened. The four-character final arrived at
  358 ms; caller evidence and the cut followed at that timestamp.

This supports preserving the existing checks rather than blindly cutting on
the first partial. It does not independently label echo/background audio or
prove recognition accuracy. No non-barge playback cancellation was mistaken
for an interruption.

## Quotation work, separately

CRM recorded nine successful submission/poll cycles lasting **3,005–4,007 ms**;
eight were marked ahead-of-request. These include polling and HTTP overhead,
not isolated compute time. Original calculation settings were preserved.

| Task | Foreground tool execution | Ready result → delivery wait | Caller-last-sound → readout start |
| --- | ---: | ---: | ---: |
| t1 | 0 ms | 1,294 ms | 2,787 ms |
| t2 | 1 ms | 0 ms | 1,870 ms |

Both deterministic readout starts have verified output evidence. The tiny
foreground execution times reflect prepared results, not instantaneous fresh
calculations. The first task's 1,294 ms wait is conversation/delivery scheduling,
not calculation tolerance; a scheduling wait alone does not establish that it
could safely be removed while preserving caller turns.

## Recording and next investigation

All five permanent archive files passed size and SHA-256 verification. Stereo
16 kHz PCM contains signal in both directions for **120.621 seconds**, with
**14 caller and 14 assistant transcript entries**. The session and call ended;
the production engine reports zero active sessions. This confirms recording
and teardown, not audio intelligibility or exact handset disconnect time.

The original profile and saved prompt/voice/greeting hashes match. No deployment,
configuration change, automatic redial or weakening of speaker safeguards was
performed during this review. Aria continues to use the original Active Call
route. The previously committed focus wakeup cannot be credited for this call.

Next, benchmark the main LLM's first-token stage under the original prompt and
conversation-history size, separating request/network/provider timing where
instrumentation permits. Keep the preset/main-answer coordination. A fast
decision API may improve classification, but would not by itself remove this
observed main-answer wait.

[Summary and archive verification](evidence/2026-11-02-active-call-real/summary.json),
[strict gate](evidence/2026-11-02-active-call-real/strict.json),
[normal stage breakdown](evidence/2026-11-02-active-call-real/normal-breakdown.json),
[interruption evidence](evidence/2026-11-02-active-call-real/interruptions.json),
[quote phases and interruption traces](evidence/2026-11-02-active-call-real/details.json).
Private audio, transcripts, credentials and customer details are excluded.
