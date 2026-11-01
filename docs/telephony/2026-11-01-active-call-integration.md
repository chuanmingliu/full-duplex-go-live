# Shared improvements returned to Active Call

On 2026-11-01 the owner ended SIP/RTP replacement exploration and selected
`codex/telephony-latency` for further optimization with the original GoLive
configuration and Active Call. This is a selective backport from the committed
SIP branch through `ad1de9c`, not a merge of its transport or deployment tree.
The target started at `be11821`, which already includes 365-day working retention.

## Included changes

| Source commits | Shared behavior and reason |
| --- | --- |
| `0dfdcc0` | Check for a completed quotation immediately after submission; include HTTP time in the existing polling interval. Shorten only the standard waiting clause after an already completed opener; preserve custom text. |
| `151b2e3`, `f8e4d83`, `21477d9`, `f12b6f5` | Release obsolete acknowledgment bookkeeping after its own playback ends, or when a superseded turn never emitted audio. Retire already answered coalesced continuations without waiting for unrelated speech to finish. Preserve function-call history and real playback evidence. |
| `3201ec4`, `34ce33f` | Record quotation API phases and separate normal-answer latency from quotation completion in the report and Call Journal. |
| `302267b` | Preserve the originating caller's input timing independently of acknowledgment output, so interrupted or silent acknowledgments do not lose quotation provenance. |
| `7fc4237` | Let a newer caller answer use its own delivery grace window; an older quotation deadline must not immediately cut it off. |
| `2891b52` | Record what was actually played when a new generation replaces an old player owner. Include regression coverage for replacement and completion ordering. |
| `1ec5d8c`, `2891b52`, `ad1de9c` (measurement files only) | Accept UUID handset captures, separate quotation SLA, retain bounded audio-clock gap diagnostics, and document preflight checks. |
| `e509b9a` (adapted documentation only) | Restore the requested Codex badge and attribution policy on this branch. |

An additional focus fix keeps exact integer caller durations from the speaker
tracker. Reconstructing them from a floating-point share could make exactly
400 ms become 399 ms, incorrectly rejecting a barge-in. The 160/400 ms gates,
speaker masks, overlap clipping and echo exclusions are unchanged. Tests cover
both the boundary acceptance and the neighboring rejections.

## Kept out of this branch

The SIP/RTP adapter, Speko bridge, ringback transport, experimental deployment
overrides, service-specific Compose files and extra SIP dependencies remain on
their own branch. Original provider profiles, prompts, voices, recipients,
Active Call launch scripts and quotation calculation settings are unchanged.
The 365-day retention update is retained; permanent finished-call archives keep
their existing policy. This integration does not restart or deploy any service.

## Controlled comparison

The same opt-in test fixtures ran against target baseline `be11821` and the
integrated candidate. Full numeric samples and fixture hashes are in
[controlled-sim.json](evidence/2026-11-01-active-call-integration/controlled-sim.json).
These isolate engine and polling behavior; they are not a phone-call benchmark.

| Scenario | Baseline | Candidate | Interpretation |
| --- | ---: | ---: | --- |
| Standard waiting acknowledgment, cached PCM duration | 2380 ms | 1540 ms | Removes the redundant 840 ms clause only after a completed opener. |
| Ready result behind completed calling-turn bookkeeping | 3004 ms | 1503 ms | Honors the configured 1500 ms delivery wait without waiting for an unrelated 3000 ms floor. |
| Same, superseded silent calling turn | 3002 ms | 1504 ms | No invented completion receipt. |
| Same, preceding coalesced continuation | 3006 ms | 1504 ms | Already delivered bookkeeping no longer blocks the result. |
| Quotation ready immediately, median of 3 | 663 ms | 161 ms | Removes the initial polling sleep. |
| Quotation ready at 350 ms, median of 3 | 663 ms | 662 ms | Essentially unchanged; candidate uses an additional early poll. |
| Quotation ready at 800 ms, median of 3 | 1245 ms | 1163 ms | Avoids adding the 80 ms HTTP time to each polling interval. |
| Quotation ready at 1350 ms, median of 3 | 1827 ms | 1665 ms | Same benefit over more intervals. |

The quotation fixture uses an 80 ms query and a 500 ms configured poll interval.
Production calculation settings are untouched. Faster polling can add one query;
requests stay serial, and subsequent starts remain at least one configured
interval apart. The scheduling fixture has no newer caller endpoint, so it
tests the old acknowledgment blockage separately from the newer-answer guard.

These savings are conditional and are not additive. No new real-call p95 or
handset improvement is claimed. In particular, the focus rounding correction
does not by itself explain the earlier multi-second speaker-decision delay.

## Validation and future workflow

Regression coverage includes custom acknowledgment preservation, caller-floor
protection, unfinished playback, stale quotation rejection, newer-answer grace,
coalesced histories, replacement receipts, timing provenance and speaker gates.
The branch's CI now runs on pushes to `codex/telephony-latency` as well as PRs,
including normal and race Go suites, formatting, vet, build, vulnerability
checking and latency/handset tests.

Run locally:

```sh
go test -race -timeout 25m ./...
go vet ./...
go build -o /tmp/golive-active-call ./cmd/golive
python3 -B scripts/test_call_latency.py
python3 -B scripts/test_sim_retro.py
make handset-test
node web/transcript_test.mjs
GOLIVE_TOOL_LATENCY_SIM=1 go test ./internal/duplex ./internal/crm \
  -run 'Test(QuoteAckLatencySimulation|CompletedCallingAckLatencySimulation|SilentCallingAckLatencySimulation|CoveredContinuationAckLatencySimulation|QuoteLatencySimulation)$' \
  -count=1 -v
```

To reproduce the comparison, export `be11821` with `git archive` into a temporary
directory, copy the three `*_latency_sim_test.go` files listed in the evidence
JSON into that tree, and run the final command there. No credentials or real
phone connection are used by these fixtures.

Use Active Call for the next deployment and simulator/real-call comparison.
Inspect the live version and idle state first, preserve its rollback binary and
configuration, and use the existing Mac runner. Measure normal first content,
first audio and interruption detection-to-stop separately. Keep quotation
calculation and result readout separate. A server measurement plus the owner's
50 ms assumption is an estimate; actual handset verification requires a valid
acoustic capture. The sub-1200 ms goal remains open.
