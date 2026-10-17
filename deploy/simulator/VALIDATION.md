# Validation record

Performed locally on 2026-10-13, using a separate clone/branch from telephony-realtime baseline `66262931f0ad258d7d95c9acad1c3e996130a7e1`. Existing Mac/ThinkPad services, gateway configuration and provider accounts were not changed. No carrier or public phone calls were placed.

## Automated checks

- Simulator controller/native adapter race tests pass.
- Receiver race tests pass: exact binding, duplicate/stale rejection, scenario protection, no default session from early audio, answer/readiness greeting gate, scripted simultaneous audio, media failure cleanup, and BYE/media ordering.
- Duplex simulator tests pass 19 named cases plus subcases, repeated 3 times under race detection. Eight normal engine regression tests also pass after the speech changes.
- Full `go test -race ./...` completed: duplex 280.373s, simulator 8.848s, provider 13.985s and other packages pass. Server fails only `TestRealtimeTurnBoundariesReachTheLatencyConsole`, reproduced on the untouched baseline: expected `monotonicNs` is empty on this macOS environment. No data-race reports. The later small receiver fixes are covered by focused race tests.
- Ordinary full `go test ./...` also exposed `TestRealtimeCallsAreStoredWhenTheEndpointIsToldTo` and `TestHedgedFirstSegmentIsTheConnectionThatSpokeFirst`; both were reproduced without simulator changes. The hedge test says the losing connection was not told to stop. Those unrelated baseline tests were not edited or suppressed.
- `go vet ./...`, original/new command builds and 5 Python isolation-validator tests pass. Compose configuration, free requested loopback port and unused project identity validated before starting only the new project.
- Independent core and speech reviews completed, then a whole-branch review. Found lifecycle/report/history edge cases were fixed with regressions. The final observation-ordering fix reproduced four bad interleavings before correction, then passed 40 forced interleavings under race detection and the full focused receiver suite (6.306s). Scoped re-review approved the fix with no remaining actionable P0/P1/P2 findings.

## Actual local SIP test

Active Call source is pinned to `eaf7ebc8f39193a289348989f6a09b0ace287da7`; the new image builds its committed local dependency lock with `--locked`. The package patches missing custom-WebSocket handshake headers. An independent Python SIP client exchanges synthetic PCMU with the pinned gateway; the gateway exchanges 16kHz PCM16 with the receiver.

The reproducible `run_sip_checks.py` harness passed all three cases against receiver code `16031e484250df7420b5a4e5b8f617fb8eb85f71`:

| Case | Signaling | Sent RTP | Received nonsilent RTP | Application result |
| --- | --- | ---: | ---: | --- |
| Cooperative | Answer 1515ms; BYE 200 |309|132| One greeting, clean end |
| Confused/interruption | Answer 1512ms; BYE 200 |311|142| Script requested, started and completed; 1074ms observed overlap; clean end |
| Cancel before answer | CANCEL 1000ms; INVITE 487 |0|0| No answer, greeting or media session |

All three final reports had zero invalid observations and no recorded failures. An earlier confused-case run exposed media closing before the control hangup notification; it was retained as a failing test result and fixed with a bounded 500ms signaling grace after immediate output closure. The re-run passed.

These are **local SIP transport and application-audio results**, using synthetic tones and mock providers. They do not establish recognizable conversation quality, carrier reachability, two simultaneous SIM channels, handset acoustics or a meaningful latency percentile. Real-provider and carrier acceptance remain unrun. Semantic report checks are `indeterminate`.

## Implementation decisions and consequences

- Separate clone on a feature branch: avoids the unrelated active checkout and deployed services; costs an extra checkout.
- Tasks 1–6 implemented locally; carrier acceptance is a runbook until hardware/routing access returns. External readiness is not implied.
- Work was split among agents with disjoint controller, speech, gateway and receiver ownership; integration and commits remained coordinated by the main agent.
- Go used an isolated writable cache and approved localhost test sockets; this changes only the test environment. Public Docker downloads used a temporary anonymous Docker config because the desktop credential helper stalled, leaving existing credentials unchanged.
- HTTP control and media share private port 18865, using separate secrets and disjoint handlers, instead of the plan's two listeners. This costs separate firewall policy per listener; split them before any public media exposure.
- A unique run ID in the called SIP extension is required. Fixed-number carrier routing remains deferred, so late calls cannot consume a later reservation accidentally.
- The gateway handshake patch and generated lock are isolated in a new pinned image. They need maintenance until an upstream version is proven compatible.
- Missing semantic evaluation returns indeterminate; audio retention and scripted silence are rejected explicitly. Additional evaluator/recorder/silence-control work is needed for those features.

Final result details are saved in `validation-results.json`. Temporary project containers and their network were stopped after validation; the separate branch, images and private report volumes are preserved. No merge or push was performed.

## Workbench and evaluator extension — 2026-10-16

