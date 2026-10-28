# Simulated calls — 2026-10-26 13:55–, the pre-quotations (golive dafc8f8, lead service f61ad6a)

Three scenarios written for the quotation flow with the default plan priced
during the ringing and its neighbours priced ahead (`crm/prequote.go`):
`sim-prequote-hit` (default → one change → one more → "say it again"),
`sim-prequote-interrupt` (default → change → the readout cut by a question →
the total asked again), `sim-prequote-miss` (default → a coverage dropped →
a tier changed → the difference). On the box under
`~/golive-sim/scenarios/custom/`.

## Run 1: sim-prequote-hit, out-mvouebz7-9qxa, 13:55 (dafc8f8 / f61ad6a)

- 7 turns, 5 opened; content p50/p95 590 from the close (970 from the last sound).
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

## Runs 2–4 on 5b1c42f (golive + lead service), 14:14–14:22

**sim-prequote-hit, out-mvov38lh-t62u, 14:14.** 6 turns, content from the
last sound p50 932 / p95 1100 (552 / 650 from the close). Default: 0 ms from the prefetch, no ack, read directly; the
memory's plan set to the 4 coverages on delivery. "三者险 → 300万": the
fold made the plan 4 coverages with the change; **cache hit, 0 ms, total
10524.8** (the neighbour priced at 13:55+6 s of the call). "再加驾乘险 50万":
two changes from the default, a miss, priced once in 4.0 s as the full
6-line plan (10758.8). The recap on request came from memory (no tool
call). 1 answer cut by the customer.

**sim-prequote-interrupt, out-mvov652k-2xdv, 14:16.** 5 turns, content from
the last sound p50 909 / p95 1030 (490 / 529 from the close). Default 0 ms direct; "→ 500万" **cache hit, 0 ms, 10884.8**.
The simulated customer did not manage to interrupt the readout (no
truncation), so the cut-readout memory note (`noteReadoutCut`) was not
exercised; a scripted barge-in (the simulator's `actions`) would be needed.

**sim-prequote-miss, out-mvovazev-mryd, 14:20.** Default 3.5 s prefetch,
direct. "座位险去掉": a drop — outside the 8 neighbours on the box's plan
(no `prequote.keep`, the cap falls on tiers and additions) — priced once,
3 coverages, 10235. "三者险改一百万" on top: 3 coverages, 9965 — the memory's
plan had lost 座位险 (the nil in the selection patch), so the second change
built on the right base. The assistant ended the call on the goodbye.

## What the round says

- The default is direct every time (0–1 ms, no ack) — the 12:42 fault is gone.
- A one-item change on the default is a cache hit at 0 ms; a two-item
  change or a drop is priced once (3.5–4 s) and kept.
- The working memory's plan now follows what was priced, including drops.
- **Open, the owner's call:** a hit still gets the ack "请您稍等" before the
  posthook and the figure (the owner's rule of 23:15 on 10-23, for the
  15 ms placeholder rules); with the cache it is the common case and adds
  ~3.6 s before a figure that is already there. The posthook "保费给您计算好了"
  would still announce it if the ack were skipped for a result inside the
  grace.
