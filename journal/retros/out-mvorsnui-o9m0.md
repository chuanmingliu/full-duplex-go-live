# Call out-mvorsnui-o9m0 — 2026-10-26 12:42, build dafc8f8 (lead service 4a8f149)

A short real call (40 s, one answered turn) to try the pre-quotations.

## What the owner heard

"The first price query triggered a prehook response" — the default
quotation, ready since the ringing, was answered with "请您稍等" and a 15 s
wait (`took_ms=15174`), then read out with the posthook.

Cause: the lead service's `prequoteNeighbours` ran inside the prefetch's
goroutine. The default quotation is answered on that goroutine's `done`
channel, which closes when it returns — and it returned only when the last
of the eight neighbours had *started* (12:42:47.357, the same millisecond the
seventh finished). The ask outlived the ack's grace, so the prehook was
said, and the result was no longer `default` for golive's purposes in time.

Fixed in f61ad6a: the neighbours are priced on a goroutine of their own; a
test asks the default while they are in flight and requires it within 200 ms
(850 ms+ on the old code with a fast fake; 15 s on the box). Probe after the
deploy: default ask 24 ms with neighbours in flight.

## Figures

One content turn: first audio 1 ms (opener), first word 593 ms. Not a
latency sample.