The checks below concern the configuration workbench, task scenarios, control APIs, suite runner and independent evaluator. They do not replace the original SIP measurements above. Development continued in the isolated `codex/inbound-user-simulator` clone; existing services, remote hosts and provider accounts were untouched.

- Race tests pass for `internal/simulator`, `internal/simclient`, `internal/simeval`, `internal/simulatorui`, `scenarios/simulator` and both simulator commands. After the final hangup publication fix, the targeted server `Simulator` race suite passed again (6.227s).
- Core tests cover old prompt/hash compatibility, strict new fields and exact JSON key casing, cancellation provenance, bounded listing, retention gates, restart transcript identity and truncation. Scripted hangup tests require observed termination plus successful controls; forced callback interleavings keep reports unavailable and the reservation occupied until final action evidence settles. Restart during pending termination remains aborted and indeterminate.
- CLI tests use fake local servers for sequential reservations, exact target identity, cancellation cleanup, redirects, timeouts, ambiguous preparation and private artifacts. Saved action lifecycle states preserve `requested` alongside `started` and `completed`.
- Evaluator tests use a fake judge. Missing/null truncation flags, failure arrays or timestamps, mismatched identities, incomplete transcripts and invalid judge output remain indeterminate. These tests make no real provider requests.
- Browser tests use the actual embedded assets under their CSP against a fake local API. They cover authentication, strict imports, scenario/profile export, exact run selection/stop, failed list refresh after successful preparation, fixed effective 16 kHz settings, report/transcript gates, inert injected text, polling cleanup and 375px layouts.
- The real `golive-simulator` binary passed `check_workbench.py`: temporary mock receiver, independent fresh tokens, temporary data directory and loopback fake gateway. Authentication, UI assets, config/presets/readiness, non-mutating validation, idempotent reservation, exact target, listing, stop, report hashes, transcript denial and clean shutdown passed. Only readiness `GET /list` requests reached the fake gateway; no signaling or media calls were made.
- Both simulator commands build with `-buildvcs=false`; `go vet ./...`, `git diff --check` and the five Python package isolation tests pass. Full-suite baseline failures recorded above remain outside this change; they were not hidden or rewritten.
- Independent task reviews and the final integrated review approved the extension through `6d16c4c`. The final fixes were re-reviewed with no remaining actionable P0/P1/P2 findings. Approval covers the isolated feature-branch push, not deployment or live conversation acceptance.

Real speech, end-to-end conversation with Claude, carrier routing and gateway capacity remain unverified. Profile export requires a deliberate restart of the isolated simulator; it does not apply settings to any running service. Semantic grades remain separate from protocol evidence and from the suite summary.

## First real-provider findings and receiver corrections — 2026-10-16

The following live observations were supplied by Claude/the operator on desk
`n10`, with related routing/source notes `n8` and `n7`. Codex did not originate
these calls or independently measure their audio:

| Evidence | Reported result |
| --- | --- |
| Runs `sim-3e5c23b5…`, `sim-6448e4f2…`, `sim-6b7d9745…` | Answer at 1.71 s; customer audio at 1.8–2.6 s; assistant audio at 2.4–8.6 s |
| Suppressed RTP after greeting | Voice failure at 23.66 s, about 15 s after last audio; Tencent ASR code 4008 |
| PBX forwarding | Request-URI `sip:sim-<id>@172.20.0.2:25060` rejected before a temporary `SIM_SIP_DOMAIN` override |
| SDP/source mismatch | SDP `192.168.71.9`, possible bridge source `172.20.0.1`; bridge counts 163 / 60 packets |

These calls establish reported bidirectional transport with real providers,
but not a successful multi-turn conversation. The corrections port the quiet
line behavior from `telephony-realtime` commit `d0bb53d` into the isolated
receiver and accept gateway-address Request-URIs while preserving exact
reservation binding. The simulator uses a fixed 100 ms gap and 20 ms PCM16
frames; ordinary service settings and existing simulator profiles are unchanged.

