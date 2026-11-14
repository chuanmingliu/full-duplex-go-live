# Expanded Chinese backchannel benefit and regression tests

The expanded tests support investigating the model as an **early interruption
signal under the existing controller's checks**. They do not support replacing
the original acknowledgment rules or directly cutting playback on a probability
threshold. Production Active Call, its original configuration, preset openers,
quotation delay and Aria routing remain unchanged. The model stays observation-only.

## Fixed test population

The protocol was frozen before evaluation with SHA-256
`3ad7bac88e0eac4e9d2e13f2272a69a48ab0db64e8c4d0cf1b95c3321dd7eb10`.
Model revision remains `4896f4774cdf1a9c5b581b0cd4a0c4000e8bb854`.
No threshold or case selection was tuned on the results.

| Cases | Count | Purpose |
| --- | ---: | --- |
| Original regression utterances | 12 | Retain earlier successes and failures |
| Configured acknowledgments | 8 | Protect “是的”, “原来如此”, “你接着说”, etc. |
| Acknowledgments outside the phrase list | 8 | Look for recognition the old list lacks |
| Short corrections / stop requests | 8 | Detect brief floor-taking without waiting for a long transcript |
| Acknowledgment followed by a request | 8 | Distinguish “嗯” from “嗯，不是…” |
| Statement/question prosody probes | 8 | Explore ambiguity without treating punctuation as a label |

That is **52 utterances**: 21 intended acknowledgments, 23 intended floor-taking
utterances and eight ambiguous probes. The original 12 WAVs were reused with
verified source IDs/text and recorded hashes. Forty new WAVs were synthesized
independently with an isolated copy of the running original GoLive engine and
its effective saved voice. No real call was placed. The synthesis process was
stopped before evaluation.

Each utterance ran in four conditions: clean 16 kHz PCM, an 8 kHz G.711 A-law
round trip, deterministic Gaussian noise at approximately 10 dB active-sample
SNR, and G.711 with an 80 ms delayed bot-reference echo mixed at -18 dB. The
noise level uses source samples with amplitude at least 300; it is not an SNR
estimate for the actual carrier. Echo uses a repeated synthetic bot reference,
not a real AEC system or measured acoustic path.

The matrix contributes 208 trials. Eight additional sequence trials vary the
acknowledgment/request gap (0, 160, 400, 800 ms, two codecs); four controls cover
silence, noise alone, echo alone and bystander speech. Twelve preselected G.711
regression trials repeat at real-time cadence. **Total: 232 synthetic trials.**
The repeated variants share source utterances and are not independent speakers.

## Benefit signal in existing Chinese recordings

All six eligible completed `f1fd9a6` audio archives were included (selection
rule: up to eight most recent, before inspecting predictions). This includes
the missed call and short simulator calls, with no exclusions based on outcome.
The recordings cover **469.21 seconds** and **25 existing interruption cuts**.

| Measurement | Result |
| --- | ---: |
| Model floor threshold available between VAD-open and existing cut | 20 / 25 |
| Three consecutive qualifying frames available before cut | 18 / 25 |
| No model threshold crossing in that window | 5 / 25 |
| Lead over existing cut, median among the 20 detected events | **174 ms** |
| Lead over existing cut, p95 / maximum among those events | 647 / 674 ms |
| Existing kept acknowledgment with model false floor crossing | 0 / 1 |
| Unpaired cut events | 0 |

The three-frame diagnostic's median lead is 149.5 ms across its 18 matched
cuts. Persistence reduces isolated crossings but does not eliminate synthetic
false cuts. Neither diagnostic was used to control any call.

Per-archive results preserve cases without useful signals:

| Call ID | Existing cuts | Earlier raw signal |
| --- | ---: | ---: |
| `out-mvykfqfn-wibn` | 2 | 2 |
| `out-mvykct0y-gklg` (missed call) | 0 | 0 |
| `out-mvxuom0e-mp6l` | 2 | 1 |
| `out-mvxr0ein-wwqh` | 15 | 14 |
| `out-mvxpoc4i-ta7z` | 3 | 1 |
| `out-mvxpmcug-bpqi` | 3 | 2 |

**These leads are opportunities, not measured live savings.** Recording input
is jitter-adjusted; four archives report dropped repeated input (80–1,140 ms),
so precise timing across those recordings is approximate. Bot state is inferred
from recorded output energy and lacks exact filler events. The model's
predictions were replayed, not delivered to the live engine. Caller identity,
ASR, echo gates and actual playback may prevent using an early signal. The
existing controller's cuts are comparison events, not independent human labels
of whether every interruption should have occurred.

## Delayed acknowledgment → interruption sequences

The sequence says “嗯嗯”, waits the predefined gap, then says “等一下，我想问个问题”.
All eight cases avoided a raw floor crossing before the second phrase and
crossed within 700 ms of that phrase's acoustic onset:

