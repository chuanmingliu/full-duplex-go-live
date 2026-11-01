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
normal-response gate. Two candidate runs on the identical scenario followed after CI and warming.

Evidence: [controlled simulation](evidence/2026-11-01-interruption-prefeed/controlled-simulation.json),
[live baseline](evidence/2026-11-01-interruption-prefeed/live-baseline.json),
[live cut decisions](evidence/2026-11-01-interruption-prefeed/live-baseline-cuts.json).

## Live-provider candidate results

| Active Call simulation | Playback cuts: VAD-open → server cut | Median | Maximum |
| --- | --- | ---: | ---: |
| Baseline `45b5564` | 109 / 147 ms | 128 ms | 147 ms |
| Candidate A `f1fd9a6` | 353 / 157 / 126 ms | 157 ms | 353 ms |
| Candidate B `f1fd9a6` | 100 / 178 / 24 ms | 100 ms | 178 ms |

**No consistent live-provider improvement is established.** Combining candidate
cuts gives median 141.5 ms and maximum 353 ms, compared with baseline 128/147 ms.
These tiny, unequal populations cannot support a causal improvement or
regression claim. The scenario has two scripted cut-ins plus generated dialogue;
the utterance and answer populations differed even with identical configuration
and scenario hashes. Keep the slower run in the comparison.

The first two utterances in each candidate run had `prefed_ms=0`. Later adopted
prefixes had 540/660 ms in A and 620 ms in B (including pre-roll, not time saved).
The corresponding playback cuts took 126 and 24 ms; A also judged a later
prefed utterance immediately when no playback cut was needed. The optimization
is active, but does not cover every interruption. Distinguishing cooldown,
candidate rejection and speech that begins before playback needs additional
diagnostics before changing the conservative speculative-work limit.

- A: `out-mvxpmcug-bpqi`, `sim-fefb525a5a26bacab96d3709`; one normal-content
  response at 881 ms; quotation readout 925 ms; first-audio samples both 380 ms.
- B: `out-mvxpoc4i-ta7z`, `sim-fcaef5f52c949db3b634cdc2`; one normal-content
  response at 942 ms; quotation readout 792 ms; first-audio samples both 380 ms.
- Both: no receiver failures/warnings, no missing output records, all five
  archive files verified, stereo signal and transcript entries on both sides.
  Their minimum-20 normal-response gates fail for insufficient samples.
- Receiver first-audio gaps (35/200 ms) include possible overlapping output;
  they are not interruption-stop measurements or meaningful-content latency.
  Quotation readout is reported separately; its configured calculation delay
  was retained. No acoustic handset measurement was made.

See [candidate A](evidence/2026-11-01-interruption-prefeed/live-candidate-a.json),
[A cuts](evidence/2026-11-01-interruption-prefeed/live-candidate-a-cuts.json),
[candidate B](evidence/2026-11-01-interruption-prefeed/live-candidate-b.json), and
[B cuts](evidence/2026-11-01-interruption-prefeed/live-candidate-b-cuts.json).

## Validation and rollout boundary

Focused race tests cover exact once-only ASR audio, discarded candidates,
ordinary silent pre-dial, corrected buffered hypotheses, final/error retention,
blocked-forwarder cleanup, and existing background/backchannel rejection.
Both full [push CI](https://github.com/chuanmingliu/full-duplex-go-live/actions/runs/41298313932)
and [PR CI](https://github.com/chuanmingliu/full-duplex-go-live/actions/runs/41298317591)
passed normal tests, race tests and all quality checks before deployment.
The first local full race run had one timeout in
`TestConversationContinuesWhileAFunctionIsPending` without a data-race report.
Three targeted race repetitions passed on each of baseline and candidate;
the failure is retained as a validation limitation, not erased by those passes.
The second local full race run passed that continuation test but failed
`TestTheSilenceRuleDoesNotCountTheAssistantsOwnAnswer`: hangup was observed
about 863 ms after the test considered the answer finished, with a two-second
configured silence rule. Three focused repetitions of that test then passed
on each of baseline and candidate. Neither full run reported a data race.
**Both local full runs failed; their timing failures remain unresolved.**
Passing targeted reruns and both complete Linux CI runs do not erase them.
See the [validation record](evidence/2026-11-01-interruption-prefeed/validation.json).

Only the idle phone engine was updated to **f1fd9a6**, using a clean export of
that commit. Its existing supervisor, exact arguments/environment, profile,
other services and Aria route were retained. Both cache warm-ups completed in
one stable process. All **847** existing archive files retained their hashes;
365-day working retention and permanent archives remained enabled. See the
[deployment receipt](evidence/2026-11-01-interruption-prefeed/deployment.json),
[build provenance](evidence/2026-11-01-interruption-prefeed/build.json), and
[CI receipt](evidence/2026-11-01-interruption-prefeed/ci.json).

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
or container restart was performed for that binding check. The later phone-only
rollout changes the engine behind the same endpoint to `f1fd9a6`, with `45b5564`
retained as the requested restore version. An unauthenticated public API probe returned
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
