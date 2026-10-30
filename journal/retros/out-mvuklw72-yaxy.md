# Simulated coverage conversation — 2026-10-30, 1fa2846

Call `out-mvuklw72-yaxy`, session `sess_54b6d1a7c9d166c64c6b9d1f`,
receiver run `sim-2f90b0d351a3c2ca12bb3272`. Existing scenario
`kb-coverage-nerd`, 14:07–14:12 Asia/Shanghai. This is a simulated PBX
conversation, not a physical handset measurement. The saved business
prompt remained unchanged (SHA prefix `639dcecf24dd`).

| Measurement | Samples | p50 | p95 | Maximum |
| --- | ---: | ---: | ---: | ---: |
| Caller last sound → server first audio, including fillers | 21 | 380 | 380 | 381 |
| Caller last sound → server first non-opener content | 21 | 926 | 1049 | 1138 |
| Receiver application first-audio boundary, including fillers | 20 | 473 | 649 | 697 |
| Linked quotation result / readout | 0 | — | — | — |

All timings are milliseconds, nearest-rank p95. The strict server
first-audio/content gate passed at 1200 ms with its twenty-sample minimum.
There were no missing, unverified, duplicate or malformed response metric
rows, no interrupted answers, and no fallback to estimated content timing.
The receiver excluded 22 other observations; they are not counted as
successful samples. Its transcript was truncated, so the receiver report
alone is not evidence of a complete conversation. The full server archive
was verified separately.

No quotation task was submitted and no linked result was delivered.
This run therefore does not verify the new quotation-readout timestamp or
posthook omission, despite passing the ordinary-response gate. It also
cannot establish the physical handset target. Older coverage runs used a
different saved prompt, so they are not a controlled before/after comparison.

## Recording and input clock

All five required archive files passed size and SHA-256 checks. The
280.736-second stereo PCM16 WAV at 16 kHz contains signal on both channels.
The journal retains 33 caller and 26 assistant transcript entries, plus
33 cached filler events. There are 12 opener-to-backchannel transitions,
explaining the difference between caller fragments and 21 answered turns.
The remaining response metric row is the initial greeting. All 37 generated
turn IDs are accounted for: 22 metric rows, 12 backchannels and three
abandoned speculations.

The input is on its recording clock and reports 8820 ms dropped. The
recorder's correction removes byte-identical repeated frames when input is
ahead of that clock; it is separate from engine ingestion. No engine input
queue-full/drop warning appears in this call. This is not evidence that
8820 ms of recognized speech was lost, and it is not subtracted from any
latency result. The corrected recording is not an independent handset
playout clock.

## Follow-up

The candidate passed complete quality and normal/race CI before deployment.
The post-deployment ordinary-response check passed, but a quotation-focused
run and real acoustic handset recording remain necessary. No new runtime
change is justified by these ordinary-response latency samples alone.

## Conversation review

The conversation stayed in coverage and eligibility questions. A calculation
was offered and then postponed; all 31 backend completions recorded zero
tool calls. No premium was quoted. The final farewell completed and
triggered `said_goodbye`; the simulator disconnected before the server's
delayed hangup needed to act. No new pricing or closing regression was
established. Receiver semantic checks were indeterminate, so its zero
failure count is not independent semantic validation.

One regional claims-provider answer followed a passage that conflicts with
other text in the same saved prompt. ISS-076 records the ambiguity. The
authoritative business rule is unknown; the saved prompt remains unchanged.
Private business wording and the full transcript are not included here.
