# Call out-mvg1bnk3-ke09 — 2026-10-20 09:59, build 479482a

A real call, 118 s, morning; the owner said goodbye and "the hangup was
postponed" — it never came; he hung up at 10:01:03.

## Why

The model answered the goodbye with 再见 twice (item_13, 12 characters;
item_14, 6) and called no tool: `tool_calls=0` on every turn of the call.
The hangup existed only as `end_call`, which depends on the model
remembering it in the same breath as its farewell. The whole-farewell fix
of the build before (ISS-046) never came into play.

Fix (ISS-047, next build): an `assistant` hangup rule — when a reply the
assistant has just finished saying matches 再见|拜拜|不打扰您了, the call
ends by the same path as `end_call`. In the default set and added to the
box's `hangup-rules` (v2). Test: `TestAGoodbyeSaidWithoutTheToolStillEndsTheCall`.

## What golive measured

9 turns with audio; console's definition p50 565, p95 879, max 1019; last
sound → first audio on the measured turns 824–1399 (one over: a 1019 ms
console figure, chat model 822 ms first token at 10:00:11). Focus: 13
judged, 0 vetoed; 2 barge-ins; 2 quiet closes. Quiet room.

## Open

- Next goodbye call: `hangup: the assistant said goodbye; ending the call`
  then `the gateway was told to hang up status=200`.
