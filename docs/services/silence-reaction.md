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
  "rounds": [
    {
      "min_wait_ms": 8000,
      "max_wait_ms": 12000,
      "templates": ["您好，您还在听吗？"]
    },
    {
      "min_wait_ms": 8000,
      "max_wait_ms": 12000,
      "templates": ["您在吗？如果有什么问题，可以直接告诉我。"]
    }
  ]
}
```

`rounds` is an ordered sequence of 1–3 reminders. Each round has its own uniform
integer waiting range (1–120 seconds) and pool of 1–10 literal spoken lines
(up to 120 characters). Templates must be distinct within and across rounds,
so continued silence cannot repeat the same configured line. Selection within
a round is random. Equal wait bounds make the interval fixed.

After each reminder finishes, the next round starts its own full waiting interval.
The final round exhausts the sequence; it never loops. An accepted caller turn
resets it to round one. Noise, echo, rejected background speech and the assistant's
own speech do not reset the sequence. Existing hangup rules still have priority,
so a configured round can be preempted by a shorter silence/duration limit.

Legacy configs with top-level `min_wait_ms`, `max_wait_ms`, `max_attempts` and
`templates` remain supported. Their random selection now avoids the immediately
previous line when another distinct line exists; a one-line legacy pool still
repeats. Do not mix these fields with `rounds`. An invalid save is rejected without
changing the stored config/version. To upgrade an existing pool, create an ordered
round for each attempt, preserving the owner's timing and assigning distinct lines.
Do not silently overwrite an existing Leads config with the new default.

Settings are authenticated through the existing tools bearer token and fetched
at call startup. They remain pinned for that call; saves affect subsequent calls.
The original `tools_end_call` / `hangup_rules` integration and tools base URL are
unchanged. Missing, invalid or unavailable config disables reminders.

New stores missing this config seed the two-round example above. Existing stores
keep their current version. `{"enabled": false}` disables reminders for new calls.
Before rolling the engine back to a build that predates `rounds`, restore the saved
legacy config while idle; that older parser cannot execute the round schema.

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
   the configured number of rounds (or legacy `max_attempts`). Accepted caller turns reset this budget. Raw noise and the
   assistant's own reminders do not replenish it.

The scheduler runs on the existing 120 ms engine tick; TTS startup and transport
playout add time after the selected interval. A reminder is not a normal response
to caller speech and must not be counted as an improved normal-response latency
sample. Its log records attempt, chosen interval, template index and config
version and round number without copying customer data into a new diagnostic event.

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

Deployment and subsequent testing: [2026-11-06 box release and simulator results](../telephony/2026-11-06-silence-reaction.md). The reminder probe passed behavior checks after a separate settings postcheck; the normal-response latency gate remains unmet.

The multi-round follow-up adds validation for duplicate/mixed schemas, legacy compatibility, round-specific deadlines, caller reset, and an engine/player simulation that emits two distinct recorded reminders in order.
