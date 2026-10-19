# Native Active Call fixtures

Synthetic source-contract fixtures for miuda-ai/active-call at immutable commit
`eaf7ebc8f39193a289348989f6a09b0ace287da7` (0.3.100). They contain no live calls.

Sources at that commit: `src/useragent/webhook.rs`, `src/call/mod.rs`,
`src/event.rs`, `src/handler/handler.rs`, `src/media/realtime_processor.rs`, and
`tests/ws_accept_answers_sip_test.rs` (CANCEL before the first command).

The webhook event is **invite**; `dialogId` is authoritative for control.
`sipCallId` is a separate SIP transaction identity. Attach with
`/call/sip?id=<URL-encoded-dialogId>`. A custom realtime URL is the string value
of `option.realtime.provider`, with explicit `run`, `call`, and `token` query
parameters. No custom-provider Authorization header is added upstream.

The adapter sends `accept`, `reject` (486), and `hangup` commands. It receives
`answer`, `reject`, and `hangup` events. Caller CANCEL is a `hangup` event with
`reason: Canceled` and `initiator: caller`; there is no cancellation webhook.
Native WebSocket Ping frames are answered by the Go WebSocket implementation.

The main SIP leg emits `trackEnd` (same `trackId`, no `playId`) before `hangup`
from `InviteDialogStates::on_terminated` in `src/call/sip.rs`. The adapter retains
that native end if control teardown drops the following hangup event. A clean
WebSocket close alone is insufficient because failed core execution can close
the same way. Adapter tests cover both close ordering and explicit error
precedence with synthetic events.

`FIXTURE_DIALOG` in control fixtures is replaced by the test's native ID.
These are source-derived fixtures, not captures of a carrier test. Tests of the
adapter use an independent local HTTP/WebSocket gateway. Actual pinned-process
SIP evidence, when available, is recorded separately in the execution report.
