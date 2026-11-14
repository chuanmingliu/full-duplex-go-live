# Real-call retrospective — out-mvvvt9l4-cauj

The configured real handset was called on d85232a at 12:09:07 on 2026-10-31; the call ended at 12:12:20. Analysis began only after the owner said done. The permanent archive contains verified audio, transcript, call record, session record and service log. Stereo PCM16 at 16 kHz lasts 192.82 seconds. The saved business prompt was used.

## Measured server output

There are sixteen answered caller rows, including three interrupted answers. First audio is p50 380 ms, p95/max 653 ms. Non-opener content is p50 889 ms, p95/max 1313 ms. Three explicitly linked deterministic quotation readouts are 912, 1039 and 751 ms. No missing content, unknown readout, duplicate, malformed or unmatched canonical-output records are reported. The strict gate fails both the twenty-sample minimum and the 1200 ms content target. These are server-output measurements; the separate Mac handset recording has not yet been located or analyzed.

The slow response is item_9, a repeated tier-comparison question answered from the model's context. Its pause is 380 ms, first backend token is 627 ms after close, and first answer content is 933 ms after close: 1313 ms overall. The winning model stream produces first content 807 ms after its request and the first clause at 837 ms. First synthesized audio takes 225 ms. There is no new pricing task on this turn. The cached opener does not remove this model-and-synthesis delay, and the evidence does not justify claiming a general latency gain from provider or endpointing changes.

## Playback defect and conversation review

All 55 canonical segments have contiguous sequence numbers. Nine segment timestamps are 149–499 ms earlier than their actual emission. On item_9, the first body segment is backdated about 392 ms: the player's old playhead continued from the completed opener despite a synthesis gap. ISS-086 fixes this clock gap while preserving audio ownership and excluding silence from interruption accounting. The existing source archive is preserved; its old timestamps must not be treated as corrected handset observations.

The conversation includes a default quotation, a tier comparison, a selected-tier quotation and onboarding instructions. The second deterministic result is interrupted, and the caller repeats the comparison. Recognition includes ambiguous words which the assistant interprets or asks to clarify; assessing those against what the caller actually said requires acoustic review. Later affirmative replies trigger repeated onboarding guidance. No new unrequested tool execution or coalesced-task event is observed. The earlier simulator's ISS-085 fix remains pending deployment and a matched replay.

The farewell completes before the engine's goodbye hangup. Assistant audio has no clipped samples and ends with about 1.526 seconds below the diagnostic amplitude threshold. Caller audio contains 61 full-scale samples; intelligibility and their origin remain unverified. The log has the existing endpoint-configuration warning and one suspected returning-assistant-voice interruption warning. These are not a clean-audio certification.

## Next verification

Complete the ISS-086 regression and full CI, publish the reviewed changes and notes, then repeat a matched simulator workload. Locate the Mac WAV and unchanged capture metadata, review all acoustic caller/reply boundaries, and collect at least twenty valid handset samples. Do not infer physical handset timing from the server archive, replace original slow requests with later repeats, or discard interrupted and ambiguous evidence.
