# Backchannel integration: real-call review

Call `out-mvzfq218-qbqy`, 2026-11-02 23:49:48–23:52:30 Asia/Shanghai,
ran on Active Call engine `b51e959` with the original GoLive settings and the
optional `asr_assist` detector. The owner said **done** before this review.

The integration worked throughout the call: 3,883 model chunks, no fallback,
14 fresh floor observations and three earlier ASR requests. The controller
still owned every interruption and acknowledgment decision.

## Actual detector assistance

| Utterance input start | Request after VAD-open | Final after VAD-open | Outcome |
| --- | ---: | ---: | --- |
| 12,080 ms | 63 ms | 166 ms | Server cut at 167 ms |
| 106,900 ms | 168 ms | 245 ms | Acknowledgment; floor retained |
| 112,660 ms | 65 ms | 129 ms | Server cut at 129 ms |

All three requests happened after **40 ms of silence**, instead of the legacy
120 ms request threshold. This is an 80 ms scheduling head start, not proof
of an 80 ms handset or cut-latency saving. In the third case the final arrived
at 100 ms of silence, before that legacy request threshold. A different
unassisted partial could still have arrived, so there is no matched
counterfactual cut time.

Three acknowledgments were retained overall. In one, the audio model had
strong floor-taking evidence and requested a final, but the unchanged
transcript logic correctly retained the floor. This supports keeping the
model as bounded ASR assistance instead of allowing direct cuts. The call
alone does not provide independently labeled echo/background accuracy.

## Normal responses and interruptions

Normal content excludes preset openers and quotation request/result work.
All timing below is server-side; cuts start at VAD-open and exclude the
preceding speech-detection interval and audible handset playback stop.

| Measurement | Previous real call `out-mvykfqfn-wibn` | This call |
| --- | ---: | ---: |
| Normal-content responses | 8 | 14 |
| Normal-content median | 1,641.5 ms | 919.5 ms |
| Normal-content p95 / maximum | 2,149 ms | 1,302 ms |
| Normal responses at or above 1,200 ms | 6 | 2 |
| Interruption cuts | 2 | 11 |
| VAD-open → cut median | 235 ms | 168 ms |
| VAD-open → cut p95 / maximum | 358 ms | 407 ms |

These are different conversations and unequal populations. The lower median
and response tail cannot be attributed solely to the detector; the
interruption tail did not improve over the immediately previous two-cut call.
The earlier 15-cut call `out-mvxr0ein-wwqh` had median 207 ms and maximum 694 ms,
also an observational comparison.

The longest cut here waited for speaker identity: at 18 ms the speaker tracker
classified the utterance as non-caller, and at 407 ms it had 400 ms of primary
caller evidence and released the hold. Preserve this background-voice guard.
All 11 cuts paired to an utterance; one separate tool-result replacement was
explicitly non-barge-in and excluded. Truncation logs shared the cut timestamp,
which does not establish when carrier buffers or the handset stopped playing.

Normal LLM request/start → first token spanned 392–789 ms (median 502.5 ms).
First token → first content spanned 30–328 ms (median 258.5 ms). The two slow
normal replies were 1,302 and 1,263 ms: their request-to-token intervals were
789 and 730 ms, followed by 287 and 276 ms to content. This remains the main
normal-response tail to investigate. Preset/main-answer coordination stays on;
18 opener enqueues were presets. Overall first-audio median was 380 ms and
p95/max 620 ms, including quotation requests and adaptive endpoint silence.

The minimum-20 strict gate **does not pass**: too few responses, normal content
p95 above 1,200 ms, and an unverified quotation chain. There were no missing
output records. Adding the owner's optional 50 ms allowance gives an estimated
normal p95 of **1,352 ms**. No actual acoustic handset capture was verified.

## Quotation timing, separate

Seventeen CRM calculation/poll cycles took **3,005–4,507 ms**, sixteen marked
ahead-of-request; the configured calculation delay was retained. These include
polling/HTTP overhead and are not isolated compute measurements.

Foreground task durations were 0–4 ms because results were already prepared.
Observed readout starts were 989, 1,111 and 2,573 ms from caller-last-sound.
Ready-result delivery waits included 759, 1,545 and 2,322 ms; these are
conversation/result scheduling, separate from calculation tolerance.

One result (`t4` / `item_18`) led to another quote request rather than a
verified deterministic readout (`readout_expected=false`). Its child task
`t5` was delivered without caller-origin timing metadata. The analyzer
correctly retains an unverified readout and invalid tool link instead of
inventing an end-to-end number. Investigate caller provenance through chained
result turns separately; do not count that gap as normal response latency or
shorten quote calculation to hide it.

## Recording, state and follow-up

All five permanent archive files passed size and SHA-256 checks. The 161.42 s
stereo 16 kHz recording has signal in both directions; the transcript contains
23 caller and 24 assistant entries. This proves stored signal, not acoustic
handset audibility or independent transcription accuracy. The call/session
ended and the phone service is healthy with zero active sessions.

The original saved prompt, voice and greeting match, and the enabled trial
profile is unchanged. No runtime change or redial was performed during review.
Keep the guarded integration and legacy fallback; next latency work should
measure the main LLM first-token tail and preserve the speaker/ack safeguards.

[Summary](evidence/2026-11-02-backchannel-real/summary.json),
[detector events](evidence/2026-11-02-backchannel-real/detector.json),
[assisted-utterance and quotation traces](evidence/2026-11-02-backchannel-real/details.json),
[strict gate](evidence/2026-11-02-backchannel-real/strict.json),
[interruption pairing](evidence/2026-11-02-backchannel-real/interruptions.json).
