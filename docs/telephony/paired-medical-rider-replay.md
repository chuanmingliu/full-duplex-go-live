# Split-turn driver and passenger medical riders

In the November 28 call, the model acknowledged a change to 20万 after a 2万 quote. Neither a new revision nor a pricing task appeared. The failure occurred before queueing, so the baseline has no calculation or delivery time for the requested quote.

Replay a base quote, then a 400万 third-party plan with shared third-party medical coverage and both occupant medical riders at 2万. Name the two riders, select 20万 in the next turn, and explicitly confirm. Check for one registration covering both riders, one confirmed pricing task, and a returned readout whose other lines match the prior selection.

Use the existing confirmation and pricing paths. The change binds an amount-only follow-up to the nearby named pair, validates each limit against the selected product catalog and uses the existing revision ID, plan version, and memory freshness checks. A changed topic, question, comparison or negated selection must not infer a paired change. Naming both riders alone must not price an assumed amount. Pricing starts after confirmation. Existing quote supersession, output interruption and cache rules apply.

The [simulator scenario](../../testdata/call-sim/second-quotation/sim-paired-medical-20w.json) uses separate caller turns for the coverage names, amount and confirmation.

## Live rollout

The live phone engine deployed build `b09f473-render1-memory1-bc1-rider1` on November 28, 2026. Startup and warm-up checks passed. The quote and timings below come from the simulator. The deployment check did not place a handset call. The previous engine is available on the deployment host at `~/golive-releases/rider-live-1128/rollback`.

## Simulator result

On the final simulator build, call `out-mx0en07b-i32n` reached the original ¥11,448.20 selection. ASR finalized “司机、乘客、医保外” at 122.081 s and “配个二十万” at 134.762 s. GoLive registered two coverages as revision 4 at 135.494 s. The caller answered “没有了” to the question about further changes. GoLive confirmed revision 4 at 143.776 s and submitted one pricing task, t3, at 143.777 s. The pricing service returned the nine-line quote at 147.803 s. GoLive read the total aloud and the simulated caller repeated it. Five archive file hashes matched.

| Coverage | Selection | Premium (CNY) |
| --- | --- | ---: |
| 三者险 | 400万 | 1,710.00 |
| 医保外用药责任险-三者 | 共享保额 | 720.00 |
| 医保外用药责任险-司机 | 20万 | 36.00 |
| 医保外用药责任险-乘客 | 20万 | 36.00 |
| 司机责任险 | 2万 | 16.20 |
| 乘客责任险 | 2万 | 64.80 |
| 车损险 | 70万 | 7,560.00 |
| 交强险 | Included | 950.00 |
| 车船税 | 代缴 | 420.00 |
| **Total** | | **11,513.00** |

Both medical rider premiums increased from ¥3.60 to ¥36.00. Comparing the pricing service's selection objects showed exactly those two changes. The increase in total was ¥64.80.

The first exploratory build also raised both parent liability limits and returned ¥12,242. That run exposed ASR pauses in the coverage label, a deictic amount fragment, and the assistant recapping parent policies between turns. One intermediate simulation ended during scripted speech preparation. A separate successful run of the final build (`out-mx0ej1jl-eo42`) registered the two riders and confirmed on “你直接报就行了”. Its prior quote omitted the shared third-party medical rider, so its verified total was ¥10,793. The matched run above includes that rider.
