# Call out-mvf63uuu-0iln — 2026-10-19 19:25, build c3695fe

A real call from a metro station, 63 s, evening. The owner: the last answer
was cut by a station announcement, not by them.

## What golive measured

Last sound → first audio, 6 turns with audio: 942, 964, 988, 994, 1085,
1118 — p50 **991**, max 1118, **0 over 1200** (console's definition p50
~600). Chat model first token 251–361 ms, synthesizer 170–250. Three of
the six answers were cut.

## What went wrong: the caller was never enrolled

`focus: this call … judged=0 … primary_speaker=-1`. Every sound over the
answer (four) was judged "no primary speaker known yet" and yielded on the
recognizer's words alone — the behaviour before the focus channel existed.
So at 44.9 s the station announcement (4 characters of transcript) cut a
3.9 s answer, two guesses were made and abandoned on its "words", and the
assistant then answered the announcement (item_11, 1085 ms).

Why no enrolment: an utterance was offered only if it was clean from its
first millisecond — not an interruption, and not within 600 ms of the
assistant's last word (its echo tail). This caller answered within ~0.9 s
of the greeting and interrupted four of six answers; the two utterances
that were neither were 240 ms and 600 ms of voice. Six candidates, none
qualified, and the labels had nothing to compare against.

Fix (ISS-044, next build): the *clean tail* of an utterance is offered —
from the end of the assistant's voice and its echo tail to the utterance's
end, if that carries 600 ms of voice. An interruption that goes on past the
cut is a caller's voice like any other. Test:
`TestTheCleanTailOfAnInterruptionIsOfferedForEnrolment` (the 3 s
interruption is offered as its last 2.06 s).

With an enrolment, the announcement at 44.9 s would have been judged
against the caller's voice within the 900 ms hold and the answer kept, as
the 10-15 background-voice calls showed for a television.

## Also

- `quiet_closes=0` — nothing for the caller-quiet close to do without an
  enrolment (it needs the utterance attributed to the caller first).
- The greeting's `first_audio_out_ms=0` (item_1): the opening line was
  played from the phrase cache.
- `end_call` not asked for (no goodbye).

## Open

- Verify on the next call: `focus: offering the caller's voice … trimmed_ms`
  with `barge_in=true`, an enrolment within the first two exchanges, and
  announcements/other voices judged `keep`.
- The owner's idea (19:29): thresholds (floor margin, barge-in margin,
  pause, quiet-close) adapted to the situation and learned from calls — see
  the desk / CLAUDE.md "What to work on".
