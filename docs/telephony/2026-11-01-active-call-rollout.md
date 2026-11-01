# Active Call rollout and simulator comparison — 2026-11-01

Shared improvements from the SIP exploration are deployed as `45b5564` on the
original GoLive Active Call path. `codex/telephony-latency` is the optimization
branch. SIP/RTP and Speko deployments were left intact and are not the default
route. The target remains **strictly below 1200 ms**; it is not yet verified.

## What ran

Full local race tests and GitHub push/PR CI passed before deployment:
[push CI](https://github.com/chuanmingliu/full-duplex-go-live/actions/runs/41287492177),
[PR CI](https://github.com/chuanmingliu/full-duplex-go-live/actions/runs/41287495355).
Engine, CRM and Journal were updated using the existing Mac runner after idle
checks. Phone `8765` and demo `9080` both report `45b5564`. Running executable
hashes match the installed files. Original profiles, saved prompt, voice,
greeting, recipient, quotation calculation delay and 8760-hour working-store
retention were preserved. Active Call/PBX containers and the separate SIP
engines, Desk, edge and quotation service were preserved.

Rollback files remain at
`/home/chuanming/golive-releases/codex-45b5564-20261101-162734/previous`.
The successful receipt is
`codex-latency/reconciled-activecall-45b5564-20261101.json` on the box.
Exact operation bodies and hashes are in the
[operation record](../../scripts/local-ops/recorded/2026-11-01-active-call/README.md).

The initial rollout's final archive guard rejected new derived latency fields
in `call.json`. Automatic rollback restored old files on disk but stopped before
restarting the healthy new processes. Reconciliation verified that removing
only `latency.conversational_first_content` and `latency.quote_only_responses`
reproduced all **143 original summary hashes**. All **572 raw archive files**
were unchanged. Candidate files were restored to match the running processes,
without another restart. The failed receipt remains historical evidence; the
successful reconciliation receipt describes the current state.

## Original-config coverage simulations

Both runs used `kb-coverage-nerd` with the same scenario hash and saved context.
They are generated conversations, not identical audio replays.

| Metric | Baseline `9547d22` | Candidate `45b5564` |
| --- | ---: | ---: |
| Normal response samples | 33 | 31 |
| First audio p95, including opener | 380 ms | 380 ms |
| Normal content p50 | 945 ms | 933 ms |
| Normal content p95 | 1157 ms | 1200 ms |
| Normal content maximum | 1279 ms | 1300 ms |
| Strict server gate | Pass | Fail: equality is not below target |
| Content p95 plus assumed 50 ms | 1207 ms | 1250 ms |
| Missing / unverified / duplicate output records | 0 / 0 / 0 | 0 / 0 / 0 |
| Simulator TTS chunk failures | 2 | 1 |

Baseline: `out-mvxk9u0n-11lw`, 289.356 seconds, 33 caller / 34 assistant lines.
Candidate: `out-mvxkn1v4-jeyp`, 243.019 seconds, 33 caller / 33 assistant lines.
Each has five verified permanent archive files and signal on both stereo
channels. Signal is not proof of intelligibility. Both receiver reports flag
TTS warnings, so neither is a clean conversational-quality pass. These runs
do not establish a causal improvement or regression. No quotation result was
measured in either coverage run; configured calculation time is unchanged.

The two longest candidate normal turns had content latency of 1300/1200 ms,
including 380 ms endpoint silence and 678/424 ms to the first model token.
The next investigation is model-to-content generation on this existing path.
Zero segment/TTS stamps can belong to a cached opener, so they do not prove
zero synthesis latency for the answer. See the
[normal-content tail](evidence/2026-11-01-active-call-rollout/normal-content-tail.json).

## Interruptions

For coverage runs, two paired server VAD-open to active-playback cut decisions
were 229/371 ms before and 185/146 ms after. The sample count is too small and
utterances too different to attribute a speedup. VAD-open itself followed
320 ms of active input. These intervals exclude pre-detection voice, network
delay and the time until the handset actually stops playing.

The separate `set-a-01-barge-in` call `out-mvxksnvy-4i3q` completed both scripted
cut-ins with no simulator warnings or failures. Server cut decisions were
154/169 ms after VAD-open. The call lasted 16.631 seconds and has verified chat,
stereo audio and all five archive files. It contains no qualifying normal
content samples; one quotation-linked readout measured 844 ms from its linked
caller endpoint. That is not an independent measurement of calculation duration.
The minimum-sample gate fails and no handset latency claim follows.

The initial collector rejected this call because Journal's `Greeting` field
was overwritten by a later system opening. Canonical logs prove the first
41-character opening matched the saved greeting; the later event had 12
characters. A follow-up Journal-only fix preserves the first opening while
keeping all system openings outside the response SLA (ISS-100). This follow-up
has focused race coverage; it is not part of deployed `45b5564`.

## Next measurements

Optimize normal content and interruption tails on Active Call. Preserve
quotation calculation tolerance and report its completion separately. Do not
count filler speed as content speed or simulator application audio boundaries
as handset evidence. Actual handset qualification still requires a clean
acoustic capture and at least 20 valid normal response samples.

Numeric evidence: [baseline](evidence/2026-11-01-active-call-rollout/baseline.json),
[candidate](evidence/2026-11-01-active-call-rollout/candidate.json),
[scripted interruptions](evidence/2026-11-01-active-call-rollout/scripted-interruptions.json),
[deployment](evidence/2026-11-01-active-call-rollout/deployment.json).
