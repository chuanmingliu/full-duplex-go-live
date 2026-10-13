# Inbound User Simulator Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build an isolated service that answers an assistant's telephone call, behaves as a simulated customer over full-duplex audio, and returns a test report.

**Architecture:** Add a separate golive-simulator binary with a run controller, inbound Active Call adapter, and simulator-only session controls. Reuse golive speech providers and media handling; a deterministic scheduler controls scripted behavior and the normal model handles natural replies. Deploy the receiver independently from every existing service.

**Tech Stack:** Go >=1.24.7, existing gorilla/websocket v1.5.3, standard-library JSON/HTTP, existing golive ASR/LLM/TTS providers, pinned Active Call, optional dedicated RustPBX, Docker Compose.

**Spec:** [Design specification](../specs/2026-10-13-inbound-user-simulator.md)

Planning date: 2026-10-13 (Asia/Shanghai).
Implementation has not started. This document is stored outside both repositories.

## Global Constraints

- All implementation uses a new branch and isolated checkout of chuanmingliu/full-duplex-go-live.
- Existing services, binaries, configurations, routes, working trees, recordings, and credentials remain untouched.
- Use a separate deployment host by default; separate ports on a shared host do not establish non-interference.
- The simulated user receives calls; this service has no outbound dial endpoint.
- The live conversation crosses an audio connection; never inject the other agent's internal text, prompt, hidden state, or tool results.
- One admitted call at a time in the first release.
- Audio capture and transmission remain concurrent, including during scripted speech.
- Unknown, mismatched, expired, or already-bound call identifiers cannot open another session.
- No deployment, provider calls, carrier calls, or changes to existing routing occur during planning.

Start from commit 66262931f0ad258d7d95c9acad1c3e996130a7e1 of telephony-realtime, on proposed branch codex/inbound-user-simulator. At execution, resolve the correct repository and create an isolated checkout; do not switch the current openai-gpt-live checkout or include its dirty files. If the upstream branch has advanced, explicitly compare the changed integration seams before choosing a newer baseline. No force pushes.

## Review Focus

1. Duplicate incoming events or caller cancellation during the answer delay: no double answer, no greeting after cancellation. Task 1 and Task 2.
2. Media/audio arriving before scenario installation: reject rather than start with a default assistant prompt. Task 3.
3. Unknown opening ID or stale callback from a previous run: no broadcast/fallback and no effect on the current run. Task 3.
4. Remote ASR final arriving during a scripted interruption: input continues and scheduled speech is not accidentally retired. Task 4.
5. Simulator restart, disconnect, or missing evidence: terminate/mark the run correctly; do not report success or replay queued speech. Tasks 2 and 5.

## File ownership

All following paths are inside the NEW checkout of full-duplex-go-live.

