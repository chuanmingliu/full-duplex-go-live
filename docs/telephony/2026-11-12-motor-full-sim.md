# Full motor coverage call simulation

Two Active Call simulations exercised local candidate `motorfull-20261112-a`,
built from `codex/telephony-latency` plus the uncommitted full-coverage and quotation
memory changes. A temporary originator on port 3005 directed the existing
Active Call transport to the candidate engine on port 18765. The candidate CRM
and mock pricing service used ports 18791 and 18792. Provider configuration,
saved instructions, greeting and voice came from the running GoLive setup.

The first scenario exercised default pricing, driver coverage at 5万元 with a
shared medical limit, third-party coverage comparison at 200/300万元, a 10%
deductible, the holiday rider and vehicle-tax collection. Backend requests
confirmed each selection. Cancellation removed both the driver main coverage
and its medical rider. The second scenario explicitly requested retaining the
rider while removing its parent. Xiaocui explained the dependency and asked the
caller to choose.

| Call | Duration | Normal response samples | Server content p50 | Server content p95 | Quotation readout p95 |
| --- | --- | --- | --- | --- | --- |
| `out-mwd5qebd-hju2` | 200 s | 4 | 849 ms | 937 ms | 8821 ms |
| `out-mwd5wh8v-x3un` | 86.7 s | 6 | 936 ms | 1180 ms | 5696 ms |

Eight mock calculations had median duration 3946 ms and maximum 4486 ms. The
pricing service used its 2500 ms calculation delay plus 0–2000 ms jitter.
Quotation readout timing also includes tool execution and speech scheduling.
The normal-response figures measure server output. Handset acoustics were
outside this simulator run. The small sample counts limit percentile conclusions.

Both calls ended and passed permanent archive hash/size checks. Both transcript
speakers and both stereo audio directions contained data. The test Journal serves
these records at port 18790. Temporary originator, engine, CRM and pricing
processes were stopped after collection.

## Findings requiring follow-up

- The first call recorded a complete quotation readout twice consecutively.
- When the caller initially requested removing only the driver main coverage,
  the model also removed its medical rider before separate confirmation. The
  explicit-conflict scenario did produce a dependency explanation.
- The transcript included redundant waiting phrases around quotation delivery.

The first reservation attempt was rejected for exceeding the simulator's
300000 ms duration limit. The corrected scenario used 295000 ms and the focused
scenario used 160000 ms. The rejected attempt placed zero calls.

The live service reported build `951a6b4` with its original Active Call endpoint
and matching profile hash after the tests. This candidate remains a test build.
The complete numeric evidence is in [results.json](evidence/2026-11-12-motor-full-sim/results.json).

The recordings are available in the [full flow](http://192.168.71.9:18790/#/call/out-mwd5qebd-hju2)
and [dependency test](http://192.168.71.9:18790/#/call/out-mwd5wh8v-x3un).
