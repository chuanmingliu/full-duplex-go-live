# Real Active Call interruption comparison

Current: `out-mvxr0ein-wwqh`, engine **f1fd9a6**, 2026-11-01 19:30–19:34
Asia/Shanghai. Previous real call: `out-mvxo348d-w5pb`, engine **45b5564**.
This is an observational comparison of different conversations, not a matched
replay or proof that the code caused every difference. The original prompt,
voice and greeting match exactly. The service is idle after the call.

Typical interruptions improved, but the slow tail did not. Ordinary responses
were slightly slower. Calculation time remains separate from those measurements.

## Normal responses

All figures are caller-last-sound → GoLive player output, in milliseconds.
First content excludes cached openers/fillers but can be an explanatory lead-in.

| Population / metric | Previous | Current |
| --- | ---: | ---: |
| Ordinary responses, excluding every quotation-origin request | 32 | 28 |
| Ordinary first-content median | 963.5 | 989 |
| Ordinary first-content p95 | 1078 | 1135 |
| Ordinary first-content maximum | 1081 | 1260 |
| Ordinary responses at or above 1200 ms | 0 | 1 |
| Existing conversational metric, sample count | 32 | 29 |
| Existing conversational metric, p95 | 1078 | 1260 |
| First audio p95, including openers and quote requests | 381 | 458 |

The existing analyzer excludes quote-only result handoffs but retains an actual
spoken acknowledgment before a quotation tool call. Current `item_11` took
**1504 ms** to that speech: 600 ms end-silence plus 904 ms after that timing
origin. This is **not calculation time** and must not disappear from the
established gate. The 28-sample ordinary-only view is an additional diagnostic,
not a replacement gate introduced to obtain a pass.

The slowest ordinary reply, `item_41`, took **1260 ms**. Its model request began
204 ms after caller-last-sound, its first token took another 795 ms, and first
answer audio followed 261 ms later. Thus the remaining delay is predominantly
model/answer production, not quotation work or cached-opener availability.

The strict gate fails at **1260 ms conversational p95** and one unverified
quotation-readout record. There are no missing ordinary output records.
The owner's optional +50 ms assumption gives 1185 ms for ordinary-only p95 or
1310 ms for the existing conversational p95. These are estimates, not acoustic
handset measurements; no current matched handset recording was verified.

## Interruptions

For an equivalent comparison, count only explicit `barge_in=true` cuts with
`speaking=true`: speech interrupting active playback. Measure from VAD-open to
the server cut decision, excluding prior detection time and audible handset stop.

| Metric | Previous | Current |
| --- | ---: | ---: |
| Comparable cuts | 11 | 15 |
| Median | 265 ms | 207 ms |
| p95 / maximum | 680 ms | 694 ms |

The median is **58 ms lower (21.9%)**; p95 is **14 ms higher**. VAD still requires
320 ms accumulated voiced input. That accumulation is not an independently
measured wall-clock onset-to-detection interval and is not silently added to
these values.

The previous broad report counted 14 playback cancellations, including three
with `barge_in=false`; it reported median 275.5 ms and p95 686 ms. This call has
15 explicit barge-ins plus four non-barge cancellations. Two of the latter are
quotation deliveries replacing playback, not caller interruptions. Timing those
from the nearest older VAD-open would falsely produce 4454/5415 ms interruption
figures. All four remain in the evidence, separately classified.

Eight of fifteen interruptions used prefed ASR audio. Their median was 207.5 ms;
the seven without prefeeding had median 203 ms. These unequal utterances do not
isolate prefeeding's causal benefit.

The slowest traces explain why early ASR alone does not fix the tail:

- **694 ms:** first partial at 148 ms still matched a configured acknowledgment;
  the next partial at 694 ms was decisive and the engine cut immediately.
- **669 ms:** first partial at 175 ms was also acknowledgment-compatible;
  clearer words arrived at 669 ms, followed immediately by the cut.
- **554 ms:** recognition arrived at 140 ms, but initial speaker evidence did
  not identify the caller. Caller evidence reached 400 ms at the 554 ms point
  and released the hold. This is an observed identity wait, not proof of a
  false rejection without independent speaker labels.
- **411 ms:** the first usable partial itself arrived at 411 ms.

Both slowest traces had prefeeding active. Preserve the acknowledgment and
background-speaker safeguards; shortening a global hold would trade away their
purpose. The next investigation should target useful partial-transcript timing
and speaker-evidence arrival. Server truncation logs share the cut's millisecond
stamp for all fifteen events, but that does not establish when the phone stopped
playing buffered audio. Echo/background rejection settings were unchanged;
this call does not independently label all background sounds to verify accuracy.

## Quotation work, separately

The CRM recorded 18 submission/poll cycles at **3003–4507 ms** (16 prepared
in advance), with no failed cycle. Previous cycles were 3004–4509 ms. These
measure API submission/polling, not isolated backend compute or caller delay.
The configured calculation delay was preserved.

| Task | Foreground tool execution | Ready result waiting for delivery | Caller-last-sound → deterministic readout |
| --- | ---: | ---: | ---: |
| t1 | 0 ms | 0 ms | 990 ms |
| t2 | 1 ms | 0 ms | 869 ms |
| t3 | 1 ms | 4418 ms | 6148 ms |
| t4 | 1 ms | 1587 ms | Unverified |
| t5 | 3523 ms | 0 ms | 4424 ms |

`t3` is a cached result delayed by conversation/acknowledgment scheduling; its
4418 ms wait is not calculation tolerance. It yielded to a newer caller answer
and subsequently delivered. `t4` produced result content after 3722 ms but has
`readout_expected=false` and no deterministic-readout timestamp, so this is not
proof that a quoted amount was spoken. Keep that evidence gap visible. `t5`
contains an actual 3523 ms foreground tool wait, separately tolerated as requested.
Previous deterministic readouts were 903/1084/1017 ms.

## Recording and follow-up

All five archive files passed size and SHA-256 checks. Audio is **273.620 s** of
stereo PCM with signal in both directions; the transcript has **34 caller and
40 assistant entries**. The session closed and the service reports zero sessions.

No runtime/configuration changes or redial accompanied this review. Aria remains
on the original GoLive Active Call endpoint with f1fd9a6; the owner's saved
45b5564 restore anchor is retained. ISS-095 remains open. Follow-up priorities
are the interruption tail, the ordinary reply's model-to-audio delay, and cached
quotation-result scheduling, while keeping calculation duration separate.

[Numeric comparison](evidence/2026-11-01-active-call-interruption-real/comparison.json),
[strict report](evidence/2026-11-01-active-call-interruption-real/strict.json),
[interruption traces](evidence/2026-11-01-active-call-interruption-real/interruption-tails.json),
[quotation breakdown](evidence/2026-11-01-active-call-interruption-real/quote-breakdown.json),
[archive verification](evidence/2026-11-01-active-call-interruption-real/archive-verification.json).
