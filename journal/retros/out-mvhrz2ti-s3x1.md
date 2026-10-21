# Simulated call out-mvhrz2ti-s3x1 — 2026-10-21 15:12, build f18530c

Scenario `sim-quote-direct` (the customer states car, history and plan in
one breath and asks the total), receiver 5128a8f, 74 s. The third run of
the quotation loop, and the first that quoted on demand.

## The quotation, step by step (the journal's new section)

| s | step |
| ---: | --- |
| 7.2 | recorded ncd, vehicleAge, vehiclePrice |
| 9.5 | recorded the plan (quote), seats, and the three again |
| 15.7 | quote_total asked → **quote 4033.70** |
| 16.2 | asked again (same memory) → the same 4033.70 |
| 17.3, 17.8 | both results told the model |
| 60.0, 61.8 | recorded quote (三者险 → 300万) |
| 62.6 | asked → **quote 4271.20** |
| 63.1 | asked again → the same |
| 64.1, 64.7 | both told the model |

Run `completed`. No refusal, no re-asking of what the customer had said,
the second total above the first.

## The three runs

- `out-mvgu556z-lq1v` (23:25, 8760f62 minus the rules): **0 steps** — the
  owner's prompt says quotes come through the link; the model obeyed it.
  Fix: `instructions_append`, the engine's tool rules after the prompt.
- `out-mvguj2d9-nbbn` (23:36, 8760f62): recorded `car_price`,
  `vehicle_age`, `claim_count`; the intake asked for vehicleAge and ncd it
  had just been given; ended by inactivity. Fix: field aliases in the plan
  (v3) and the canonical names in the rules.
- this one: quoted, re-quoted. Left: **each ask made twice** within half a
  second on the same memory, and both results read to the model. Fixed in
  the commit after this: a repeated call of the same tool on the same
  memory within 8 s is folded into the previous task
  (`tools: a repeated call was folded`).

## What golive measured

8 turns, console p50 1198 — the afternoon; the figures of a quotation call
are not comparable (the answers are long and carry numbers).
