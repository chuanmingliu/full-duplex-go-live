# Inbound simulated user: design specification

Date: 2026-10-13 (Asia/Shanghai)
Status: planning only; no implementation or deployment authorized by this document.

## Intended result

Claude's deployed voice assistant originates a call. A Codex-built service answers as a simulated human customer. Each participant receives the other participant's audio over the telephone connection and generates its own speech. Codex and Claude configure and maintain the runtimes outside the audio loop.

## Fixed requirements

- All implementation uses a new branch and isolated checkout of chuanmingliu/full-duplex-go-live.
- Existing services, binaries, configurations, routes, working trees, recordings, and credentials remain untouched.
- Use a separate deployment host by default; separate ports on a shared host do not establish non-interference.
- The simulated user receives calls; this service has no outbound dial endpoint.
- The live conversation crosses an audio connection; never inject the other agent's internal text, prompt, hidden state, or tool results.
- One admitted call at a time in the first release.
- Audio capture and transmission remain concurrent, including during scripted speech.
- Unknown, mismatched, expired, or already-bound call identifiers cannot open another session.
- No deployment, provider calls, carrier calls, or changes to existing routing occur during planning.

## Source baseline and observed facts

Target repository: https://github.com/chuanmingliu/full-duplex-go-live
Starting branch: telephony-realtime
Reviewed snapshot: 66262931f0ad258d7d95c9acad1c3e996130a7e1
Proposed implementation branch: codex/inbound-user-simulator

This is different from the current local openai-gpt-live repository. Do not branch the latter or carry its uncommitted edits into this work.

The target module is github.com/chuanmingliu/golive; go.mod declares Go 1.24.7 and gorilla/websocket v1.5.3. Reuse its JSON configuration and provider adapters; no new framework or orchestration dependency is required.

