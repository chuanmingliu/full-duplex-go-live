# Call out-mvkb3411-s889 — 2026-10-23 09:43, build 059ae48

A real call from a metro station platform (announcements over the line), 20
s, the kept prompt (12,840 characters). The owner: "debug this call…
background noise sometimes interrupted the call."

## What happened, by the clock

- 34.1 answered, greeting (first audio 253 ms); the caller spoke over it
  (0.6 s, no turn).
- 41.9 the caller's question, 1.8 s; answered on a guess (first token 468,
  synthesizer 225 ms). The caller's voice enrolled from this one utterance
  (1.2 s, tentative).
- 46.4 a sound over the answer, exactly 320 ms (the echo warning fired):
  the labels said 92% another voice, 100 ms the caller — three points short
  of the veto share, under the bar for "the caller spoke". The hold waited
  its 900 ms out "undecided" and **yielded**: the answer cut at 3.6 s
  (ISS-052).
- 47.9 an utterance opened and was judged another voice's (100%) for 2.4 s
  with the assistant silent — an announcement. At 50.3 the caller's voice
  joined (200 ms) and the engine took the utterance up; 420 ms later the
  quiet close took the turn. Its transcript, 91% the announcement's words:
  seven garbled characters (ISS-053).
- 51.4 the model read them as a refusal and called `end_call` under
  "refused twice", 17 s into the call, on the caller's first line
  (ISS-054). The farewell played whole; the caller spoke over it twice
  (verdict: the caller's voice; ignored by design, ISS-046); hangup 54.4.

## Latency

One measurable turn: 834 ms last sound → first audio (first token 502,
the morning). Nothing to pool.

## Fixed in the next build

- ISS-052: a hold that waits out with the labels saying mostly another
  voice keeps the floor.
- ISS-053: the quiet close lets an utterance go by when the caller's share
  of its voice is under half, and does not take it up again.
- ISS-054: hangup rules take `min_turns`; `refused_twice` needs two turns
  of the caller; the engine refuses the model's `end_call` under it and
  the goodbye net stands down for the refused farewell. The box's rules
  go to v3 with it.

## Open

- The echo speaker was never identified (the call was too short, the
  caller talked over the greeting); on a longer call the echo masks would
  have read the 46.4 sound through. The enrolment was tentative on 1.2 s
  the whole call.
- Announcements still open utterances and hold the gate; what changed is
  that they no longer cut answers or become turns. The caller's words said
  inside an announcement are lost with it.
