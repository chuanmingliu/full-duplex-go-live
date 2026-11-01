# Tool execution and acknowledgment latency comparison

This change keeps the original GoLive configuration files and Active Call
service intact. It is tested in isolated copies, not deployed over the original
real-call services. Baseline source is `4003b1d`; candidate changes only quotation
poll scheduling and the standard waiting acknowledgment.

## Changes

**Quotation polling:** check once immediately after the asynchronous submission
returns. Schedule later query starts at the existing configured interval,
counting HTTP query time toward that interval. Queries remain serialized, stale
IDs/flows and unpriced records remain rejected, and the configured deadline stays
in force. The interval remains 500 ms in the deployed private configuration;
this change does not shorten it or bypass upstream pricing. There can be one
additional early query. Polling phase means an individual readiness time need
not improve even when average polling overhead is reduced.

**Acknowledgment:** after the originating caller's opener has naturally completed,
the exact standard waiting script emits its existing first cached clause and
omits the redundant final request to wait. The full 17-character script remains
in the configuration; the guarded playback is 12 characters. Custom panel text
and acknowledgments without a completed originating opener remain unchanged.
The shorter turn completes naturally: no mid-word cancellation, invented spoken
history, or bypass of caller interruption/result freshness guards is introduced.

This addresses local overhead. It does not make the business's pricing computation
faster. The previous real call's 4162 ms tool interval included a CRM-reported
4006 ms upstream API operation; seconds of that work can remain on a cache miss.

## Controlled simulations

Both source snapshots run the identical opt-in tests with local HTTP and cached
PCM fixtures. Each HTTP case has three repetitions, 80 ms query response time,
500 ms polling interval, unchanged upstream readiness and the same quoted value.
Durations below include the initial previous-quote lookup.

| Upstream readiness after submit | Baseline median tool time | Candidate median | Saving |
| --- | ---: | ---: | ---: |
| 0 ms | 664 ms | 162 ms | 502 ms |
| 350 ms | 664 ms | 664 ms | 0 ms |
| 800 ms | 1246 ms | 1165 ms | 81 ms |
| 1350 ms | 1827 ms | 1666 ms | 161 ms |

The actual Player pipeline emits 2380 ms of fixed baseline acknowledgment PCM
versus 1540 ms for the candidate. With the simulated result ready 610 ms after
acknowledgment start, the modeled remaining wait is 1770 versus 930 ms:
**840 ms saved**. Those fixed PCM fixtures approximate the preceding real call's
1545/835 ms clauses. They isolate playback duration; they are not live TTS or
handset measurements. Savings from the HTTP and playback cases must not be added
as though they came from a single measured call.

Reproduce on either snapshot with the same simulation test files:

```sh
GOLIVE_TOOL_LATENCY_SIM=1 go test ./internal/crm ./internal/duplex \
  -run 'TestQuote(Ack)?LatencySimulation$' -v -count=1 -timeout 60s
```

## SIP simulator protocol

The broad run uses baseline–candidate–candidate–baseline with the established
120-second scenario. A separate baseline–candidate pair uses a 90-second scenario
with explicit synthetic quotation changes at 25 and 60 seconds. The same saved
assistant prompt, greeting, voice, provider settings and immutable copied CRM
configuration are used in each cohort. Each trial starts fresh engine/CRM
processes, resetting in-memory caches. The business quotation endpoint remains
shared; it is not replaced with an artificially faster service.

The adapter binary stays `fb11e2c`, PCM16 to GoLive and PCMU to RustPBX, with
independent SIP Contact. Only reserved simulator URIs can be dialed. The broad
stack uses engine/CRM/Journal 9905/9907/9906 and SIP 15066; the focused stack uses
9915/9917/9916 and SIP 15068. The original engines, real-call candidate on 9895,
Active Call and original Journals stay available.

Receiver first-audio boundaries include fillers and are not quotation-readout
or handset measurements. Provider-warning calls must not be pooled into a
successful latency qualification. Natural-language simulator turns vary even
under the same scenario, so per-call figures are descriptive, not paired causal
proof. Canonical first content and linked readout results are reported separately.

## Observed SIP results

