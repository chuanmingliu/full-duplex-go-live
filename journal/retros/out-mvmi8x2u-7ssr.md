# Call out-mvmi8x2u-7ssr — 2026-10-24 22:39, build 4f20751

A real call, 3½ minutes, quiet line; the first on the default-quotation
flow (prefetch during ringing, told directly) and the second quotation
through the quotation service on the box. The 22:25 call before it
(out-mvmhqs1i-m6e3, build 8fdc884, 4 turns, p50 692 console-out) was the
dress rehearsal: prefetch 4.0 s during ringing, default delivered with a
2956 ms gap wait, the second quotation still asked for the plate and age
(`need carNum,vehicleAge`) — fixed in 4f20751 (lead vars reused) before
this call.

## The quotation

- **Default, told directly:** t1 at 22:40:01, `took 0` (prefetched), 5
  lines, no prehook ("finished within the ack's grace; nothing to
  announce"), no posthook (`default:true`), delivered after a 420 ms gap.
  ✓ as the owner specified.
- **Second quotation:** no field asked (lead vars reused) ✓; t2 submitted
  22:40:48, result 22:40:51 (quotation service, ~3 s), 5 lines, **驾乘险
  skipped** — `quote-api` v3 on the box had no code for it (nor 划痕/玻璃/
  医保外用药) and the service's rating table lacked 958. Fixed on the box
  23:12: rating.json +958, `quote-api` v4 with codes 955–958; probe with
  驾乘险 80万 prices it (360).
- **The prehook was dropped** (22:40:49 "the ack's moment passed; the
  caller or the assistant is speaking"): the model's own sentence was
  playing when the 400 ms grace ended. The model then improvised its own
  wait phrase ("这会儿正在出价"), which the caller cut. Fixed in a4bad35
  (the ack waits for the floor, retrying every 150 ms while the task runs)
  and 4d34bea (the model is told to say nothing about waiting).
- **The result interrupted** after a 1560 ms gap wait (the caller was
  speaking) — by design.
- **A figure repeated after the readout:** the delivery note said both
  "系统已念出报价，不要重复" and "请报总价和关键构成"; the model obeyed the
  second. Fixed in ff9f0f6 (the second line is dropped when the figures
  were read out; no number, amount or coverage name allowed in the reply).

## Latency

19 caller turns. Console out (turn judged over → first audio): **p50 532,
p90 756, p95 782, max 940**. Caller's last sound → first audio (out +
pause): **p50 912, p90 1136, p95 1162, max 1320**; 7 of 19 over 1000, all
of them pause-bound turns (380 ms pause + 650–940 out). The chat model
was quick (first token 169–536 from the request, median ~290); the three
over 1100 were long first clauses (out 940, 765, 754). Nothing pointed at
the vendor hour.

## Focus and hangup

20 judgements, 0 vetoes, 7 barge-ins judged, 4 kept, 0 quiet closes; one
primary speaker throughout. Hangup by the goodbye net (`said_goodbye`,
27 characters) after the caller wound up; the farewell played whole.

## Follow-ups

- Measure the fixed prehook on the next second quotation: expect
  "这会儿正在给您计算保费" at the first gap after the model's sentence,
  posthook + readout ~3 s later, then one model sentence without figures.
- 驾乘险 on the next second quotation: expect a priced line, no "暂时算不了".
