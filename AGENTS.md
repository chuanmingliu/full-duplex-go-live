# Call command routing

The owner's chat commands select a mode for that invocation, not a sticky default:

- `call`: one real call using the original GoLive configuration and saved prompt,
  greeting, voice, and configured recipient.
- `call sim`: a simulator call using the original GoLive simulation workflow;
  do not dial the real recipient.
- `call gpt-live-1`: one real call using the dedicated Speko `gpt-live-1` bridge
  and its SIP adapter profile.

Follow the mode-specific launch and recording details in
[CALL-OPERATIONS.md](CALL-OPERATIONS.md#chat-command-modes).
Do not let a previous Speko call change what plain `call` means. A mode-selection
request does not itself authorize starting a call. Preserve any active call.
For either real mode, retain its call ID, record chat and audio, and wait for the
owner's `done` before deep analysis. `done` is not a command to redial.
