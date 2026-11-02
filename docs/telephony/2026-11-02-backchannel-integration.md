# Backchannel ASR assist in Active Call

The Pipecat service is connected as optional evidence for earlier ASR finals.
It does not own playback interruption or suppress caller speech. The existing
speaker, echo, acknowledgment, continuation and hangup logic remains in charge.
See the [implementation and exact gates](../services/backchannel-integration.md).

The deterministic engine test requests a final after a 40 ms pause when all
assist gates pass, versus the original 120 ms. A known acknowledgment retains
120 ms. This is an 80 ms scheduling opportunity, not a measured handset saving.
The Chinese replay matrix contains false floor signals, so direct model-owned
cuts remain inappropriate for this trial.

## Initial rollout and discovered integration issue

Commit `60dbe53` passed full normal/race CI and was deployed through the Mac
runner after checking idle state. The original executable and profile were
saved; only the detector URL and `asr_assist` mode were added to the profile.
The deployment verified warming, 877 unchanged archive files, preserved other
services and unchanged Aria routing. The older Aria restore anchor was retained.

Simulator `out-mvzeis6d-5vzp` completed, with both directions archived, but the
new detector processed **zero** chunks. Active Call's normal pre-answer input
mute had disabled the client immediately. This run is an integration failure,
not evidence of benefit. It had no qualifying normal-content responses; its
one 872 ms quotation readout is a separate metric, not normal response latency.

Commit `7a466e3` distinguishes an initial mute before any audio is consumed from
a later mute that breaks model history. The former retains the fresh client;
the latter still disables and clears it for the remainder of the session.
A regression test exercises repeated pre-answer mute, ignored audio while
muted, the first post-answer prediction, and permanent fallback on a later mute.

The original prompt, greeting, voice, preset openers, provider settings and
quotation calculation delay are retained. Server timestamps, the optional
owner's 50 ms estimate and actual acoustic handset evidence remain distinct.

Evidence: [initial deployment](evidence/2026-11-02-backchannel-integration/deployment.json),
[initial CI](evidence/2026-11-02-backchannel-integration/ci.json),
[failed integration simulator](evidence/2026-11-02-backchannel-integration/initial-simulator-fallback.json).

## Second simulator and bounded burst handling

The `7a466e3` correction passed full CI and was deployed with an unchanged
profile, preserved services and 882 verified archive files. Simulator
`out-mvzf4ctp-jqjp` then processed 30 model chunks before `queue_overflow`.
It is also retained as an integration failure. Its normal-content population
was zero; the single 792 ms quotation readout remains a separate metric.

Commit `b51e959` increases the optional client's bounded queue from 4 to 32
40 ms chunks (40 KiB of PCM), allowing batched WebSocket input to be processed
without immediately losing the model's audio history. Queued work still
expires at 120 ms; input-time, generation, utterance and 150 ms observation-age
checks remain unchanged. The extra capacity is not permission to act on a
backlog. Sustained overload and timeouts continue to select the legacy path.

Race tests cover a 320 ms input burst, expired queued work, overload and all
existing assist guards. A separate test against the actual ThinkPad detector
processed 20 sequential chunks followed by one 640 ms batch in 80 ms total. This
proves bounded model catch-up, not a phone-call latency improvement.

## Verified integration and live-test readiness

`b51e959` passed the full normal/race and quality CI jobs. Deployment preserved
the enabled profile byte-for-byte, other services, Aria routing and 887 archive
files. All three release snapshots remain available; the original unmodified
profile is in `~/golive-releases/backchannel-60dbe53/profile.previous.json`.

Simulator `out-mvzfoloq-k0yg` / `sim-2457b19d85d8a9055361b9f3` completed:

| Measurement | Result |
| --- | --- |
| Detector chunks processed | 341 |
| Detector fallbacks | 0 |
| Eligible model observations logged | 3 |
| Earlier ASR final requests | 0 |
| Receiver failures / warnings | 0 / 0 |
| Verified archive files / audio directions | 5 / 2 |
| Normal-content responses | 2; median 832 ms, maximum 897 ms |
| VAD-open → server playback-cut decisions | 309 / 234 / 182 ms |
| Quotation readout, reported separately | 1; 813 ms |

The model stream is now connected to a complete Active Call session, but the
other guards did not permit an earlier ASR request in this short scenario.
There is **no demonstrated latency gain** here. Earlier `f1fd9a6` runs of the
same scenario had cut decisions of 353/157/126 and 100/178/24 ms; these tiny,
unequal live-provider populations are not a controlled comparison. The new
run has only two normal-content samples and fails the minimum-20 latency
gate. Its 897 ms maximum is a server-output measurement, not handset proof.
Quotation calculation settings were unchanged.

The owner-requested real call uses Active Call, the original saved settings
and the same guarded assist mode. Analysis waits for the owner's **done**.

Evidence: [final CI](evidence/2026-11-02-backchannel-integration/burst-ci.json),
[final deployment](evidence/2026-11-02-backchannel-integration/burst-deployment.json),
[successful simulator](evidence/2026-11-02-backchannel-integration/successful-simulator.json).