Relevant verified source:
- [README](https://github.com/chuanmingliu/full-duplex-go-live/blob/66262931f0ad258d7d95c9acad1c3e996130a7e1/README.md): audio formats, /v1/realtime, native /v1/live/sessions, sidebands.
- [TELEPHONY.md](https://github.com/chuanmingliu/full-duplex-go-live/blob/66262931f0ad258d7d95c9acad1c3e996130a7e1/TELEPHONY.md): golive sits behind Active Call/RustPBX.
- internal/server/server.go: NewServer and Handler composition.
- internal/server/realtime.go: per-call association and opening handling; existing /v1/opening can fall back to all sessions when the supplied call has no match.
- internal/server/session.go: session-local engine configuration.
- internal/duplex/engine.go: Say builds a committed speech turn without requiring quiet input.
- internal/duplex/think.go: AppendCommentary/drainDeferred normally wait for quiet input.
- internal/duplex/listen.go: onFinalTranscript can begin a new generation and retire active speech.
- cmd/golivebench/main.go and BENCHMARK.md: useful audio-boundary timing approach, not physical handset proof.

Active Call's documented incoming flow is webhook notification with dialogId, connection using that dialogId, then Accept. Pin a tested Active Call build and capture its exact wire messages in fixtures before relying on it. Documentation is not evidence of the installed version.
https://miuda.ai/docs/active-call/guide/overview

Earlier read-only checks reached the ThinkPad at 192.168.71.9. Its Active Call and golive endpoints reported zero active calls at that instant. That does not establish hardware capacity. Gateway model, inbound routing, SIM registration, spare simultaneous voice channels, and independent provider quota remain unknown. Further hardware inspection is deferred until the user has access.

## Architecture

Claude assistant <-> its outbound telephone leg <-> SIP/carrier network
  <-> dedicated receiving telephone leg <-> dedicated Active Call
  <-> golive-simulator

The new golive-simulator binary composes:
1. An authenticated controller for preparing and observing a run.
2. An incoming-call adapter for the dedicated Active Call instance.
3. The existing golive speech engine with an explicit simulator-only control surface.
4. A deterministic behavior scheduler and a bounded observer/report writer.

A dedicated RustPBX is optional: use it only when registration/routing requires it. Do not modify the existing PBX to make the new deployment convenient.

For development, an independent SIP caller and prerecorded/scripted media drive the receiver in an isolated network. Label this SIP testing. For final acceptance, use an actual inbound carrier number or a supported dedicated SIM/voice gateway. A local SIP call does not establish mobile-network behavior.

## Isolation and deployment policy

Use Compose project golive-user-simulator, separate image tags, config files, credentials, network, and data volume. Publish controller/metrics only on loopback or the dedicated private management interface. Keep realtime traffic inside the isolated network. Exact SIP/RTP host bindings are selected after the dedicated host's network and NAT capabilities are known; deployment validation refuses occupied ports and never frees them by stopping another service.

Recommended controller binding on the new host: 127.0.0.1:18890. Internal speech listener: 18891. These are proposals, not reservations. Carrier/SIP exposure needs a verified RTP range and advertised address; do not infer them from old configuration.

Resource and provider isolation are part of non-interference:
- Dedicated host or separately reserved resources with demonstrated independence.
- Dedicated provider accounts/quotas where practical; separate keys alone may share an account quota.
- Simulator profile uses one LLM request and one TTS request per turn, without the existing telephony profile's parallel hedging.
- No mounts of existing service directories, secrets, Docker socket, or existing call stores.
- No reuse of phone-brain.sh start/restore/sync commands; those operate on existing services.

Reuse of an existing cellular gateway is a later option only if the model supports a separately routable spare channel, its configuration can be changed without disrupting existing calls, and the user accepts that bounded integration. Otherwise choose a dedicated inbound gateway or trunk. Hardware purchase is outside this plan.

## Controller and run contract

Proposed API, not existing functionality:
- POST /sim/v1/runs: prepare one scenario; return run_id, waiting state, expiry and an authorized dial target reference.
- GET /sim/v1/runs/{id}: sanitized state and failure reason.
- GET /sim/v1/runs/{id}/events: bounded resumable event feed.
- GET /sim/v1/runs/{id}/report: terminal report.
- POST /sim/v1/runs/{id}/stop: end only this run and its bound call.
- POST /sim/v1/incoming: authenticated gateway callback; not a public dialing interface.

Use a unique per-run SIP extension if supported. For a single PSTN number, reserve its line exclusively for the run's waiting window. Caller ID is only an additional hint, never the sole authentication or matching authority. The dedicated gateway callback identity, called destination, reservation, and current state must all agree. A late or second call gets rejected rather than assigned to the next run.

State path:
prepared -> waiting -> ringing -> answering -> connected -> ending -> completed
Terminal alternatives: rejected, expired, failed, cancelled, aborted.

The caller's CANCEL during the ringing delay prevents Accept. Duplicate webhook/answer/end events are idempotent. A run becomes connected only after gateway answer confirmation and its bound voice session are both ready. Release one greeting only after that barrier. An accept timeout or voice failure ends the call instead of leaving a silent connection. A process restart marks unfinished runs aborted and never silently resumes an old call.

Initial limits: 120-second reservation, 1.5-second answer delay, 5-second setup deadline after Accept, 120-second connected duration, 12 remote turns, 15-second inactivity timeout. These are configurable test defaults. No automatic redial.

## Scenario format and starting personas

Use JSON to match this repository and avoid a YAML dependency. A versioned example:

```json
{
  "schema_version": 1,
  "id": "appointment-confused",
  "language": "zh-CN",
  "persona": "A busy customer who answers briefly and asks what appointment this is.",
  "facts": {"available_day": "Thursday", "available_period": "afternoon"},
  "greeting": "喂，哪位？",
  "answer_delay_ms": 1500,
  "max_duration_ms": 120000,
  "max_remote_turns": 12,
  "idle_timeout_ms": 15000,
  "actions": [{
    "id": "clarify-once",
    "trigger": "remote_speech_started",
    "occurrence": 1,
    "after_ms": 700,
    "require_remote_speaking": true,
    "kind": "speak",
    "text": "等一下，你说的是哪个预约？",
    "max_hold_ms": 3000
  }],
  "retain_transcript": false,
  "retain_audio": false
}
```

Starting personas: cooperative customer, confused customer, busy customer who interrupts once. Keep facts immutable within a run. The assistant must elicit them through speech. If the remote assistant asks the simulator to abandon its role, it continues its customer persona.

Two modes share the same call path:
- Natural mode: the LLM selects short replies consistent with persona and facts.
- Scripted mode: event-triggered utterances, silence and hangup actions with deterministic scheduling.

Pin scenario version, source commit, prompt hash, provider model and voice identifiers in each report. A fixed scenario does not make provider output or carrier timing deterministic.

## Voice and intentional interruption

The receiver's incoming audio is the remote assistant, even though the underlying LLM protocol calls it a user message. The simulator's generated audio is the customer. Reports normalize these roles; they must not accidentally score the simulated customer as the assistant.

Ordinary commentary is unsuitable for guaranteed interruption because it waits for input silence. Add a simulator-only scheduled utterance:
- Pre-synthesize scripted audio before the run becomes available when using a real provider.
- Trigger from the remote audio/VAD boundary observed by this receiver, not from the assistant's internal transcript.
- Start the requested utterance even when remote speech continues.
- Keep input/ASR active, but defer ordinary response-turn creation while this bounded action owns output.
- Release that ownership on playout completion, timeout, cancellation, call end or session error.
- Preserve the spoken prefix in history if interrupted or stopped; discard unheard queued tails.
- Stop must immediately win over scheduled speech; late callbacks cannot revive it.

Disable speculation, autonomous backchannels and fillers in the initial deterministic simulator profile. Preserve all existing normal-engine defaults. Natural variation and noise injection can be added after the basic call works.

The simulator must not use the legacy /v1/opening endpoint. Its outer mux rejects that path and provides strict internal session operations with exact run -> call -> session binding. A wrong or missing identifier returns an error and affects no sessions.

## Measurement and evidence

Two independent views:
- Conversation checks: identity explanation, clarification response, appointment facts, graceful ending. Evaluate synthetic transcript in memory; return pass/fail/indeterminate with evidence availability. A semantic model score is separate from deterministic protocol checks.
- Audio behavior: response gap, overlap duration, interruption stopping delay, silence and disconnect behavior.

Initial timestamps use one monotonic clock at the simulator's successful WebSocket writes and received audio frames. Mark measurements application_audio_boundary. First non-silent returned speech counts; arbitrary first bytes, silence packets and locally generated prompts do not. Measure Claude's response from simulated-customer speech end to remote-assistant speech onset. Report the simulator's own response latency separately.

These measurements include neither proven handset playout nor the complete gateway/carrier buffering path. Add receiver-side RTP instrumentation later if needed, explicitly labeled rtp_observed. Acoustic evidence requires a handset/audio fixture and is separate.

Interrupted, overlapped, timed-out and failed turns remain visible and are classified separately; do not silently exclude them from reports. A short three-call pilot proves functionality, not a p95 latency claim.

Metadata is saved by default in the new simulator's data directory. Audio/transcript retention is explicit per run; synthetic facts only. No credentials, actual phone numbers, unbounded logs, or raw provider responses enter reports. A missing semantic transcript produces indeterminate rather than pass.

## Delivery stages

1. Controller and behavior unit tests using fake clocks, no hosts or providers.
2. Isolated incoming SIP fixture and mock voice providers; proves call/media lifecycle only.
3. Real speech on the dedicated environment with separate provider quota; proves recognition and persona behavior.
4. Real carrier call after hardware/routing prerequisites are verified.
5. Claude uses prepare/status/report/stop through HTTP or CLI. MCP is an optional later wrapper around the same contract.

The first usable release answers one incoming call, says hello, sustains a short natural conversation, performs one planned interruption, ends cleanly, and returns a correctly labeled report.

## Deferred decisions

These do not block implementation:
- Receiving gateway model and independent inbound channel, or new inbound SIP trunk.
- Dedicated deployment host and network/RTP mapping.
- Actual test number and permitted caller route.
- Provider accounts/quota and voice choice.
- Whether handset acoustic behavior needs a separate physical-phone stage.

Defaults: Chinese customer voice, appointment callback scenario, one call at a time, no retained audio, no shared deployment host, no automatic outbound calling.

