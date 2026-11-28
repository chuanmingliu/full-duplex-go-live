# Active Call backchannel ASR assist

The original controller owns interruptions, acknowledgments, echo/background
rejection, end-of-turn decisions and hangup. The optional Pipecat audio model
can ask the **existing streaming ASR final/continuation path** to run sooner in
a short pause. It cannot directly stop playback, suppress a caller turn or
extend a floor hold. This avoids acting directly on the false floor signals
found in the Chinese matrix.

## Configuration

Both fields default to empty (off); the shipped telephony profile stays unchanged.
A separately enabled deployment may add:

```json
{
  "duplex": {
    "backchannel_detector_url": "ws://127.0.0.1:18087/v1/stream",
    "backchannel_detector_mode": "asr_assist"
  }
}
```

`shadow` connects and consumes predictions without changing ASR scheduling.
`asr_assist` enables the bounded policy below. An empty mode disables the client.
Equivalent environment overrides are `GOLIVE_BACKCHANNEL_DETECTOR_URL` and
`GOLIVE_BACKCHANNEL_DETECTOR_MODE`. Only a numeric loopback WebSocket endpoint
with path `/v1/stream`, no credentials or query is accepted. A restart to change
configuration requires an idle phone service.

Preserve the complete original profile and running binary in the release
snapshot. A rollback restores those exact files and restarts only the idle
phone process under its existing supervisor; retain all recordings and the
owner's earlier Aria restore anchor. The regular Active Call transport, original
prompt, greeting, voice, provider choices and quotation delay remain the same.

## Streaming and fallback

Each engine session starts its own asynchronous client. It takes the existing
16 kHz pipeline PCM and frames it into 40 ms chunks. Bot-state flags come from
actual Player speaking and backchannel activity, not queued TTS. Bot onset,
pause and stop ages use that input-frame clock. This is sampled player state,
not acoustic echo cancellation; the connection declares `aec: false` and
`language: zh`.

The queue holds at most 32 chunks (40 KiB of PCM) to absorb batched input.
This does not extend the 120 ms queued-work freshness deadline. Input gaps, input-queue drops, muting after audio begins, timeout,
invalid responses and overload disable the optional helper for that session,
clear its evidence, and retain the legacy controller. Active Call’s initial
pre-answer mute does not invalidate a stream that has consumed no audio. No automatic reconnect
or network wait occurs on the engine audio loop. Connect is bounded to one
second; each model exchange has a 120 ms read/write deadline and queued work
older than 120 ms is discarded by disabling the stream. Close cancels the
connection. The service's `control_allowed: false` remains true to its contract:
it returns observations, and the engine alone owns the separate assist policy.

Predictions are generation-tagged. A generation transition inside a chunk
invalidates that chunk's actionable streak without dropping audio from the
model. Updates coalesce in a capacity-one notification; old observations are
never queued as engine actions.

## Exact assist policy

The helper may shorten **ASR's early-final request pause** from the configured
120 ms to 40 ms only when all of these hold:

- Original early-final/speculation is enabled, the utterance is open, and the
  existing recognizer has enough voiced audio for its normal early-final rule.
- An actual barge-in floor hold is pending, it has not been attributed to a
  background speaker, and no protected farewell is active.
- At least three consecutive model frames have `p_floor >= 0.7`, all from the
  current utterance and playback generation. Evidence is at most 150 ms old
  and at most 120 ms behind the input timeline.
- The speaker tracker has a known primary, at least 60% coverage, and satisfies
  the existing `callerSpeaksOver` rule. Missing, untrusted or observe-only
  speaker evidence does not enable this shortcut.
- The current ASR text is not a known/potential acknowledgment or hesitation.
  An empty ASR partial can qualify only with the positive speaker evidence above.
- No earlier assist has been used for this utterance.

There is still a real 40 ms pause; continuous speech does not force a final.
The normal continuation recognizer starts and subsequent words remain part of
the utterance. Its final transcript goes through the unchanged `judgeHold`
rules. The model never calls `releaseHold` or `interrupt`. Normal responses
without a pending barge-in hold and quotation calculation are unaffected.

The original 120 ms rule runs whenever any assist condition fails. This means
there may be no improvement when speaker evidence is late or the current text
still resembles an acknowledgment. Actual benefit must be measured on calls;
the earlier raw-signal lead in replay is not a promised saving.

## Tests and observability

The controlled same-engine simulation measured the final request at 120 ms in
shadow mode and 40 ms with eligible assist. An acknowledgment partial retained
120 ms despite a positive model score. This proves an 80 ms scheduling change,
not handset latency or provider response speed.

Tests cover WebSocket framing, state isolation, generation transitions, gaps,
overload, timeouts, freshness, acknowledgment/prefix/hesitation preservation,
missing/background speaker evidence, and the existing speaker rejection suite.
`TestDeployedModelStream` is an explicit opt-in smoke test against the real
service using `GOLIVE_TEST_BACKCHANNEL_URL`.

Per-session private logs contain metadata-only entries:

- `backchannel: policy enabled` and `backchannel: connected`;
- `backchannel: floor evidence`, once per eligible utterance, without ASR text;
- `backchannel: earlier ASR final requested`, with utterance and pause timing;
- `backchannel: legacy fallback`, with a bounded reason string;
- `backchannel: stream closed`, with processed chunk count.

Existing Call Journal storage still records the conversation, both audio
directions, original ASR/turn/cut timings and the session log. Review after the
owner says **done**, separating normal response, interruption and quotation
calculation. Do not infer an assisted turn from service health alone: it needs
the explicit earlier-final log followed by the original decision evidence.

The [Active Call rollout record](../telephony/2026-11-02-backchannel-integration.md)
records both initial integration failures and the final verified simulator.
