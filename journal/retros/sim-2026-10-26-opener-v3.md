# Simulated runs 2026-10-26 10:00–10:37 — the opener's third round

Four `kb-coverage-nerd` / `kb-process-struggler` conversations through the
PBX while the opener's rules were finished. The simulated customer pauses
~1.1 s inside most of its sentences, so it is the hard case for "a pause
inside a sentence": 11–16 of every ~25 openers become backchannels.

| time | build | what | first audio p50/p95 | content p50/p95 | cuts | turns without an opener |
| --- | --- | --- | --- | --- | ---: | ---: |
| 10:00 | 2bf836a | opener at the base pause during patience | 768 / 1369 | 949 / 1369 | 10 | many: a backchanneled opener left the player "active" with nothing to play, so the continuation found the floor busy |
| 10:07 | 78fc3ab | skip only on fragments under 1 s | 380 / 1264 | 950 / 1264 | 6 | 10 (same cause) |
| 10:20 | ed45794 | `FinishOpenerAsBackchannel` (grace on the next mark) | 380 / 991 | 910 / 999 | 4 | 10 (the opener's mark had already passed; grace never resolved) |
| 10:31 | 9158f10 | end the turn once the opener's audio is out; fragments by voiced ms | **380 / 1041** | **932 / 1041** | **1** | 5 (4 fragments under 1 s, by design) |

What stands at 9158f10: an opener that turns out to be mid-sentence finishes
as 嗯, nothing of the guessed answer follows, no cut (1 in 27 openers, down
from 10), the continuation opens unless it was a fragment under a second of
voice, and the opener comes at the base pause even when the turn is given a
patience wait. The content figure on this pause-heavy script is ~930 p50 /
~1040 p95 in the 10:00 hour; the live 09:31 call before these changes was
839 / 1077 with the over-1000 turns on patience waits, which this round
addresses — to be measured on the next live call.
