# Call out-mvhv0189-xw9u — 2026-10-21 16:37, build aad9132

A real call, 84 s, quiet room; the first on ISS-050's fix. The owner:
"why did the AI hang up unexpectedly?"

## The hangup — the silence rule, counted wrongly (ISS-051)

The caller's last word was at 16:38:34. The assistant then answered at
length — 12.8 s and 7.2 s of audio, back to back, to 16:38:58. The silence
rule (25 s without a word from the caller) ran its clock from the caller's
last word alone, so it fired at 16:38:59 — the moment the assistant stopped
talking — said its farewell, and hung up. The caller answered the farewell
("喂？") and that cut it too: a speculative turn begun on the reply became
the current turn and aborted the farewell's speech (ISS-046's guard was on
the yield, not on the turn's creation).

Fixes (next build): the silence clock runs from whoever spoke last — the
caller's last word or the assistant's last — so an answer is never the
caller's silence; and while the call is ending no turn is begun at all,
so nothing aborts the farewell. Tests:
`TestTheSilenceRuleDoesNotCountTheAssistantsOwnAnswer`,
`TestTheCallerSpeakingOverTheSilenceFarewellDoesNotCutIt` (both fail
without the fixes).

## ISS-050 — verified

`quiet_closes=0` on a quiet line (5 judged, 2 barge-ins, 0 vetoed). The
other session's fix holds: the close no longer fires in the caller's own
pauses.

## Also

The quotation ran: three asks, the intake and a total (the journal's
Quotation section has the steps); the model made repeated tool calls in
one turn (items 7–8, 11–14), folded by 76297c9's rule.
