# Simulated calls — 2026-10-26 13:55–, the pre-quotations (golive dafc8f8, lead service f61ad6a)

Three scenarios written for the quotation flow with the default plan priced
during the ringing and its neighbours priced ahead (`crm/prequote.go`):
`sim-prequote-hit` (default → one change → one more → "say it again"),
`sim-prequote-interrupt` (default → change → the readout cut by a question →
the total asked again), `sim-prequote-miss` (default → a coverage dropped →
a tier changed → the difference). On the box under
`~/golive-sim/scenarios/custom/`.

## Run 1: sim-prequote-hit, out-mvouebz7-9qxa, 13:55 (dafc8f8 / f61ad6a)

- 7 turns, 5 opened; content p50/p95 590.
- The default: asked at +9 s, answered in 1 ms from the prefetch, no ack,
  read directly. ✓ (the 12:42 fault is gone)
- The neighbours: 8 priced one at a time from 13:55:12 to 13:55:39, distinct
  totals, a caller's ask never behind them.
- **The change "三者险 → 300万" was priced on one coverage (total 1530), and
  the next change on two (1764).** The working memory held no plan after the
  default — the default is quoted with nothing recorded — so the fold of the
  change (`recordConfigChange`) made the plan `{"三者险":"300万"}` and the
  service priced exactly that; a miss, 3 s under the ack, and a wrong
  figure. The simulated customer took the figures as given.
- Fixed before run 2: the quotation's answer carries the `selection` it
  priced; the engine brings the memory's plan to it on delivery
  (`recordSelection`, coverages dropped from the plan removed); and the lead
  service lays a change with no plan in memory over the default selection
  (`DefaultSelection`). With that, the change on the default is the
  4-coverage neighbour and a cache hit.
