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
