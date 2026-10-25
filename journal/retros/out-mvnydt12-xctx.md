# Calls out-mvny3iy8-wzva (22:52) and out-mvnydt12-xctx (23:00) — 2026-10-25, simulated

Two more conversations through the PBX with the simulated customer, to put
the p95 on more than one or two turns.

| call | build | scenario | turns | first audio p50 / p95 / max | the answer's own first word p50 / p90 / p95 / max |
| --- | --- | --- | ---: | --- | --- |
| out-mvnxt123-m1tm 22:42 | 1f853df | kb-coverage-nerd | 32 | 380 / 380 / 444 | 887 / 990 / 990 / 1054 |
| out-mvny3iy8-wzva 22:52 | 1f853df | kb-claims-skeptic | 38 | 380 / 380 / 451 | 841 / 990 / 990 / 1069 |
| out-mvnydt12-xctx 23:00 | 8166f1b | kb-process-struggler | 35 | 380 / 380 / 383 | 890 / 967 / 992 / 1013 |

All from the caller's last sound; the evening hour; the owner's kept prompt.

On the 22:52 call 12 of 38 content figures were exactly 380 + the 明白，
opener's length (610 ms): the longest opener, not the model, decided when
the answer began on those turns. 8166f1b keeps two openers (好的，/嗯，, 440
and 510 ms trimmed); on the 23:00 call the content figure follows the model
(890 p50) and the openers are off its path.

Pooled over the three calls, 105 answered turns on the phone path: first
audio p95 380, the answer's own first word p95 ~990, 5 turns over 1000, 0
over 1200. 19–20 barge-ins a call, all the simulated caller's, yielded; 0
quiet closes; the model's own 好的 dropped as a duplicate 7 times on the
first call. What is missing is a person on the line.
