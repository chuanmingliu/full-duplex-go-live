# Native GoLive transport comparison — 2026-10-31 / 2026-11-01

The fixed adapter sustained full simulator conversations and clean remote
hangup. Receiver latency is comparable to Active Call, not a demonstrated
large improvement. The strict 1200 ms target still fails on quotation readout.

The owner stopped Speko/GPT-Live-1 and Realtime mini optimization and requested
simulator comparisons of Active Call versus our SIP/RTP adapter, using the
original GoLive configuration. No real number was dialed in this experiment.

## Configuration and evidence boundary

The original engine is `9547d22`, SHA-256
`e8648a7942d1e7c6a2c726033ab86efd786ca5801d302712d95cbc1a9bc7c493`.
The adapter test instance copies that exact executable and the running
process environment. The base profile SHA-256 is
`072ab8dbda962eff682fa56fcd049f70cd3717530e64a06bec97e66618878e7d`.
Only isolation settings differ: port 9895, capacity one, held opening,
private recording/last-call paths, and no Active Call hangup URL.
The production service, older SIP stacks, and Speko deployment are preserved.

Both lanes use the saved instructions (SHA-256
`639dcecf24dddc73274a7412c017e36258b51679c2fd3ad4e72a595453aa8d25`),
opening, and `alloy` voice setting. Providers remain Tencent ASR, DeepSeek,
and MiniMax with the original configured hedges. Endpointing, speculation,
40 ms playback chunks and 300 ms playback lead are unchanged.

The synthetic customer uses a copy of `kb-coverage-nerd`, with a 120-second
limit and 20 remote turns. Its live model is nondeterministic: matching the
scenario is not matching individual utterances. The receiver does not support
audio retention; GoLive/adapter recordings are retained separately. An initial
reservation was rejected for requesting unsupported receiver audio retention;
it created no call. The corrected scenario was used for every reported call.

Private evidence is under
`/home/chuanming/golive-sip-ab/native-ab-20261031` on the box. Receiver metrics
measure application audio arrivals, can include fillers, and exclude invalid
overlap/missing-response observations. Server content metrics are a separate
boundary. Neither establishes acoustic handset latency or semantic quality.

## First A–B–B–A round

| Lane | Call ID | Valid receiver samples | Median | p95 | Result |
| --- | --- | ---: | ---: | ---: | --- |
| Active Call | `out-mvwkzvfr-yaio` | 6 | 477.5 ms | 601 ms | Duration limit |
| Adapter, direct G.711 | `ab-sim-0af3d93d865cbcfda83d6a9a` | 0 | — | — | GoLive silence hangup |
| Adapter, direct G.711 | `ab-sim-7c1bae45eb1108cbd0f7ce04` | 1 | 79 ms | 79 ms | GoLive silence hangup; not a valid performance comparison |
| Active Call | `out-mvwl4dps-o1fb` | 10 | 465.5 ms | 561 ms | Remote-turn limit |

Active Call combined: 16 valid samples, median 468.5 ms, p95/max 601 ms.
Its two runs excluded 15 and 10 invalid observations respectively. The single
79 ms observation in a failed adapter conversation is not a latency success.

Active Call server content p50/p95 was 957/1184 ms (11 samples) and 872/1130 ms
(13 samples). Both originals have five checksum-verified archived files,
including stereo audio with signal on both channels. Their Journal is on
port 8790. The server's fast filler latency is not content latency.

Both G.711 adapter runs stopped after roughly 37 seconds. The engine received
packets but recorded no recognized caller turn; the simulator emitted very
little speech after its greeting. Receiver logs included echo-like barge-in
warnings; that warning is diagnostic and does not itself reject audio. SIP captures show the adapter's BYE was acknowledged by RustPBX.
This is not a reproduction of the unresolved remote-BYE propagation fault.
The cause of the failed conversations was not established by these logs.

## Native PCM compatibility change

The first round also exposed a configuration mismatch: Active Call negotiates
16 kHz PCM, whereas direct G.711 selects an 8 kHz GoLive session/TTS rate.
GoLive already resamples input to its ASR pipeline rate; this does not imply
that Tencent ASR itself ran at 8 kHz. Provider output and conversion paths still
differ, so the initial lanes were not equivalent at the audio interface.

Commit `f758708` adds `golive_audio_format: pcm16` to preserve the original
16 kHz GoLive connection while keeping SIP/RTP G.711. It confirms the agreed
format/rate before dialing, converts streaming input/output without batching,
and handles fragmented samples and cancellation. Direct G.711 remains the
default for existing configurations. This is compatibility work, not yet a
claim that the silence failure or end-to-end latency is fixed.