The RTP audit verified the exact Active Call pin `eaf7ebc8` and the locked
`rustrtc 0.4.7` crate checksum. Default source latching supports the reported
address difference; no gateway patch was needed. See the
[source references and qualifications](activecall/README.md#rtp-source-address-and-latching).
This audit did not run the Rust transport tests or verify the deployed image.

Local verification of code commits `c55896c` (forwarded URI) and `dbacec4`
(quiet input), performed with mocks and temporary loopback sockets:

- Both reported failures were reproduced before their fixes. The receiver
  regression sends 400 ms of voiced PCM and then no more packets; it now
  observes zero PCM at ASR, VAD speech-stop, completed transcription and a second
  customer response. Wire speech evidence still contains one remote turn.
- Six additional line-input tests cover the 100 ms threshold, 20 ms / 640-byte
  frames, fresh and continuous audio, empty appends, no catch-up burst after a
  15-second gap, admission, cancellation, stop/join and evidence-before-enqueue.
- A fresh combined race run passed **117 top-level tests**: 89 across simulator,
  client, evaluator, workbench and CLI packages; 28 server tests covering the
  simulator plus ordinary Session, Realtime and binary-audio paths. The server
  subset took 21.942 s. No data races or test failures were reported.
- `go vet ./...`, `go build -buildvcs=false ./...`, the five Python isolation
  tests and `git diff --check` passed. The rebuilt receiver passed
  `check_workbench.py`; only readiness probes reached its fake gateway.
- Independent review of both fixes and documentation found no remaining
  actionable correctness or security issue. The full repository test suite
  was not rerun; its previously recorded baseline failures remain unchanged.

The corrections require a new real-provider call after deployment of the
dedicated receiver. Successful local tests do not establish that Tencent 4008
is absent on a live connection. This work does not deploy, restart, reroute or
place calls through an existing service.

## TTS recovery, scenario limits and flood diagnostics — 2026-10-17

Investigation of desk `n15`, `n16`, `n17` / `ISS-039` used read-only queries
against the ThinkPad. The source checkout and retained reports identified
`a4f77d5`. No credentials or conversation content were copied into this record.

| Retained evidence | Observation |
| --- | --- |
| `sim-a92cd75ac88dab9e77970ca3` | 40 remote speech starts; `remote turn limit` stop at 162146 ms; control-disconnected failure at 162147 ms |
| `sim-8d8e941772211aacb3beba3b` | 40 remote speech starts; `remote turn limit` stop at 1456526 ms; control-disconnected failure at 1456527 ms |
| `sim-212aefbe…`, session `sess_3c3f396751786bde7a716e8e` | Audio rate rejection at `2026-10-17T01:38:17.08236553Z`; old log has no byte/frame counters |

The event times above are the report's event offsets, not inferred call
durations. Both sampled control disconnects followed a scenario-limit stop by
1 ms. The old stop effects closed media before requesting hangup, allowing the
gateway to tear down first. TTS chunk errors were also present, but source
inspection does not establish them as the cause of these disconnects: the old
chunk-error path abandoned the speech pipe without a segment-end event, and
the pinned gateway logs Realtime `error` messages without directly hanging up.

Corrections on the isolated feature branch:

- `89565db`: simulator-only recovery for TTS open, synthesis, chunk and empty
  segment failures. Drop the failed segment, abandon partial text in spoken
  history, and continue later segments/turns. Emit only sanitized warning IDs.
- `00bf6c4`: numeric wire-audio counters on rate-limit rejection and five
  regressions proving silence fill is exempt, normal resume fits the 512 kbps
  budget, and actual wire floods still close. The filler already bypassed the
  guard at `a4f77d5`; the historical n15 cause remains unconfirmed. This does
  not raise the configured threshold or claim the 2048 kbps workaround is needed.
- `68a0de2`: warning counts, per-response timing invalidation, effective duration
  and speech-burst limits, native termination attribution, and quiesce-before-
  hangup ordering. Native evidence can enrich a failed report without converting
  failure into success. Generic `system` / `BySystem` termination remains unknown.
- `1bb8725`: server warning/response correlation, media quiescence without
  closing its WebSocket, and strict warning-aware protocol verdicts and report
  sanitization. Semantic evaluation still rejects incomplete speech evidence.

`max_remote_turns` retains its existing meaning: received speech bursts separated
by 200 ms of silence. It is not a count of semantic assistant turns. The new
report fields expose both the effective limit and the observed starts; the old
40-start records alone do not prove which scenario settings the caller intended.

Local regression tests reproduced the dropped-TTS reporting and premature
media-close behavior before the corrections, then passed under the race
detector. Coverage includes all four warning codes, later response recovery,
partial speech, warning persistence, response-specific gaps, strict warning
projection, native caller/local/unknown termination, and missing confirmation.
The receiver WebSocket tests verify continued audio after a recoverable chunk
failure and quiet-but-connected media until native hangup confirmation.

`go vet ./...`, `go build -buildvcs=false ./...`, the rebuilt receiver's
`check_workbench.py`, and all five Python package tests passed. The workbench
check used temporary loopback processes and only readiness probes reached its
fake gateway. Independent integrated code review found no remaining actionable
findings.

The final `go test -race -json ./... -count=1` run against code `1bb8725`
completed: **458 top-level tests passed, 1 skipped, 2 failed** (722 passes
including subtests). There were no race diagnostics. The full duplex package
passed in 283.076 s; all new simulator regressions passed. The only failures
were the previously documented ordinary-server baseline cases:

- `TestRealtimeCallsAreStoredWhenTheEndpointIsToldTo`: temporary directory
  cleanup raced a remaining writer (`directory not empty`).
- `TestRealtimeTurnBoundariesReachTheLatencyConsole`: `monotonicNs` was empty
  on this macOS environment.

These tests were neither changed nor suppressed; the full repository suite is
therefore not green despite the passing scoped regressions.

Deployment and real-provider acceptance are still required. This work does not
restart the receiver, change the remote profile or gateway, or place calls.