| Component | New files | Existing files with narrowly scoped changes |
|---|---|---|
| Scenario/run model | internal/simulator/scenario.go, state.go, state_test.go, scenario_test.go; scenarios/simulator/*.json | None |
| Controller/API | internal/simulator/controller.go, http.go, controller_test.go, http_test.go | None |
| Incoming adapter | internal/simulator/activecall.go, activecall_test.go; test/simulator/fixtures/* | None |
| Session admission | internal/server/simulator.go, simulator_test.go | internal/server/realtime.go and session.go, optional hooks only |
| Scripted speech | internal/duplex/simulator.go, simulator_test.go | internal/duplex/engine.go, listen.go, think.go, only guarded scheduling seams |
| Report/observer | internal/simulator/observer.go, report.go, observer_test.go, report_test.go | internal/server/session.go for write/read boundary callbacks if necessary |
| New command | cmd/golive-simulator/main.go, main_test.go | None; cmd/golive/main.go stays unchanged |
| Isolated package | deploy/simulator/Dockerfile, compose.yml, README.md; configs/simulator.mock.json, simulator.real.json | None; existing Dockerfile and telephony profile stay unchanged |
| Acceptance harness | test/simulator/incoming_test.go, duplex_test.go, README.md | None |

Avoid importing internal/server into internal/simulator. The new command wires adapters through small interfaces, keeping the core reducer and controller independent of the server.

## Shared contracts

Define these proposed types in internal/simulator/state.go; the JSON schema in the spec is authoritative for Scenario and its Action objects.

```go
type Binding struct { RunID, CallID, SessionID string }
type Phase string
type State struct {
    Phase Phase
    Binding Binding
    AnswerSeen, VoiceReady, GreetingReleased bool
}
type Event struct {
    Kind string
    Binding Binding
    AtMS int64
    Reason string
}
type Effect struct { Kind string; Binding Binding }
func Advance(State, Event) (State, []Effect, error)
```

Phase values: prepared, waiting, ringing, answering, connected, ending, completed, rejected, expired, failed, cancelled, aborted.
Event kinds: reserve, incoming, answer_due, voice_ready, answered, stop, ended, expired, failed.
Effect kinds: accept, reject, open_greeting, hangup, close_voice, finalize.

The reducer receives monotonic relative times; scheduling deadlines and scenario text stay in Controller. A binding can add its SessionID exactly once after gateway correlation; conflicting values always fail.

Define these controller ports in internal/simulator/controller.go:

```go
type Telephony interface {
    Accept(context.Context, string) error // call ID, receiving-only configuration
    Reject(context.Context, string, string) error
    Hangup(context.Context, string) error
}
type Voice interface {
    Open(context.Context, Binding, string) error
    Speak(context.Context, Binding, string, string, time.Duration) error
    Close(context.Context, Binding) error
}
// Speak arguments after Binding: action ID, utterance text, maximum hold.
type Snapshot struct { RunID string; Phase Phase; Reason string }
type Controller interface {
    Prepare(context.Context, Scenario) (Snapshot, error)
    Handle(context.Context, Event) error
    Get(context.Context, string) (Snapshot, error)
    Stop(context.Context, string) error
}
```

Implement Controller as unexported controller with serialized event handling and injected clock/timers. No global singleton. Telephony.Accept uses a call-specific option structure owned inside activecall.go; it must not depend on the old service's global prompt or URL. Missing configuration fails readiness.

## Task 1: Scenario validation and deterministic run lifecycle

**Files:** new internal/simulator/scenario.go, scenario_test.go, state.go, state_test.go; three scenario JSON files.

**Interfaces:** DecodeScenario(io.Reader) (Scenario, error); Scenario.Validate() error; Advance(State, Event) (State, []Effect, error).

- [ ] Define Scenario fields exactly as the spec's JSON example. Reject unknown fields, duplicate JSON keys, unsupported schema_version, duplicate action IDs, empty persona/greeting, negative timing, action holds above 5 seconds, and max duration outside 1–300 seconds. Limit input to 64 KiB; reject invalid UTF-8. Define ErrInvalidScenario and ErrInvalidTransition.
- [ ] Add the lifecycle tests below before implementing the reducer. Test both orders of answered and voice_ready; open_greeting must occur once after both. Add a stale binding case, second incoming call case and expired reservation case.
- [ ] Run `go test ./internal/simulator -run 'TestAdvance|TestScenario' -count=1`; expect failure until implementation exists.
- [ ] Implement allowed transitions with explicit effect lists. Terminal states do not resurrect. Unknown IDs return ErrInvalidTransition; never substitute the current run.
- [ ] Add fixtures: cooperative customer with no actions; confused customer with the spec's clarify-once action; busy customer with one interruption and a hangup at the scenario limit. Use only synthetic facts.
- [ ] Run the targeted tests, then `go test -race ./internal/simulator`; commit this component after it passes.

Representative test body:

```go
func TestAdvanceAnswerNeedsVoice(t *testing.T) {
    b := Binding{RunID:"r1", CallID:"c1", SessionID:"s1"}
    s := State{Phase:"answering", Binding:b}
    s, effects, err := Advance(s, Event{Kind:"answered", Binding:b})
    if err != nil || len(effects) != 0 { t.Fatalf("early greeting: %v %v", effects, err) }
    s, effects, err = Advance(s, Event{Kind:"voice_ready", Binding:b})
    if err != nil || s.Phase != "connected" || len(effects) != 1 ||
       effects[0].Kind != "open_greeting" { t.Fatalf("missing greeting barrier: %+v %v", s, effects) }
    _, effects, err = Advance(s, Event{Kind:"answered", Binding:b})
    if err != nil || len(effects) != 0 { t.Fatal("duplicate answer repeated greeting") }
}
```

## Task 2: Receiving controller and Active Call adapter

**Files:** new controller.go/controller_test.go, http.go/http_test.go, activecall.go/activecall_test.go and test/simulator/fixtures/.

**Interfaces:** implements Controller and Telephony above; HTTP routes exactly as the spec; gateway callback translates into Event.

- [ ] Pin an Active Call release/commit in the new deployment manifest. Inspect its source and documentation to capture exact Incoming, Accept, Answer, CANCEL/end and Reject wire fixtures. Record the source commit in fixtures/README.md. Do not copy credentials or live calls.
- [ ] Prove in an isolated contract test that inbound Accept can select the simulator realtime endpoint and propagate a call identity before media. If that version cannot, use a dedicated adapter/proxy that supplies correlation, or select a version with documented support. Do not ship a guessed outbound-Invite-shaped Accept payload.
- [ ] Write fixture-driven tests: duplicate incoming callback yields one Accept; incoming callback for an unreserved destination yields Reject; caller cancellation at 1499 ms prevents the scheduled 1500 ms Accept; Accept timeout ends that call; second simultaneous call is rejected; a late old callback cannot claim the next reservation.
- [ ] Write API tests for wrong token, unknown run ID, body over 64 KiB, absent idempotency key on prepare, repeated prepare with same key, and same key with different content. Use status codes 401, 404, 413, 400, 200 and 409 respectively.
- [ ] Run `go test ./internal/simulator -run 'TestController|TestHTTP|TestActiveCall' -count=1`; inspect expected failures.
- [ ] Implement the serialized controller using the reducer. Reserve one line; normalize only expected gateway identities. Require an authenticated callback source plus the reservation's called destination; caller ID alone cannot admit a call.
- [ ] Implement the adapter with bounded reconnect and request deadlines. After an uncertain Accept result, reconcile by call ID rather than blindly sending another Accept. Never retry by creating/dialing a new call. On Stop, send Hangup only for the bound ID and close its voice session.
- [ ] Cancel timers when a run ends. Preserve a small terminal metadata record; on process restart mark unfinished records aborted without replaying control effects.
- [ ] Add this reducer regression and rerun targeted tests plus race tests; commit.

```go
func TestCancelledCallCannotAnswer(t *testing.T) {
    b := Binding{RunID:"r1", CallID:"c1"}
    s := State{Phase:"ringing", Binding:b}
    s, _, err := Advance(s, Event{Kind:"stop", Binding:b, AtMS:1499})
    if err != nil { t.Fatal(err) }
    _, effects, _ := Advance(s, Event{Kind:"answer_due", Binding:b, AtMS:1500})
    for _, effect := range effects {
        if effect.Kind == "accept" || effect.Kind == "open_greeting" { t.Fatal(effect.Kind) }
    }
}
```

## Task 3: Strict simulator sessions and natural customer speech

**Files:** new internal/server/simulator.go, simulator_test.go, cmd/golive-simulator/main.go, main_test.go, configs/simulator.mock.json. Guarded edits to server/realtime.go and session.go.

**Interfaces:** implement simulator.Voice through a command-level adapter; expose a server-side SimulatorControl with exact Bind, Open, Speak and Close operations. The ordinary server has no simulator control capability.

- [ ] Add an admission hook to the new binary's server instance, executed before any audio can start an engine. It resolves immutable scenario instructions and audio format from an already-bound call. A gateway-supplied assistant prompt cannot replace the simulator persona.
- [ ] Write HTTP/WebSocket tests: early audio before binding fails; unknown call fails; second session for one call fails; conflicting call/session IDs fail; a ready session receives exactly its own persona. Test client paths `call`, `call_id`, `callId` consistently or accept only one documented spelling in the simulator.
- [ ] Write an opening test with TWO internal fixture sessions even though public admission is one call: an unknown or empty target opens neither; the correct target opens only itself; a repeated open does not repeat the greeting.
- [ ] Run `go test ./internal/server -run Simulator -count=1`; verify failures expose missing hooks.
- [ ] Implement strict SimulatorControl using exact identifiers. Reuse Session.Open mechanics after the controller's answer barrier, without invoking handleOpening. Disable the legacy held-session timeout's automatic release in simulator mode; timeout closes/fails the run.
- [ ] Compose a separate outer mux in cmd/golive-simulator that rejects /v1/opening, public /attach, /fork and /content. Expose only necessary authenticated controller routes and the admitted realtime media route. Observers use internal callbacks.
- [ ] Add per-session simulator options through the existing config-copy seam. New hooks are nil/off for cmd/golive, preserving defaults and normal endpoints.
- [ ] Configure mock providers by default, empty automatic greeting, one active session, no storage, no speculation/fillers/backchannels, and no hedging. Real credentials are loaded only by an explicit later profile.
- [ ] Run `go test -race ./internal/server ./internal/simulator ./cmd/golive-simulator`; run existing server parity and realtime tests; commit.

The two-session regression must assert actual emitted audio/open calls, not only an HTTP status. The original /v1/opening behavior remains unchanged in the ordinary binary and unreachable in this simulator.

## Task 4: Bounded speech while the remote assistant is talking

**Files:** new internal/duplex/simulator.go and simulator_test.go; guarded edits to engine.go, listen.go and think.go.

**Interfaces:** proposed SimulatorSpeechRequest and Engine.ScheduleSimulatorSpeech; serialized helpers startSimulatorSpeech and finishSimulatorSpeech.

```go
type SimulatorSpeechRequest struct {
    ActionID string
    Text string
    DueAt time.Time
    ExpiresAt time.Time
    MaxHold time.Duration
}
func (e *Engine) ScheduleSimulatorSpeech(context.Context, SimulatorSpeechRequest) error
```

The server's capability, not an untrusted client request, enables this method. Commands are posted to the existing serialized engine loop. Prepared audio buffers are bounded, session-owned and released at session end.

- [ ] Write a deterministic test that keeps remote audio/ASR active, schedules an already-prepared utterance, and observes output before remote speech stops. Use sample counts and a fake scheduler clock; do not wait seconds in tests.
- [ ] Add a remote final transcript during scripted playback and assert that input events continue, playback is not retired by tracker.Begin, and ordinary response generation resumes once after the bounded action ends.
- [ ] Add hangup at the scheduled instant, duplicate action ID, expired action, pre-synthesis failure and maximum-hold expiry tests. No late audio may appear after Close/Stop.
- [ ] Run `go test ./internal/duplex -run Simulator -count=1`; verify expected failures.
- [ ] Implement scheduling by reusing Engine.Say's committed-turn/speechPipe mechanics. Do not route through AppendCommentary/drainDeferred, which wait for silence.
- [ ] While the action owns output, keep PushAudio/ASR running but defer ordinary turn creation and yielding that would invalidate it. Bound the pending-final queue and fail the run on overflow; never silently lose customer-relevant audio. Release ownership on every terminal path.
- [ ] Preserve heard-prefix/truncation history. A required-remote-speaking action skips with an explicit reason if remote speech ends before its due time; it must not claim an interruption occurred.
- [ ] Add a regression proving ordinary commentary still waits for silence and normal interruption behavior is unchanged without simulator capability.
- [ ] Run `go test -race ./internal/duplex ./internal/server ./internal/simulator`; commit.

Scheduling correctness is separate from provider latency. Real-provider scripted utterances are prepared before making the reservation available. The report records the actual output start and trigger offset, even when a scheduled deadline is missed.

## Task 5: Reports that distinguish the assistant from the customer

**Files:** new internal/simulator/observer.go, report.go, observer_test.go, report_test.go; minimal internal/server/session.go observation hooks if required.

**Interfaces:**
```go
type BoundaryTurn struct {
    CustomerEndMS int64
    AssistantStartMS int64
    HasCustomerEnd, HasAssistantStart, Overlapped, Failed bool
}
type Gap struct { ValueMS int64; Valid bool; Reason string }
func AssistantGap(BoundaryTurn) Gap
type Check struct { Name, Result, Evidence string } // pass, fail, indeterminate
```

- [ ] Add the test below. Add cases for silence-only audio, missing response, overlapping utterances, failed calls, duplicate events and a backwards timestamp. Invalid or missing measurements must carry a reason and stay in the denominator/report.
- [ ] Run `go test ./internal/simulator -run 'TestAssistantGap|TestReport|TestObserver' -count=1`.
- [ ] Implement one receiver-local monotonic clock at successful audio writes and received audio frames. Detect first non-silent remote speech; label every measurement application_audio_boundary. Never subtract clocks from different hosts.
- [ ] Normalize roles explicitly: engine input = remote assistant; engine output = simulated customer. Report response gaps for the assistant separately from those for the simulator.
- [ ] Add deterministic checks for answer/greeting count, interruption overlap, call limits and clean termination. Semantic checks return indeterminate when evidence or an evaluator is absent. Keep model judging distinct from protocol facts.
- [ ] Write bounded report.json with source commit, scenario/prompt hashes, provider/voice identifiers, action outcomes, failures, sample counts and evidence labels. Retain audio/transcript only when scenario explicitly requests it, in the simulator's private data directory; no phone numbers or secrets.
- [ ] Test report redaction and truncated/missing artifacts. Run package/race tests and commit.

```go
func TestAssistantGapUsesCustomerEnd(t *testing.T) {
    g := AssistantGap(BoundaryTurn{
        CustomerEndMS:1000, AssistantStartMS:1800,
        HasCustomerEnd:true, HasAssistantStart:true,
    })
    if !g.Valid || g.ValueMS != 800 { t.Fatalf("%+v", g) }
    missing := AssistantGap(BoundaryTurn{HasCustomerEnd:true, CustomerEndMS:1000})
    if missing.Valid || missing.Reason == "" { t.Fatalf("%+v", missing) }
}
```

## Task 6: Package the independent receiver and prove the complete call

**Files:** deploy/simulator/Dockerfile, compose.yml, README.md; configs/simulator.real.json; test/simulator/incoming_test.go, duplex_test.go, README.md.

**Interfaces:** new binary accepts `-profile`, `-addr`, `-media-addr`, `-data-dir` and `-activecall-url`. Health routes are /livez and /readyz; readiness requires simulator configuration and dedicated gateway control availability, not just a TCP listener.

- [ ] Add isolated integration tests using a pinned Active Call instance and independent SIP caller. Verify ring -> accept -> greeting -> speech in both directions -> one planned overlap -> end -> report. Use synthetic PCM/scripted providers for transport/lifecycle tests and label them accordingly.
- [ ] Add a separate opt-in real-speech test: real recognizable utterances, real ASR/LLM/TTS with independent accounts, cooperative and confused personas. Mock tone success must not mark this test passed.
- [ ] Create a new Dockerfile and Compose project golive-user-simulator. Use separate images, directories, network and secrets; no old containers, host Docker socket, existing volumes or baseline paths. One LLM request and one TTS request per turn in the real profile.
- [ ] Add a package validator rejecting occupied requested host ports, production-path mounts, old container/service names and mutable dependency tags. Use loopback controller 18890 and internal media 18891 by default. Keep SIP/RTP mapping explicit for the dedicated target's network; fail before startup if it is incomplete.
- [ ] Document commands that name only this Compose project and file. Destructive cleanup is never a generic docker prune or a broad service match. No shared-host deployment by default.
- [ ] Run `go test -race ./...`, `go vet ./...`, `go build ./cmd/golive ./cmd/golive-simulator` and the isolated integration suite. Report external tests as not-run if their prerequisites are absent.
- [ ] Review the whole branch diff and package manifest. Confirm cmd/golive defaults, existing config files, existing deployment scripts and existing services are unchanged. Commit the isolated package and runbook.

## Task 7: Carrier acceptance after hardware access returns

**Files:** new docs/simulator-carrier-acceptance.md; no changes to existing deployment files.
**Consumes:** all prior deliverables, dedicated receiver host, verified inbound route, test destination, separate provider capacity.
**Produces:** a carrier-observed acceptance report, or an explicit unmet-prerequisite report.

- [ ] Complete the staged read-only gateway inventory when the user can access a terminal. Identify actual model and voice channels; an idle service or unused SIM slot is not proof of simultaneous voice capacity.
- [ ] Verify registration, inbound support, independently routable destination, codec negotiation, answer/hangup propagation and supported concurrent channels. Require two usable call legs when both ends use cellular gateways.
- [ ] Choose the independent receiver: dedicated inbound SIP trunk/number or supported dedicated SIM gateway. Existing gateway reuse remains conditional under the spec's non-interference requirement.
- [ ] Resolve dedicated host, NAT/RTP mapping and exact caller/destination outside version control. Produce a concrete deployment manifest for user review; this plan itself does not authorize changes to shared infrastructure.
- [ ] When deployment and the exact test destination are authorized, start only the new receiver. From the assistant side, make one short cooperative call, one clarification call and one interruption call; no automatic retry.
- [ ] Verify real audio on both legs, greeting after answer, interruption, clear hangup and labeled reports. Count failures. Do not claim acoustic verification without a handset/audio fixture or p95 from a three-call pilot.
- [ ] Rollback stops/removes only the simulator project and its dedicated resources. Existing services and routes retain their prior identities/configurations.

## Self-review record

- Spec coverage: isolation and no outbound path (Tasks 2, 6, 7); persona (1, 3); answering/lifecycle (1, 2, 3); simultaneous audio and intentional interruption (4); reports/privacy (5); independent deployment and real carrier boundary (6, 7).
- The five review-focus failure modes each have explicit tests above.
- Proposed APIs are labeled as new; existing public APIs are not claimed to support the new behavior.
- Hardware, Active Call version contract and provider prerequisites are explicit gates, not fabricated facts.
- Natural-language provider behavior is not claimed deterministic.
- Local SIP, application audio, carrier and acoustic evidence remain distinct.
- Existing service non-interference requires resource and account isolation as well as a branch.

## Execution handoff

Tasks 1–6 can be implemented and tested in a separate development environment without access to the existing Mac/ThinkPad services. Real-provider tests additionally need dedicated provider access. Task 7 waits for hardware and carrier prerequisites.

Recommended execution method when implementation is requested: subagent-driven, with one owner for core engine/server changes and one owner for the independent controller/adapter, after the shared interfaces are fixed. Review each component and then the whole branch. Do not start implementation merely because this plan exists.

