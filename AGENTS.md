# Branch focus and call routing

`codex/telephony-latency` is the active optimization branch. Its phone transport
is Active Call with the original GoLive configuration. SIP/RTP replacement
exploration is parked on `codex/sip-rtp-adapter`; do not import its deployment
overrides or silently route calls through it.

- `call`: one real call through Active Call, using the original saved GoLive
  prompt, greeting, voice and configured recipient.
- `call sim`: the original simulator workflow, without dialing the real recipient.
- `call gpt-live-1`: the explicitly selected, separate Speko path. Its setup stays
  on the experimental branch; never pass this mode to the plain-call script or
  silently fall back to a different real-call route.

Use [CALL-OPERATIONS.md](CALL-OPERATIONS.md) and the existing Mac runner for box
operations. Preserve active calls and existing services. Record chat and both
audio directions, retain the call ID, and wait for the owner's `done` before deep
analysis. `done` does not authorize redialing. Protect credentials and customer
data from logs, commits and reports.

Keep the original scenario/preset openers as the conversational baseline. Leave
Talker-generated openers disabled; preserve the coordinated opener/answer flow.

Prioritize normal response and interruption latency. Report quotation
calculation and result completion separately; preserve its configured delay.
Keep server-output measurements, the owner's optional 50 ms handset estimate,
and actual acoustic handset measurements explicitly distinct.

# Commit attribution

Keep the configured human author. For work performed by Codex, add:

```text
Co-authored-by: Codex <noreply@openai.com>
```

Do not rewrite published history or attribute unrelated work to Codex.
