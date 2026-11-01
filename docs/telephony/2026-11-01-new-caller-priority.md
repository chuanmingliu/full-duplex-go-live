# An older ready result must allow a new caller answer to start

In the `302267b` SIP recording, caller turn item_5 closed at 14:29:23.211.
An old ready task t2 then cancelled that answer and began result turn item_6 at
14:29:23.253: only **42 ms later**. Another instance cancelled item_14 just
43 ms after its endpoint. Both occurred after ASR finalization, so the existing
caller-speaking/finalization guard had correctly ended, but the old task's
1500 ms queue deadline had already expired.

The result continuation then processed conversation context containing newer
caller input, and launched another task from a non-caller result turn. This
explains why merely inheriting the original task's caller timing would be
unsafe: the newer request may be the one actually being answered. The missing
origin remains unverified until the true relationship is explicit.

The scheduler now gives an unrelated newer caller answer the existing
configured delivery wait measured from its own recognized endpoint. Protection
ends earlier when that answer's playback completes. It never waits indefinitely
for a stalled model. A ready result explicitly linked to that same caller,
including a coalesced request, can proceed immediately under the other existing
guards. Scripted acknowledgments and live caller speech retain their guards.

Old acknowledgment bookkeeping is still cleared promptly. One diagnostic event
per task/newer caller records the protected window without logging text or
repeating on every tick. Provider, saved prompt, voice, endpoint settings and
the 2500–4500 ms quotation-service delay are unchanged. This prioritizes an
actual answer to a newer caller; quotation completion remains separately timed.

The regression fails on the previous code before output and after output starts.
The candidate's focused race suite passes in 23.213 seconds, covering timeout,
playback completion, configurable limits, same-caller direct/coalesced results,
old acknowledgment cleanup and existing delivery guards. Recorded SIP testing
and full CI remain required; no handset speedup is claimed from unit tests.