| Gap | PCM crossing after second onset | G.711 crossing after second onset |
| --- | ---: | ---: |
| 0 ms | 50 ms | 90 ms |
| 160 ms | 50 ms | 170 ms |
| 400 ms | 130 ms | 50 ms |
| 800 ms | 130 ms | 130 ms |

Median 110 ms; maximum 170 ms. This supports the intended distinction between
an acknowledgment and subsequent floor-taking for this particular voice and
phrase pair. It does not generalize to all Chinese acknowledgments. A complete
40 ms chunk must arrive before either of its two 20 ms predictions is available;
no future second-phrase audio is credited to the earlier acknowledgment phase.

## Synthetic accuracy and regression evidence

All rates below exclude the eight ambiguous prosody probes. A floor signal
before the intended caller onset is counted as a false early trigger, never
as a fast interruption success. Speech onset is the first clean-source 20 ms
frame with RMS at least 300; it is not a human annotation.

| Condition | Floor-taking detected within 700 ms without earlier false trigger | Intended acknowledgments with a floor crossing |
| --- | ---: | ---: |
| Clean PCM | 19 / 23 | 18 / 21 |
| G.711 A-law | 20 / 23 | 20 / 21 |
| Noise stress | 2 / 23 | 21 / 21 |
| Residual echo stress | 0 / 23 | 21 / 21 |

For clean PCM, the detected floor-taking subset has median 100 ms and p95
780 ms; one short “等会儿” never crossed the threshold. Three longer requests
beginning with acknowledgments crossed at 780–820 ms. All eight new combined
acknowledgment/request sentences eventually crossed, but only five met 700 ms.
Seven of eight new short corrections crossed, all within 260 ms.

The original phrase list recognizes ten of the 21 intended acknowledgments.
The raw model nevertheless crosses the floor threshold on seven of those ten
in PCM and nine in G.711. The eight new unlisted acknowledgments all cross in
PCM. There were **zero additional confident end-of-utterance acknowledgments**
under the predeclared tail diagnostic (mean probability >=0.7 in the first
200 ms after the acoustic end, with no floor crossing). That diagnostic is not
the upstream unpublished VAD/backstop implementation or a calibrated classifier.
These results provide no evidence of better acknowledgment coverage.

The G.711 transformation here covers the complete padded stream, including
codec-quantized silence. The earlier small trial encoded the speech clip before
zero-padding. Thus its G.711 numbers are not an exact repeated arm; the same
“对对” regression clip now has one threshold crossing (peak 0.723) but no
three-frame crossing. Clean PCM preserves the original input construction.
This near-threshold sensitivity is retained as a regression, not hidden.

Silence and the fixed-amplitude noise-only control did not cross. Echo-only
and bystander-speech controls did. The variable higher noise level in the
10 dB speech tests triggered pre-onset crossings in 19 of the 23 floor-taking
cases. The model cannot replace acoustic gating, echo rejection or caller
identity. Three-frame persistence is not a sufficient replacement for them.

The eight prosody probes remain unscored: a synthesized question mark does not
prove that a human would perceive a question or request to take the floor.
Their numeric results are retained for later listening/labeling, with no claim
of prosody classification accuracy.

## Performance, fallback and next use

All 12 paced sentinel repetitions matched their corresponding unpaced G.711
classification and audio-clock crossing times. Their local service round-trip
p95 ranged from **2.240 to 2.515 ms**. The run processed 40,324 additional model
chunks with **zero service errors**, finishing with zero active sessions.
These are service costs, not end-to-end conversational response latency.

The evidence favors testing a separately enabled, bounded model input to the
existing interruption controller. Retain the original phrase acknowledgments,
caller/echo/background checks, current timeouts, fresh per-call state and
immediate legacy fallback on uncertainty or service failure. The matrix does
not validate such an integration; this commit adds tests and evidence only.
The raw model is not ready to become the default floor controller.

Normal-response latency and quotation calculation were not modified or scored
as improvements. Quotation completion remains separate. No handset acoustic
measurement or optional +50 ms estimate is included.

## Validation and artifacts

Twenty Python tests passed locally, including the actual pinned ONNX smoke;
ten evaluator tests passed inside the disposable Linux container. The Go
original-profile regression and 52-case actual `PhraseSet` baseline passed.
CI includes the matrix and summary regression tests. Production executable,
profile, environment and saved call context matched their pre-test hashes;
Aria routing was unchanged and the isolated engine was stopped.

- [Case protocol](../../services/backchannel-detector/chinese_matrix.json)
- [Summary](evidence/2026-11-02-backchannel-expanded/summary.json)
- [Complete numeric results](evidence/2026-11-02-backchannel-expanded/results.json)
- [Actual Go phrase baseline](evidence/2026-11-02-backchannel-expanded/legacy-phrases.json)
- [Operation receipt](evidence/2026-11-02-backchannel-expanded/operation.json)
- [Exact operations and source hashes](../../scripts/local-ops/recorded/2026-11-02-backchannel-matrix/README.md)

The private source audio, generated speech, customer text, credentials and
per-frame traces remain on ThinkPad. No private call content is committed.
