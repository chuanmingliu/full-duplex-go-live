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

- Race tests pass for `internal/simulator`, `internal/simclient`, `internal/simeval`, `internal/simulatorui`, `scenarios/simulator` and both simulator commands. The targeted server `Simulator` race suite also passed (7.007s).
- Core tests cover old prompt/hash compatibility, strict new fields and exact JSON key casing, cancellation provenance, bounded listing, retention gates, restart transcript identity and truncation.
- CLI tests use fake local servers for sequential reservations, exact target identity, cancellation cleanup, redirects, timeouts, ambiguous preparation and private artifacts. Saved action lifecycle states preserve `requested` alongside `started` and `completed`.
- Evaluator tests use a fake judge. Missing/null truncation flags or timestamps, mismatched identities, incomplete transcripts and invalid judge output remain indeterminate. These tests make no real provider requests.
- Browser tests use the actual embedded assets under their CSP against a fake local API. They cover authentication, strict imports, scenario/profile export, exact run selection/stop, failed list refresh after successful preparation, fixed effective 16 kHz settings, report/transcript gates, inert injected text, polling cleanup and 375px layouts.
- The real `golive-simulator` binary passed `check_workbench.py`: temporary mock receiver, independent fresh tokens, temporary data directory and loopback fake gateway. Authentication, UI assets, config/presets/readiness, non-mutating validation, idempotent reservation, exact target, listing, stop, report hashes, transcript denial and clean shutdown passed. Only readiness `GET /list` requests reached the fake gateway; no signaling or media calls were made.
- Both simulator commands build with `-buildvcs=false`; `go vet ./...`, `git diff --check` and the five Python package isolation tests pass. Full-suite baseline failures recorded above remain outside this change; they were not hidden or rewritten.

Real speech, end-to-end conversation with Claude, carrier routing and gateway capacity remain unverified. Profile export requires a deliberate restart of the isolated simulator; it does not apply settings to any running service. Semantic grades remain separate from protocol evidence and from the suite summary.
