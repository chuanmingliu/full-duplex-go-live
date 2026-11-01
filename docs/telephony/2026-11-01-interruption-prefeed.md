# Earlier recognition of Active Call interruptions

The real call `out-mvxo348d-w5pb` had 14 server playback cuts. VAD-open to
cut p95 was 686 ms, in addition to 320 ms of accumulated voiced detection input.
Some early partial transcripts could still become configured backchannels,
so the engine correctly waited for more words. The existing ASR pre-dial
completed the connection early but sent no audio until VAD confirmed speech.

This change sends the exact candidate audio prefix to that same recognizer
while playback is active and the detector is still accumulating evidence.
It uses the existing `asr_predial` setting. Recognition results remain private
until VAD confirms an utterance. A superseded buffered hypothesis is replaced
by the latest available correction, retaining final/error boundaries.

The opening snapshot skips exactly the prefix already sent. Rejected candidates
are closed and cannot be adopted by a later utterance. A five-second cooldown
limits repeated audio prefeeding after false alarms, while ordinary handshake
pre-dial remains available. An unadopted stream cannot remain blocked forever
on a full result buffer after close. Finished streams are not reused.

The phone profile, providers, voice, prompt, greeting, backchannel phrases,
VAD thresholds, caller-focus thresholds, echo masks and quotation delays are
unchanged. Ordinary non-playback pre-dial remains silent. More candidate audio
can reach the existing ASR provider before confirmation; no caller turn,
tool action or speech cancellation is permitted from it before confirmation.
Provider-side work for rejected candidates is a bounded additional cost.

## Simulator comparison

The same paced fixture and original phone profile ran against baseline
`eb8a48c` and this candidate, three repetitions each. A scheduled mock ASR
returns a decisive partial 180 ms after receiving 80 ms of voiced audio.
Speaker labels and audio are controlled; this is not a live-provider benchmark.

| Controlled outcome | Baseline | Candidate |
| --- | ---: | ---: |
| Caller onset → server cut, median | 486 ms | 310 ms |
| Echo cuts, three runs | 0 | 0 |
| Background-voice cuts, three runs | 0 | 0 |
| Backchannel cuts, three runs | 0 | 0 |

The median reduction is **176 ms** in this fixture. It is conditional on the
recognizer producing useful evidence during VAD's existing confirmation window.
It does not establish that a handset or the live ASR provider saves 176 ms.
The fixture holds an answer in flight and observes the engine's cut decision;
it does not measure an audio device stopping.

The live-provider Active Call baseline simulator completed separately:
call `out-mvxor222-6g7h`, run `sim-3e8e49c8dff7644ecc9020e7`, engine `45b5564`,
scenario `set-a-01-barge-in`. Its two playback cuts were **109 / 147 ms** after
VAD-open. No receiver warnings or failures; all five archive files verified.
It has no qualifying normal-response population and cannot pass the minimum-20
normal-response gate. A matching live candidate comparison is still required.

Evidence: [controlled simulation](evidence/2026-11-01-interruption-prefeed/controlled-simulation.json),
[live baseline](evidence/2026-11-01-interruption-prefeed/live-baseline.json),
[live cut decisions](evidence/2026-11-01-interruption-prefeed/live-baseline-cuts.json).

## Validation and rollout boundary

Focused race tests cover exact once-only ASR audio, discarded candidates,
ordinary silent pre-dial, corrected buffered hypotheses, final/error retention,
blocked-forwarder cleanup, and existing background/backchannel rejection.
The complete race suite and automatic CI must finish before any runtime rollout.
The running phone engine remains `45b5564` at the time of this source change.

Reproduce the controlled comparison with:

```sh
GOLIVE_INTERRUPT_SIM=1 go test ./internal/duplex \
  -run '^TestInterruptionASRDeliverySimulation$' -count=3 -v
```

For the baseline, export `eb8a48c` with `git archive` and copy only
`internal/duplex/asr_prefeed_sim_test.go` into that tree before the same command.
No real telephone number is dialed by these fixtures.

## Aria binding and requested rollback

Aria's selected phone backend was already `local`, revision **13**, pointing to
`ws://127.0.0.1:8765/v1/realtime`. Loopback API inspection verified that this is
the original GoLive Active Call engine **45b5564**. No redundant routing update
or container restart was performed. An unauthenticated public API probe returned
403; this is not a claim of public browser access verification.

The Mac runner saved the actual running executable, profile and private launch
material under `/home/chuanming/golive-telephony/codex-latency/aria-binding-20261101/`.
Only the [sanitized receipt](evidence/2026-11-01-interruption-prefeed/aria-binding.json)
is committed. The restore engine SHA-256 is
`0af8a624053a706bc4f2f20ec2b9f847409eef2e6f502e21520cca60a8ec8a29`.

Restore that version only when the owner requests it. Inspect current calls,
routing and later configuration edits first; restore through the Mac runner
only when idle, retain the replacement for undo, and verify recording, health,
Aria binding and archives. Do not replay a historical dial or overwrite later
owner settings as part of rollback.
