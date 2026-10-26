# Call out-mvokg4m2-91lc — 2026-10-26 09:16, build 7da9e1c

A real call from a metro station, ~90 s, the owner: "ruined by the station
announcement and other background noise". Not a latency sample.

- 4 utterances closed in the whole call, 820–2520 ms long: the announcements
  ran into the caller's words inside one utterance, the hypotheses kept
  changing, and 8 speculations were missed/abandoned (4 held). 3 of the 4
  turns closed on a 600 ms patience wait.
- The focus channel judged nothing "another voice" this time (enrolment
  confirmed on 3 then 4 utterances): the noise reached the recognizer, not
  the labels — the ISS-033 shape (a line that is never quiet), not ISS-052's.
- 4 openers (行, 对, 好的, 嗯嗯), none became a backchannel; 1 answer cut.
  One opener's cached audio was 1000 ms (the others 400–490): a learned entry
  (`phrase_cache_learn_chars`) may have replaced the warmed one for the same
  words with a longer rendering; worth a look.
- Figures, for the record only: first audio from last sound p50 600 (the
  patience waits), content 784 / 1030 / 1182 / 1645.

No change from this call; the noise work (ISS-033/052/053/055/056) stands.
