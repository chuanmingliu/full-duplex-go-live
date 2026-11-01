# Recorded non-calculation interaction test

Historical simulator-only operation. Do not replay consumed attempt or
idempotency keys. `run.sh` binds the reviewed payloads in `manifest.json`.
The five-minute customer scenario asks one original-plan quote, follows with
ordinary questions, and interrupts an explanation once. Agent settings stay
unchanged. The intended slow-calculation overlap is not established: this
quote was already prepared. Interruption action completion is established;
per-interruption acoustic stop latency is not.

Runner exits 0 at 15:06:12 on 2026-11-01. The server conversation gate passes
at 1087 ms p95 over 25 responses; receiver first-audio p95 is 1193 ms over 20.
`verification.json` records the strict gate and archive checks;
`interaction-evidence.json` retains numeric stages, receiver gaps and action
outcomes. No physical handset measurement is claimed.

Files named `*-rejected.*` preserve the first rejected pre-dial operation.
It exceeded simulator duration/action limits; temporary processes were verified
absent before the distinct corrected attempt. No simulator limits were changed.

See [the full retrospective](../../../../journal/retros/sim-interaction-ab988aa8-b778-4a5d-aab2-b51464cc2768.md).
