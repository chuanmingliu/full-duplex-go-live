# Re-engaging a silent caller

Active Call can ask whether the caller is still there after a randomized quiet
interval. The original GoLive prompt, voice, greeting, preset openers, speaker
protection and quotation calculation delay remain in use. No small LLM is needed
for this feature: the assistant speaks one of the configured literal templates.

## Leads configuration

Open **Leads → Configs → silence-reaction**. The existing versioned JSON editor
controls both the timing and the wording:

```json
{
  "enabled": true,
  "min_wait_ms": 8000,
  "max_wait_ms": 12000,
  "max_attempts": 2,
  "templates": [
    "您好，您还在听吗？",
    "您在吗？如果有什么问题，可以直接告诉我。"
  ]
}
```

The engine samples a uniform integer delay between the two bounds, once per
quiet interval. At expiry it randomly picks a template. These are complete
spoken lines, not Jinja templates or instructions to the model. The allowed
interval is 1–120 seconds; a config may contain 1–10 nonempty templates of up to
120 characters and allow 1–3 reminders per unanswered sequence. Equal interval
bounds give a fixed delay, useful for testing.

Settings are fetched with the existing tools bearer token at call setup and
pinned for that call. Save a new version to affect subsequent calls. The feature
uses the original profile's `tools_end_call` / `hangup_rules` integration
and tools base URL; no new transport or profile override is needed. A missing,
invalid or unavailable config disables reminders for that call. This permits an
engine upgrade before the Leads service upgrade without changing old behavior.

New Leads installations, and existing stores missing this config, seed the
example above. An existing version is never overwritten. To roll back for new
calls, save `{"enabled": false}`. Existing calls keep their snapshot.

## Timing and priority

1. Wait until the greeting has finished or caller activity has occurred; do not
   treat a newly created, unanswered session as an invitation to speak.
2. Start the randomized interval only while the conversation is quiet. Caller
   speech (including pre-VAD candidates), pending recognition, a held interruption,
   model generation, playback/completion receipts, delegated replies, tool work,
   queued results and acknowledgments all postpone the reminder.
3. A busy interval cancels the deadline. A full new interval starts when it ends.
   Quotation calculation therefore does not count as caller silence for reminders.
4. Existing hangup rules run first. If a hangup is due, no reminder is started.
   Ready tool results and deferred conversation work also run before reminders.
5. Speak through the normal preset speech pipeline. The line appears in the
   assistant transcript and output audio, is included in conversation history,
   and can be interrupted through the existing echo/background-aware controller.
6. After each reminder finishes, wait another random interval, up to
   `max_attempts`. Accepted caller turns reset this budget. Raw noise and the
   assistant's own reminders do not replenish it.

The scheduler runs on the existing 120 ms engine tick; TTS startup and transport
playout add time after the selected interval. A reminder is not a normal response
to caller speech and must not be counted as an improved normal-response latency
sample. Its log records attempt, chosen interval, template index and config
version without copying customer data into a new diagnostic event.

The existing silence-hangup clock restarts after assistant playback, including
reminders. With the standard 25-second silence rule, that means up to two
reminders and then 25 seconds of quiet after the last one, plus playback time.
A shorter hangup threshold can win before any reminder. Duration limits and all
hangup rule values are unchanged. This feature does not alter quotation timing
or claim that the caller actually heard a reminder at the handset.

## Verification and rollout

```sh
go test -timeout 25m ./internal/tools ./internal/crm ./internal/duplex
go test -race ./internal/tools ./internal/crm ./internal/duplex \
  -run 'SilenceReminder|SilenceReaction|UnavailableSilence|SilenceHangup'
```

Coverage includes config validation/authentication/versioning, bounded sampled
deadlines, activity and quotation suppression, attempt limits, hangup priority,
missing-service fallback, real engine/player simulation of transcript and audio,
and a caller interrupting the reminder. These simulations do not dial a phone.

Local verification on 2026-11-06: tools and CRM package suites passed; the duplex
suite hit the default 10-minute test timeout without an assertion failure, then
all 14 remaining tests passed in a separate run with `-timeout 25m`. Focused
feature tests passed with the race detector. The interruption simulation passed
caller, background, echo and acknowledgment controls. Both binaries built;
`go vet`, UI JavaScript syntax validation, 62 latency-analyzer tests and 10
simulator-retrospective tests passed. These results do not constitute deployment
or real-call verification.

Roll out only after CI passes and the current call is idle, following
[CALL-OPERATIONS.md](../../CALL-OPERATIONS.md). Both the GoLive engine and Leads
CRM need this code for automatic config seeding and validation. Keep the prior
binaries and original configuration for rollback. Do not restart Active Call,
RustPBX or an active session merely to enable this feature.

Implementation: `internal/tools/silence.go` (schema and authenticated fetch),
`internal/crm/service.go` (seed/validation), `internal/crm/ui/app.js` (editor help),
and `internal/duplex/silence.go` (per-call scheduler).
