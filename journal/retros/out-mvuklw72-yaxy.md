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


## Observed follow-up release and simulator replay — 380c1a7

Evidence collected at 2026-10-30T06:45:18.605787+00:00. Commit `380c1a7350ec73094f01cc19006acff08b587719` passed the complete
quality, normal and race CI jobs ([run 41014100115](https://github.com/chuanmingliu/full-duplex-go-live/actions/runs/41014100115)).
The engine, CRM and journal executable hashes matched the reviewed build;
the deployed release completed its cache, recording and journal checks.
Historical call audio, session and log fingerprints remained unchanged.

The separate guarded configuration change enabled only standard-plan
`prequote.follow_selection_max=8` in quote-plans v7
(SHA `f2142bd9cddd`). The original initial limit remains
implicit eight. All other configuration snapshots were unchanged.

The subsequent simulator replay was `out-mvuluygh-p1vy`, session `sess_fe205c981a9694ecd25c6acc`,
run `sim-ed7527b2b5e18a97e6184bcb`, using the same `sim-prequote-hit` scenario and saved
business prompt (SHA prefix `639dcecf24dd`). All five archive files passed
size and SHA-256 checks; both recorded audio channels contained signal.

| Replay measurement | Observed result |
| --- | --- |
| Server first audio, including fillers | n=5, p50=380, p95=380, max=380 ms |
| Server first non-opener content | n=5, p50=994, p95=1192, max=1192 ms |
| Linked result content | n=3, p50=1103, p95=1192, max=1192 ms |
| Explicit quotation readout output | n=3, p50=1103, p95=1192, max=1192 ms |
| Receiver first-audio boundary | n=7, p95=667 ms |

The targeted quotation exercise **passed** with
3 observed readouts and 2
confirmed guarded nondefault posthook omissions. The separate strict
twenty-sample server gate **failed**. Its recorded reasons were:
first_audio: 5 samples, need 20; first_content: 5 samples, need 20.
These observations alone do not prove that a completed waiting
acknowledgement or additional prefetch was exercised; use delivery logs
for that narrower attribution. This replay is simulator evidence. The
physical handset target remains unproven.
