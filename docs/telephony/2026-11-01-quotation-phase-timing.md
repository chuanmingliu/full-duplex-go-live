# Quotation latency phase measurements

The recorded original-GoLive SIP trial on `f8e4d83` had a cold revised quote
task taking 5266 ms, including a 4006 ms quotation-service operation. Those
totals cannot distinguish API serialization, HTTP duration and polling waits.

The CRM now emits two numeric diagnostic records:

- `crm: quote gate acquired`: foreground/background flag, memory version and
  time waiting for the shared API gate. The selection-follow worker acquires
  its gate separately; absence of this record is not proof of zero queueing.
- `crm: quote phases`: baseline-query time, inquiry submission time, cumulative
  polling HTTP time, cumulative polling sleep, number of polls, poll errors,
  stale/unpriced replies, total duration and terminal phase.

The existing call UUID and memory version correlate records. The added fields
contain no credentials, request/response bodies, prices or customer details.
The fields measure local elapsed time, including HTTP transfer and parsing;
they do not measure the upstream's internal compute time directly.

Request ordering, polling cadence, freshness checks, timeout, cache keys,
serialization and saved provider/configuration files remain unchanged. The
shared API gate is necessary: the upstream returns the latest quote for a
phone/vehicle, so concurrent inquiries can associate a price with the wrong
coverage plan. Phase evidence must precede any change to that scheduling.

The existing HTTP simulation verifies that two stale replies and an unpriced
reply are still rejected, and that the measured phases reconcile with total
duration. No latency improvement or handset qualification is claimed for
instrumentation itself. Future qualification uses strictly below 1150 ms
server p95 with the owner's assumed 50 ms telephony/handset overhead, retains
the 20-response minimum and requires separate actual-handset evidence.

## Recorded evidence

The diagnostic call `sim-quotephases-916ac344-59ab-4e0b-a527-2fd393c75a40`
completed with verified two-sided archives and no receiver warnings. A revised
quote waited **2852 ms for the API gate**, then **4508 ms for the API operation**.
Within that operation, polling HTTP consumed 4 ms and polling sleeps 4503 ms;
nine queries still returned the old quotation before the new result appeared.
Another foreground operation took 4005 ms with no gate wait.

The configured endpoint is **the local quotation test service**, not a remote
insurer endpoint. Read-only runtime inspection found `quotesvc-linux-amd64`
PID 1405266 listening on loopback port 8793 with `-compute-ms 2500 -jitter-ms
2000`. The saved CRM config selects that endpoint and a 500 ms polling interval;
neither the original CRM nor the diagnostic process overrides the base URL.
`cmd/quotesvc/main.go` defines these flags and `internal/quotesvc/quotesvc.go`
intentionally sleeps before publishing a quote. Fresh prices cannot become
available inside 1150 ms while this minimum is preserved. Faster polling could
reduce detection lag, but cannot erase the minimum computation delay.

The diagnostic's server content p95 was 1016 ms (8 valid responses), first audio
978 ms (9), linked tool result 8836 ms (4), and quotation readout 9762 ms (4).
The strict gate fails on insufficient samples, one missing response, two
unverified links and quotation latency. Adding the assumed 50 ms overhead does
not establish actual-handset latency. See the
[retrospective](../../journal/retros/sim-quotephases-916ac344-59ab-4e0b-a527-2fd393c75a40.md).

No test-service delay, provider setting or original service was changed. The owner explicitly chose to retain the original delay, target fast
conversational responses, and report quote completion separately. The earlier
strict shared-SLA results above remain historical results under the previous shared policy.

## Accepted qualification policy

On 2026-11-01 the owner chose: “Keep the original delay; target fast
conversational responses and report quote completion separately.” New checks use:

```sh
python3 scripts/call_latency.py PRIVATE_LOG --json --gate \
  --target-ms 1150 --min-turns 20 --metric both --quote-sla separate
```

The default `shared` policy retains the old gate for reproducibility. `separate`
keeps all quoted-amount and tool-result distributions visible, but does not apply
the conversational time limit to verified quote completion. It gates first audio
and `conversational_first_content`. An explicit quote-only handoff is reported
and excluded from conversational content samples; it cannot turn an opener into
a fast answer or count toward the 20-content-sample minimum. Slow non-quote tool
results remain gated. Missing, malformed, duplicate and unverified evidence,
including readout provenance, still fails qualification.

The 50 ms telephony/handset overhead remains an assumption. The accepted change
separates quote completion from the conversational SLA; it does not establish
actual handset timing or remove the requirement for physical-playout evidence.

Re-evaluating the same archived diagnostic under `--quote-sla separate` gives
conversational content p95 **1016 ms, n=7**, first audio p95 **978 ms, n=9**, and
separately reported quotation readout p95 **9762 ms, n=4**. One verified
quote-only handoff is excluded from conversational content. The gate still
fails: one missing response, two unverified links, and fewer than 20 valid
samples. The assumed handset content estimate is 1066 ms, not a qualification.
All 56 public-command regression tests pass; the branch CI now runs them too.
