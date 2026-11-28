# Motor quotation combinations and partial caching

The requested test models one customer with a prewarmed set of common policies.
A first request outside that set uses the quotation API. A successful result is
then reusable for the same call, selection, pricing identity and input facts.
The warm-set choices in the voice test are representative test fixtures. Actual
production frequency ranking requires observed business traffic or an
operator-maintained list.

## Executed scope

The catalog has 413,677,492,904 dependency-valid structural combinations,
including the all-disabled state. The executed matrix contains 8,498 unique
policies. It exhaustively enumerates each coverage family and adds compatible
pairwise choices across fields. Coverage-family state counts are 26 for vehicle
damage, 3,601 for third-party liability, 1,051 each for driver and passenger
coverage, and two each for compulsory insurance and tax collection. These sum
to 5,733 family states. The full cross-product remains larger than the executed
matrix.

The deterministic matrix warm set contained 2,123 policies. All 2,123 hit cache
with zero new upstream requests. The other 6,375 policies each submitted exactly
one quotation API request. Repeating all 8,498 policies hit cache and retained
the same total. Changing the call scope or pricing facts triggered a new request.
The test passed both normally and under Go's race detector. Its pricing delay
was zero to exercise the HTTP request, polling, cache and validation paths at
matrix scale.

## Voice simulation

The candidate was `motorfull-20261112-a`, with the original saved instructions,
greeting, voice and provider configuration. A temporary originator assigned
call ID `90786112-6950-4cdf-9169-6ef4b1a3acfc` before dialing so that the same
call-scoped cache could be seeded ahead of the conversation. The two seeded
policies were the default plan and that plan with driver coverage at 5万元 plus
shared driver medical coverage.

The scenario changed third-party coverage to 300万元, then 400万元 with the
holiday rider, restored an earlier policy and requested a 200/300万元 comparison.
The call lasted 169.7 seconds and produced 23 caller and 24 assistant transcript
entries. Archive checks verified both audio directions. CRM recorded six cache
lookup events, including preparation/comparison lookups, and four new API
calculations during the call. Calculation durations were 4,144, 2,862, 4,356 and
3,673 ms, using the configured 2,500 ms base delay plus jitter.

Single HTTP probes after the call verified the routing explicitly.

| Probe | HTTP time | New quotation API requests |
| --- | --- | --- |
| Seeded common policy | 2.26 ms | 0 |
| First request for a new policy | 4,241.42 ms | 1 |
| Repeat of that policy | 1.80 ms | 0 |

The call's normal-response server content p95 was 1,170 ms across nine samples.
These are server measurements. The post-call probes measure business API time.

The restored policy's total required a caller follow-up question. Two quotation
tasks had missing readout evidence and another had unverified readout evidence,
with additional coalesced origins in the Journal. Cache routing passed while
speech delivery still needs follow-up. This test adds no claim about handset
latency.

Temporary originator, engine, CRM and pricing processes were stopped after the
test. The test Journal continues to serve the recording.

[Call Journal](http://192.168.71.9:18780/#/call/90786112-6950-4cdf-9169-6ef4b1a3acfc)

[Matrix evidence](evidence/2026-11-12-motor-cache-sim/matrix.json) ·
[Race evidence](evidence/2026-11-12-motor-cache-sim/matrix-race.json) ·
[Voice and API probe evidence](evidence/2026-11-12-motor-cache-sim/voice.json)

## Reproduction

From the repository root, the opt-in matrix runs with

```sh
GOLIVE_MOTOR_CACHE_SIM=1 GOLIVE_MOTOR_CACHE_REPORT=/tmp/motor-cache-report.json \
  go test -race ./internal/crm -run '^TestMotorCombinationCacheSimulation$' -count=1 -v -timeout 10m
```

The voice test uses the existing Mac runner, the Active Call transport and the
reserved customer simulator. It exercises an isolated candidate instance with
mock pricing and a predefined call ID for cache preparation.