Local tests passed for both codecs and Realtime dialects, including actual
mock ASR/LLM/TTS cooperation. The controlled sender experiment (60 samples)
measured median/p95 0.144/0.224 ms for the adapter versus 10.073/20.477 ms for a
modeled Active Call timer. The modeled timer is not an actual Active Call run.

## Separate codec and interface experiments

Two subsequent PCM16/PCMA runs also silence-hung-up, despite warming the
opening cache: `ab-sim-be9bd17350601764704e79f6` (one 112 ms sample) and
`ab-sim-d6ac412471e6f0e7122359df` (126 and 166 ms). Those numbers are excluded
from successful-conversation comparisons. Changing the GoLive interface and
warming the opening did not fix the failure.

The deployed simulator gateway has `codecs = ["pcmu"]`. RustPBX logs show
PCMA-to-PCMU transcoding for all four failed trials, whereas the Active Call
baseline uses direct PCMU relay. Keeping the PCM16 interface and changing
only the adapter's isolated phone profile to PCMU restored conversation.
This localizes the failure to the deployed transcoding route; it does not
identify the faulty transcoder code or prove a general defect in PCMA.

| PCM16 + PCMU call | Receiver samples | Median | p95/max | Excluded | End |
| --- | ---: | ---: | ---: | ---: | --- |
| `ab-sim-015ab58a08ac39a2e46abd3a` | 7 | 429 ms | 493 ms | 10 | Receiver inactivity; stale adapter cleaned up |
| `ab-sim-869f87606ad0f66cb0bc4f38` | 12 | 515.5 ms | 612 ms | 5 | Receiver duration limit; stale adapter cleaned up |

The receiver also reported `tts_chunk_failed`: two warnings in the first
run and one in the second (16 and 18 remote turns respectively). Its logs
redact the provider error. These are additional qualification failures and
confound a clean transport-only comparison; no successful scenario gate is
claimed for either run.

Combined PCM16/PCMU: 19 valid receiver samples, median 457 ms, p95/max 612 ms.
Against the first two Active Call runs, the median difference is only 11.5 ms
faster, while p95 is 11 ms slower. These unpaired conversations do **not**
establish a material latency advantage. They do demonstrate that the direct
adapter can run the native GoLive conversation over the existing PBX.

Server content for those two runs: 893.5/1736 ms p50/p95 (12 samples) and
911/1009 ms (15 samples). Both have complete measured output records, but
neither reaches the per-call 20-sample minimum, and the first fails the
1200 ms content p95 threshold. Interruptions were recorded (six and four
interrupted answers respectively); their occurrence does not establish
acoustic barge-in responsiveness.

## Remote hangup defect and contact isolation

Both conversational adapter runs required explicit cleanup after the receiver
ended. A capture covering the PBX port shows the exact failure:

1. The simulator sends BYE to RustPBX; RustPBX acknowledges it.
2. RustPBX constructs the correct request URI,
   `sip:1001@192.168.71.9:15064`, for the adapter dialog.
3. It nevertheless sends the UDP packet to Active Call at port **13050**.
4. Active Call returns **481**. No BYE reaches the adapter's port 15064.

The adapter and Active Call shared contact user `1001`. This is a routing
identity collision on the deployed PBX, not evidence that the adapter ignores
a BYE actually delivered to it. SIP traces remain private because they contain
authentication material.

Commit `fb11e2c` adds optional `contact_user`, separating the dialog Contact
from From and Digest authentication. It preserves existing behavior when
omitted. A dedicated Contact can be trialed without replacing Active Call's
registration or changing the original configured caller identity. Network
regressions verify the contact survives Digest retry and remote BYE closes
the real GoLive session; malformed contact values are rejected.

Full CI passed for `f758708` (run `41217437376`). The contact change passed
local race tests and full CI run `41218586568` (both verify and simulate).

## Final latency controls

A further direct-G.711/PCMU run (`ab-sim-de288a209a0b84d4fb21f194`) had four
valid receiver samples: 36, 281, 1194 and 1289 ms; median 737.5 ms, p95/max
1289 ms, 15 invalid observations. It reached 12 remote turns and the duration
limit, reported one receiver TTS warning, and required stale-session cleanup.
Its 8 kHz native session differs from the original Active Call audio interface.

The final original Active Call control (`out-mvwlp9ve-u0i0`) reached 19 remote
turns and the duration limit without receiver failures. It had 10 valid gaps,
median 462 ms, p95/max 567 ms and eight exclusions. Server content was
899.5/1075 ms p50/p95, 16 samples, with complete output evidence. Its five-file
archive, two audio channels and both transcript speakers were verified.

