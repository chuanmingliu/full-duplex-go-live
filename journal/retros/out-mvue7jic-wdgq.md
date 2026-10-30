# Real configured-handset call — 2026-10-30, 461b79f

Call `out-mvue7jic-wdgq`, session `sess_b6adccb8b94eb7b2941cefe8`,
11:08–11:11 Asia/Shanghai. The owner signalled **done** before this review.
Runtime `461b79f`; saved prompt SHA prefix `639dcecf24dd`, 13,116 characters,
unchanged from the preceding real calls.

| Server-output measurement | Samples | p50 | p95 | Maximum |
| --- | ---: | ---: | ---: | ---: |
| Last caller sound → first audio, including fillers | 13 | 380 | 643 | 643 |
| Last caller sound → first non-opener content | 13 | 1025 | 1215 | 1215 |
| Last caller sound → linked result-turn content | 4 | 1001 | 1139 | 1139 |
| Explicitly measured quotation readout | 0 | unknown | unknown | unknown |

Timings are milliseconds, nearest-rank p95. Eighteen metric rows contain
thirteen caller responses and five non-speech responses. All four original
caller/result links are unique and verified. Three answers were interrupted
after content began. First non-opener content can be a lead-in; result-turn
content can be a cached posthook. Neither establishes that the figures have
begun. The stricter readout report marks all four historical readout times
unverified because this build has no explicit readout-output stamp.

The strict gate **fails**: fewer than twenty caller samples, first-content
p95 of 1215 ms, and four unverified quotation-readout boundaries. The
physical handset leg is also unmeasured.

## Previous handoff fix worked

The guarded handoff ran for `item_2 → t1 → item_3`,
`item_5 → t2 → item_6`, and `item_11 → t4 → item_12`. Ready results waited
29, 13 and 0 ms respectively for the opener's natural completion. The
third task, `item_8 → t3 → item_9`, needed no suppression: its opener was
already finished when the task completed. It was delivered immediately.

All original rows were retained as clean, opener-only completions. All
deliveries had `interrupting=false` and unchanged memory. The fourth task
actually suppressed an already generated calling body; the first two
finished before one had been generated. No extra scheduler tick or
acknowledgment-grace wait remains evident in this call.

## Cached posthook still hides and delays the figures

The default first quote skips the result posthook, so its observed
1139 ms result-content time also corresponds to the readout by inspection.
The other three begin with the built-in cached result posthook, lasting
1264 ms. Their reported result-content times of 923, 1079 and 881 ms mark
that phrase, not the actual figures. Readout synthesis was ready after
265, 185 and 508 ms from result start, but readiness is not playback.
The player still had the posthook ahead of it. Historical logs do not
provide a separate strict readout-output timestamp.

The implemented change distinguishes posthook, deterministic readout and model
continuation at the actual output boundary. It reports readout presence
and timing separately while preserving existing first-content semantics.
The built-in posthook is omitted only when the same completed caller
origin has already emitted an opener, memory is unchanged and the floor is
free. Custom posthooks retain their configured behavior.

The fourth readout also coincided with an abandoned MiniMax task drain of
276 ms. The logs lack connection IDs, so attribution to that exact request
is an inference. Source review and a failing wire regression found that a
request canceled while waiting for the stream could still send a new
synthesis task when the drain finished. A context check after the lock now
prevents that send. Expected hedge cancellation also preserves a healthy
connection; actual transport failures still trigger recovery. The provider
drain barrier remains intact, avoiding stale audio leaking into a new turn. The sole ordinary-content sample above target was the closing
reply (`item_20`): 835 ms after speech close plus the 380 ms pause. Its first
TTS segment took 401 ms, with the configured fallback starting at 320 ms.
No VAD setting was changed based on this sample.

## Conversation and recording

The first two quotation transcripts end inside numeric strings because
the caller interrupted them; the full synthesized figures matched their
authoritative rounded results. The comparison totals and difference were
consistent, and the later selected quote matched all five line items.
The requested repeated price was correct. No new pricing defect was
established.

An explicit final farewell led to a reply and the assistant-goodbye
fallback. Earlier problematic standalone closing phrases were not repeated,
so ISS-069 remains open. One reply used wording about locking the plan and
sending a link without a corresponding exposed action tool. This is a
capability-wording concern, not proof that an external notification failed:
the saved prompt contains notification language and the caller later
acknowledged seeing it. No external action or failure was invented.

All five required archive files passed size and SHA-256 checks. The
159.8-second WAV is stereo PCM16 at 16 kHz, with signal on both channels.
There are thirteen caller and eighteen assistant transcript entries and
thirteen cached filler events. Original audio, session and log fingerprints
were saved for deployment verification. Private recordings, prompts,
transcripts and business details remain outside GitHub.

The recording input is on its clock and reports 80 ms dropped; this is
not subtracted from latency. Echo analysis found no usable return: strongest
significance 3.2 versus required 6, and zero significant stretches among
twenty eligible. It cannot establish physical handset playout. A separate
local acoustic recorder and sample-boundary verifier are available for
future calls; synthetic tests are not real handset evidence.

## Candidate verification

Readout timing, guarded posthook omission, task-origin and delivery tests
passed, including twenty repeated race runs. CLI and simulator reporting
passed 37 and 10 regressions, and the journal race suite passed. All provider
packages passed with the race detector; the canceled-request and drain
regressions passed ten repeated runs. Independent source reviews found no
actionable issue in the output-role handling or provider cancellation.
`go vet ./...` and diff checks passed. Local handset tooling passed 24
verifier, 3 loopback-server and 13 browser-controller/PCM tests. No microphone
was opened and no physical handset capture has yet been made.

Full candidate CI, guarded box deployment and a new real-call measurement
are pending. These tests establish implementation behavior, not attainment
of the physical handset latency goal.
