# Speko call 04: response delay and idle audio buffering

Call: `out-speko-20261031-04`, bridge release `17f7798`, October 31, 2026.
The user reported high latency. The 137.52-second connected call ended with
`agent reported an error`. The bridge did not retain the underlying local
error, so its exact termination cause cannot be recovered from this call.

## Recording findings

Call Journal on the ThinkPad, port 9886, retains the chat, both audio directions,
carrier ringback, and original adapter events. All six archived file checksums
were verified after import. There are 49 imported transcript rows; these are
fragments, not 49 conversational turns.

Seven selected caller-to-assistant gaps in the adapter WAV files are:

| Caller speech ends (s) | Reply starts (s) | Gap (ms) |
| ---: | ---: | ---: |
| 25.08 | 28.64 | 3560 |
| 35.96 | 39.00 | 3040 |
| 49.02 | 51.94 | 2920 |
| 62.16 | 64.74 | 2580 |
| 72.30 | 74.64 | 2340 |
| 93.20 | 95.30 | 2100 |
| 103.76 | 107.94 | 4180 |

Times are relative to the adapter recorder start. Median: **2920 ms**;
range: **2100–4180 ms**. These are approximate **adapter-boundary** measurements,
not acoustic handset end-to-end measurements. Selection excludes the opening,
overlapping speech, and incomplete caller fragments. Reply onset includes short
acknowledgments/fillers; it is not necessarily the start of the substantive answer.
The opening starts about
4.7 seconds after SIP answer.

Method: 20 ms windows of the 8 kHz, PCM16 WAVs; RMS threshold 300; manually
selected windows using the transcript to pair replies. Repeating with thresholds
150 and 600 changed individual estimates by at most 160 ms across the full
threshold range. This is an energy-based estimate, not a phonetic alignment.
The recorder's clock alignment and sparse writes also limit exact timing.
Transcript arrival timestamps were not used as speech onset or end timestamps.

The adapter reports a maximum send queue of 40 ms, zero lost accepted RTP
packets, and 121 rejected packets. The latter is not proof of a loss-free
carrier path. The 40 ms queue cannot explain these multi-second gaps by itself.

## Reproduced bridge problem

The Speko bridge paced digital silence through the same queue as speech. A
no-phone replay showed hundreds of milliseconds of queued output, with a
sampled backlog reaching 1300 ms. It then hit the two-second queue limit and
terminated at 70.06 seconds. Its final maximum accepted backlog was 1960 ms,
and no client delegations occurred. This reproduces a bridge failure class;
it does not prove the historical handset call had the same final error.
A deterministic provider fixture supplying
1500 ms of leading digital silence followed by speech delayed speech by
1522 ms in the old bridge.

The fix retains at most 100 ms of leading digital silence when playback has
not started or has had no voiced samples for at least 200 ms. It never removes
voiced samples or silence behind queued speech. Speech remains paced in 20 ms
frames and its two-second queue bound still applies. This can shorten excessive
digital-silence pauses; it does not speed up spoken audio.

Periodic bridge diagnostics now record queue duration and received/played audio
counts. A final summary records the maximum queue, trimmed silence, delegation
count, and termination reason. No audio, transcript, prompt, or credentials are
logged by these counters. Counts of trimmed silence across a whole session are
not equivalent to per-turn latency savings.

Validation: clean-source race tests passed for `internal/speko` and
`internal/telephony`; Speko vet and standalone build passed. The 1500 ms idle
silence fixture fell from 1522 ms to about 121 ms before first speech. A 3000 ms
idle-silence burst also survives; a 3000 ms voiced burst still fails the bound.
The tests also preserve voiced bytes and silence following queued speech.

The corrected live no-phone replay completed its 150-second test window
(146.38 seconds in the active bridge audio loop) without an error. Maximum
queue: 920 ms; client delegations: zero. It removed 37.51 seconds of cumulative
idle digital silence across the session. That cumulative number is not a
per-turn saving. The earlier uncorrected replay failed at 70.06 seconds.
Both runs used the recorded caller input, full configured prompt, and live
Speko service; model outputs and network scheduling were not deterministic.

## Limits and next measurement

The fixture demonstrates a removable local delay, not a guaranteed saving on
this handset call. Provider recognition, response generation, network transit,
and remaining playout delay still contribute. The old call has no per-stage
provider trace; a slow delegated LLM request must not be assumed to be its cause.

The 1200 ms target remains unproven. A subsequent explicitly requested real
call is needed to compare the corrected bridge, and synchronized acoustic
recording is needed to establish handset end-to-end latency. `done` does not
place another call.