| Transport cohort | Runs | Valid receiver samples | Median | p95 | Max |
| --- | ---: | ---: | ---: | ---: | ---: |
| Original Active Call, PCM16/PCMU | 3 | 26 | 466 ms | 569 ms | 601 ms |
| Adapter, PCM16/PCMU | 2 | 19 | 457 ms | 612 ms | 612 ms |
| Adapter, direct G.711/PCMU | 1 | 4 | 737.5 ms | 1289 ms | 1289 ms |

The PCM16 adapter median is nine ms lower but p95 is 43 ms higher. Different
utterances, excluded observations, receiver TTS warnings and teardown failures
prevent treating this as a controlled speedup or a replacement qualification.
The isolated Contact-routing test uses a shorter duration and is kept out of
this latency table. Do not pool the failed PCMA conversations into it either.

## Contact-fix box verification

The 60-second probe `ab-sim-9e85dfdc366bfa72807d7a19`, using `fb11e2c` and
`contact_user: golive-sip-adapter`, completed 11 remote turns with no receiver
failures. The adapter returned exit 0 without external cleanup. Capture shows
RustPBX now sends BYE to **15064**, and the adapter returns 200 about 0.2 ms
later. From and Digest credentials remain unchanged.

Six receiver samples had median 451 ms and p95/max 556 ms. These are diagnostic
only: the shorter scenario is excluded from the full-length latency comparison.
This proves the captured routing fault is corrected in that isolated test;
it does not qualify remote hangup across all carriers or change the preserved
Speko/handset configuration.

The experiment also has unpaired cache history: the two GoLive processes'
phrase caches and live provider conditions were not synchronized. A small
median difference is not a causal estimate of transport savings.


## Fixed-candidate qualification and final comparison

`fb11e2c` was tested twice with PCM16, PCMU and the independent Contact using
the same 120-second/20-turn scenario as the original baseline. Both calls
returned exit 0, handled remote hangup without cleanup, and reported no
simulator failures:

| Call | Remote turns | Receiver samples | Median | p95/max | Excluded |
| --- | ---: | ---: | ---: | ---: | ---: |
| `ab-sim-d2d5e0c524f68bb324328851` | 20 | 9 | 453 ms | 566 ms | 11 |
| `ab-sim-0cb916cd1580aa3f47dc3397` | 17 | 11 | 448 ms | 641 ms | 6 |

| Final cohort | Valid receiver samples | Median | p95 | Max |
| --- | ---: | ---: | ---: | ---: |
| Original Active Call, three controls | 26 | 466 ms | 569 ms | 601 ms |
| Fixed adapter, two full-length calls | 20 | 450 ms | 574 ms | 641 ms |

The median is 16 ms lower and p95 is 5 ms higher. All these valid first-audio
samples are below 1200 ms, but the small unpaired difference does not establish
a causal speedup. There are 33 excluded observations in the Active Call cohort
and 17 in the fixed-adapter cohort. Reported p95 uses nearest rank; medians use
the average of the middle pair for even sample counts. The historical runner
progress output used the lower middle value; the tables use the corrected
`compare-transport-reports.py` summary.

The fixed adapter's combined server evidence has 28 answered caller turns:
first-audio p50/p95/max 380/381/471 ms and ordinary first-content
974/1102/1106 ms. However, two linked quotation readouts took **6186 and
7172 ms** from the caller's last sound. Their p95 is **7172 ms**, so the
strict content/readout gate **fails**, even with sufficient pooled caller
samples. The first-content boundary can be a lead-in and must not conceal
this business-result latency. Investigate quotation preparation, call setup
and backend timings next; these runs do not establish the specific cause.

## Retained state

Thirteen simulator calls were made: three original controls and ten adapter
trials, including failed routes and the shorter Contact probe. All have
five-file, checksum-verified archives with signal on both audio channels.
The four failed PCMA calls retain their actual assistant-only transcripts;
no caller text was inferred. Raw SIP traces remain private.

The tested candidate is retained on the box with a private simulator profile
using PCM16, PCMU and `contact_user: golive-sip-adapter`. It still requires a
fresh simulator reservation. The temporary engine on 9895 and owned capture
containers were stopped after collection. Original GoLive remains `9547d22`,
with its profile, prompt, greeting and voice unchanged. Only normal saved-call
receipt metadata (`call` and `saved_at`) changed during baseline calls.
The real-call default and preserved Speko services were not switched.

The [recorded operation bodies](../../scripts/local-ops/recorded/2026-11-01/README.md)
retain simulator guards, binary hashes, immutable attempt receipts and archival
steps. This is simulator qualification, not a physical handset latency result
or approval to replace the production Active Call path.
