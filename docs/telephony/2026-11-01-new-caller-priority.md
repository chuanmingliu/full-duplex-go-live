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
old acknowledgment cleanup and existing delivery guards. The later recorded SIP test below exercises the guard five times. Full CI
[41281964710](https://github.com/chuanmingliu/full-duplex-go-live/actions/runs/41281964710)
passes for exact source `7fc4237`; no handset speedup is claimed from unit tests.

## Recorded validation

The follow-up 90-second SIP trial exercises the new guard five times.
Conversational p95 is 988 ms across six responses; quote readout p95 is
6341 ms across five verified results. The strict gate still fails for the
small sample and one unverified chained result/readout. See
[the full retrospective](../../journal/retros/sim-newcaller-16c41c8a-a5b5-413d-ab81-1377d5891a14.md).

## Deployment

Native SIP test engine 9895 now runs `7fc4237`, verified at 14:55:38 on Nov 1.
Full CI, readiness and no-dial adapter preflight pass. The prior `f12b6f5`
binary is retained for rollback. Original GoLive 8765, saved settings, quote
delay, native Journal and Active Call remain intact. The running adapter is
still `fb11e2c`; this release changes its GoLive engine, not SIP/RTP code.
See [the exact operation and receipt](../../scripts/local-ops/recorded/2026-11-01-native-7fc4237/).
