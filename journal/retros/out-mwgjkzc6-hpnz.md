# Follow-up comparison bypassed quotation confirmation

Call `out-mwgjkzc6-hpnz` used Active Call and the original GoLive settings on
`c17f23a-quote-confirm`, from 23:09:55 to 23:12:04 China time on 2026-11-14.
The owner reported that the expected natural confirmation question was absent.

## Evidence and finding

The engine loaded quote-speech version 4. The call contained a first quotation
and a subsequent three-tier comparison. It logged no pending revision, approval
or cancellation. The comparison path was explicitly exempted from confirmation
in this build. Later discussion of an unspecified vehicle-damage adjustment did
not submit another quotation. Consequently, this call verifies neither the
confirmation exchange nor variation in confirmation acknowledgments.

The comparison utterance omitted its coverage name, relying on the prior turn.
Its skill preparation returned `no_match`. The coverage-option question before
it loaded guidance. Routing needs bounded nearby-topic context for that kind of
follow-up. The correction also places the confirmation procedure and varied
wording examples in the configurable quotation skill.

Both foreground tasks finished rapidly, in approximately 3 ms and 6 ms.
CRM logs identify the first result and the three comparison alternatives as
cache hits. The comparison took approximately 4 ms inside CRM. Both scheduled
waiting prehooks were suppressed because results arrived during their grace
period. This explains the absence of repeated calculation-waiting phrases in
this call independently of the confirmation bypass.

## Recording and latency

The plugin verified five archive hashes, session closure, 11 caller transcript
entries and 11 assistant entries. The 128.960-second stereo recording contains
signal in both audio directions. No acoustic handset capture was verified.
Analyzer SHA-256 was
`c5df3bf2a120d4bcc241f75a1e2c28b3205a903dd94b33516fa21e11ea549a50`.

| Server measurement | Samples | Median | p95 / maximum |
| --- | --- | --- | --- |
| Normal first content | 7 | 977 ms | 1,067 ms |
| First audio including openers | 7 | 964 ms | 1,067 ms |
| VAD utterance open to playback cut | 5 | 434 ms | 591 ms |
| Quotation result completion | 2 | 931 ms | 1,133 ms |
| Quotation readout output | 2 | 1,370 ms | 2,011 ms |

Interruption samples were 591, 274, 434, 145 and 463 ms, with zero unpaired cuts,
zero detector assists and zero detector fallbacks. Echo/speaker checks remained
active. These timings exclude pre-VAD detection and downstream audio playback.

The previous call had 18 normal samples at 1,027 ms median / 4,977 ms p95 and
14 interruption samples at 456 / 823 ms. The current smaller sample has lower
observed tails, but different conversations cannot establish a causal gain.
Seven normal samples are insufficient for the required 20-turn acceptance gate.

## Calculation time kept separate

The initial background prefetch took about 4,024 ms. Nine background pricing
operations, including the default and neighboring combinations, took roughly
3,002–4,513 ms each through the pricing service. The foreground requests reused
completed results. Those background spans include service orchestration and
polling; they are not isolated calculator CPU time or conversational latency.
The configured calculation delay remains the baseline.

## Correction

Extend pending-request confirmation to follow-up comparisons while preserving
all alternatives and the selected policy. Ask about concrete supported choices
when an adjustment is unspecified. Keep the procedure and wording examples in
`motor-quotation`, with host enforcement and fallback. Validation includes the
comparison bypass regression, stale IDs, shared changes, cancellation, skill
routing and real-model synthetic conversation. The new behavior still needs a
subsequent authorized real-call test.

## Synthetic verification of the correction

A 17-turn real-model replay passed using the actual quotation skill and v1
prompt rendering. It covered four baseline requests and thirteen candidate
turns, including extra changes, clarification, cancellation and confirmed
three-tier comparison. No candidate pricing request preceded approval.
The initial confirmation questions started mock audio in 762–1,868 ms; approved
calculation acknowledgments took 1,408–1,906 ms. These synthetic timings remain
above the latency target in several cases. Mock calculation remained three
seconds. Routing and interrupted-playback guards passed separate deterministic
regressions. The original live profile and runtime remained unchanged during
simulation.
