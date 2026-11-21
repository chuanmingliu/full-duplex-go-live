---
name: golive-call
description: Place an explicitly requested original GoLive Active Call or simulator call through the GoLive MCP plugin, retain the call ID, and record both audio directions in Call Journal. Use for GoLive call commands, not generic phone calls or Speko calls.
---

# GoLive calls

Read `golive://operations` when establishing a session. The active optimization
branch is `codex/telephony-latency`, using Active Call and the original GoLive
configuration. The SIP/RTP replacement is parked.

## Route the owner's words

- `call` means one real call through Active Call to the saved recipient.
- `call sim` means the original simulator. Choose a deployed scenario matching
  the owner's task. No real recipient is dialed.
- `call gpt-live-1` selects the separate Speko path. This plugin does not provide
  that route. Use its separately configured integration if available; otherwise
  explain that it is unavailable. Never substitute a plain call.
- `done` authorizes review of the selected call, not another call.

## Place and follow one call

1. Queue `golive_get_status`, then poll `golive_get_operation`. Inspect the
   baseline, original preset policy, Aria endpoint, health and idle state.
2. Generate one request UUID for this attempt. Call `golive_place_call` with
   that UUID and the returned `state_sha256`. Use `active_call` with no scenario
   for a real call, or `sim` with a deployed scenario name from
   `golive_list_scenarios` for a simulation.
   The host must have enabled the corresponding mode in its local config.
   The user's explicit call request is authorization; do not ask again merely
   because a tool has side effects.
3. Poll the same request UUID. Check `result.ok` and `data.stage`. Only
   `dial_accepted` establishes acceptance. Retain `call_id` and any simulator
   `run_id` in the conversation. Transport completion does not mean call ended.
4. For a real call, wait for the owner's `done`. Status/recording availability
   can be checked while waiting. Keep deep analysis for after that signal.
5. Use the `golive-review` skill to verify the permanent archive and report timing.

Never manufacture a new request UUID to retry a timed-out or unknown dial.
Inspect the same receipt and Aria first. A deliberate later retry requires the
owner's instruction. A runner queue that is busy is a reason to wait.

Keep prompt, greeting, voice and recipient private. The MCP tools expose hashes
and counts; open the private Call Journal links only when the task needs them.
Preserve existing services and active calls. This plugin never restarts them.