All six calls terminated and the adapter exited zero. Thirty archived files
passed size/hash checks. Five calls have both transcript speakers and signal in
both audio channels; broad candidate trial 3 has assistant-only text and fails
conversation completeness despite nonzero signal in both recorded channels.
These failures remain in the report, not silently removed from the population.

| Cohort / trial | Build | Valid receiver samples | Receiver median / p95 | Content samples / p95 | Receiver or capture limitation |
| --- | --- | ---: | --- | --- | --- |
| ab 1 | baseline | 7 | 541 / 1390 ms | 15 / 1124 ms | No receiver warnings |
| ab 2 | candidate | 2 | 635.5 / 812 ms | 14 / 1046 ms | warning: tts_chunk_failed (count=3) |
| ab 3 | candidate | 1 | 54 / 54 ms | 0 / — ms | warning: tts_chunk_failed (count=3); caller transcript missing |
| ab 4 | baseline | 4 | 345.5 / 1047 ms | 14 / 1148 ms | warning: tts_chunk_failed (count=2) |
| focused 1 | baseline | 2 | 321.5 / 525 ms | 9 / 992 ms | No receiver warnings |
| focused 2 | candidate | 4 | 260.5 / 401 ms | 5 / 25796 ms | warning: tts_chunk_failed (count=1) |

The broad scenario exercised no quotation tasks. Its candidate and final baseline
calls carry receiver TTS warnings; do not pool them into a successful comparison.

The focused baseline had tool execution times 0, 3, 5818, 1 and 8427 ms;
candidate completed tasks took 1, 5866, 1 and 6140 ms. Requests, cache hits,
interruptions and task counts differ, so these are **not matched tool timings**.
Neither focused call emitted a scripted acknowledgment: the 840 ms saving is
verified by controlled Player tests, not exercised by these SIP calls.

Known deterministic readout evidence is only one sample per focused call:
13177 ms baseline and 11786 ms candidate. Both strict gates fail. Baseline has
one missing and four unverified canonical records, four invalid tool links and
one unverified readout; candidate has one unverified canonical record, one invalid
link and three unverified readouts. Candidate content p95 is 25796 ms including
the measured linked result. Do not treat these incomplete populations as a
successful performance qualification or claim a percentage improvement.

Some ready results waited 8.9–18.2 seconds behind ongoing speech in the candidate.
The fixes here do not bypass existing speaking-floor and freshness protections.
A separate follow-up must reproduce those waits and ambiguous/interrupted result
links before changing scheduling. Upstream API execution and caller/assistant
overlap remain larger bottlenecks than SIP packet scheduling.

## Evidence and rollback

Broad Journal: <http://192.168.71.9:9906>. Focused Journal:
<http://192.168.71.9:9916>. Individual call links and qualification details are
in the repository retrospectives and the corresponding Journal notes.
The [recorded operations](../../scripts/local-ops/recorded/2026-11-01-tool-ack/README.md)
include reviewed runner jobs, isolated process setup and archive verification.

Both comparisons verified byte-identical original profiles, saved-call file,
real adapter settings and versioned CRM configuration. Active Call is still
running. The original 8765 engine and real SIP stack on 9895 were idle throughout;
only temporary test engine/CRM processes were started and stopped. Their candidate
binaries remain in the experiment directories for reproducibility. The Journals
remain running so recordings stay accessible. No real call was placed.

Do not promote this as a sub-1200 ms handset release. The original runtime needs
no rollback because it was not replaced. A future candidate deployment must keep
that same original service available and require an explicit handset test.

## Validation

The immediate-ready-query and shortened-acknowledgment regressions fail against
baseline and pass against candidate. Tests retain old/unpriced-result rejection,
poll cadence, custom acknowledgment text, missing-opener protection, natural
completion, result ordering and posthook ownership. Focused race checks and CRM
race tests pass. The CRM queue-priority fixture now models actual upstream work
instead of relying on an unconditional polling sleep for its timing window.

A full all-package race run passed every non-duplex package but was interrupted
during the long duplex suite to inspect its progress. That run also saw the
existing application-function parity test time out; it passes independently on
both baseline and candidate. The remaining 11 duplex tests pass separately under the race detector (36.452 s).
This is not a claim that the initial all-package invocation was green.
