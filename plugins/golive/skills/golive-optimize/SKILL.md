---
name: golive-optimize
description: Optimize normal response, interruption, or guarded backchannel latency in the GoLive Active Call codebase using simulator-first comparisons and preserved original settings. Use for implementation work, not routine call placement.
---

# Optimize Active Call

This skill needs the GoLive source checkout in addition to the plugin. Locate
the user's existing `full-duplex-go-live` checkout and read its `AGENTS.md`,
`CALL-OPERATIONS.md`, and `scripts/local-ops/README.md` before edits or deployment.
If the checkout is unavailable, complete status/evidence work with MCP and ask
for its location. Historical operation scripts are evidence, not reusable jobs.

Work on `codex/telephony-latency`. Keep the original Active Call transport,
prompt, greeting, voice, scenario/preset openers and coordinated answer flow.
Do not import SIP/RTP adapter or Speko deployment overrides. Keep Talker-generated
openers disabled. Keep echo, background-speaker and acknowledgment rejection.
The optional Pipecat detector assists the original ASR/focus decisions; it does
not independently cut playback, and failures retain legacy behavior.

Use `golive_get_status` to bind runtime/configuration hashes. Capture baseline
and candidate simulations with the same scenario and original saved context.
`golive_place_call(mode="sim")` runs one simulation and preserves its receipt.
Collect ended-call evidence through `golive_analyze_call`. Compare server normal
first-content and interruption distributions with sample counts. Keep quotation
calculation/readout separate and preserve its intentional calculation delay.

Implement the narrow change indicated by evidence, add behavior-level regression
tests, and run relevant checks and the repository's required CI. A small synthetic
pass is not a real-call latency claim. Preserve smooth phrasing and avoid counting
an earlier filler as a faster substantive answer.

For an authorized push, keep the human author and add

```text
Co-authored-by: Codex <noreply@openai.com>
```

Deployment uses the existing Mac runner and repository release workflow. The MCP
plugin intentionally has no arbitrary shell/deploy/restore endpoint. Bind the
actual running version, require idle state, preserve the current services and
rollback material, then verify the deployed binary and recording configuration.
A Git push does not deploy. Restore an owner's saved Aria version only when
requested. Place a real test call only when requested and wait for `done` before review.
