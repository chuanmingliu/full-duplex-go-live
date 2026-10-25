# Retrospective: every turn so far, and what 1000 ms would take

Written 2026-10-22 from the 45 retrospectives in `journal/retros/`, the
pooled tables in CLAUDE.md and the two benchmark records, covering the
calls from 10-11 to 10-21 — roughly 1,100 answered turns on 24 builds, real
and simulated, quiet rooms and a metro platform. The owner's question: the
target is to move from **p95 ≤ 1200 ms** to **p95 ≤ 1000 ms**, caller's
last sound → first audio; what has been holding the latency, and what
would have to change. Figures are the retros' own; the pass over the
archive's per-turn records (`call.json`) that would replace estimates with
counts is listed at the end.

## 1. Where the ~880 ms of an ordinary turn go

Medians from the caller's last sound, quiet line, an ordinary hour (the
10-13 21:15 call, 47 turns; the 10-13 15:37 call; the 10-21 calls agree
within ±30 ms):

| stage | ms | cumulative | who |
| --- | ---: | ---: | --- |
| wait before the final is asked for (`early_final_ms`) | 120 | 120 | golive |
| the final comes back | 95 | 215 | recognizer |
| chat model: request → response headers | 120 | 335 | network + vendor |
| chat model: headers → first token (fastest of 8 copies) | 240 | 575 | vendor |
| the first clause collected (≥4 chars, ≤160 ms deadline) | 105 | 680 | model's token rate |
| synthesizer: first audio of the first clause | 200 | 880 | vendor |
| first audio → the wire | 0 | 880 | golive |

Two things follow and both matter for 1000:

- **The pause (`min_silence_ms` 380) is not on the path.** The answer is
  begun at ~215 ms (the early final) and is ready at ~880; the turn closes at
  380. Lowering the pause buys nothing until the answer is ready before
  380 — which it never is. Lowering `early_final_ms` 120 → 100 buys 20 ms
  and splits ~18% more recognitions (measured on 51 pauses, 10-12).
- **golive's own waiting is ~225 ms of the 880; three vendors' round trips
  are the rest.** There is nothing left to tidy on the path itself: the
  recognizer gets 20 ms frames as they arrive, the synthesizer one task per
  segment on a warm connection, the detector has no hangover (CLAUDE.md,
  "Where a turn's time goes now").

So a p95 of 1000 leaves 120 ms above the median for everything that
varies. Today's p95 on the good calls is 1045–1160 (47, 42, 25-turn calls);
the variation is in sections 2–4.

## 2. What put turns over the line: every cause seen, by weight

Over the 45 retros, roughly 70 turns were over 1200 on calls with a
figure. By cause (the retro's own attribution; several turns have two):

| cause | share | typical excess | issue | state |
| --- | ---: | ---: | --- | --- |
| chat model first token slow (a slow spell, or the 23:00 hour) | ~55% | +200–500 | ISS-018 | open — vendor |
| synthesizer first audio 400–760 ms, or a 3–8 s stall | ~20% | +200–600 (stall +3000) | ISS-017 | mitigated (pool, Fish fallback) |
| a patience / settled-close wait on a sentence said in pieces | ~12% | +600–900 | by design | tunable |
| a long first clause (600–700 ms after the first token) | ~5% | +500 | — | not addressed |
| short turn answered from nothing (no head start) | ~5% (before 10-12) | +400 | ISS-016 | fixed |
| noise: hold judged late, hypothesis kept growing | ~3% | +300–3000 | ISS-033/034/049/050 | fixed on quiet lines |

And the one that is not a turn cause but an hour: **between about 23:00 and
midnight the chat model's first token is 600–780 ms at the median for every
copy on every key** (10-11, 10-12, 10-13, 10-16, 10-19: five nights of
five). On those calls 7–12 of 13 turns miss 1200; at 1000 every turn would.
Daytime slow spells of the same shape were seen on 10-14 at 15:37 and
10-20 at 10:00.

## 3. The levers, ranked by what they are worth at p95

What follows is ordered by expected gain at p95, not by ease. The first
three are the ones that reach 1000; the rest are tens of milliseconds.

### 3.1 Open from the phrase cache on most turns — *the only lever that beats both vendor tails*

A turn whose first clause is already in the cache plays it from memory:
no synthesis, and the chat model's first token is off the path of the
first audio. Measured: 515–756 ms from last sound (10-11 23:08), 569–831
(10-13 21:15, 13 turns), 584 (10-17 12:00). Today it happens by chance to
one turn in eight. Asking the model to open every answer with one of a
few short clauses — "好的，", "明白，", "您说的对，", "这个我帮您看一下，" —
makes it most turns. Expected: p50 ~650, p95 ~900, and the tails of
sections 2.1 and 2.2 stop reaching first audio (they move to the *second*
segment, where a 200 ms stall is a brief pause mid-answer, not dead air).

Costs: the assistant's voice changes — every answer opens with an
acknowledgement; the owner's prompt decides what the assistant says and
this is a change to it, so it is the owner's (decision 15). Cheap to try:
one line in `instructions_append`, no build; measure on two calls.

Also unlocked by it: with first audio at ~215 + 200, the pause becomes the
binding term and `min_silence_ms` 380 → 300 is worth 80 ms a turn.

### 3.2 Race the copies on the first clause, not the first token

The hedge picks the copy that produces its first token first
(`internal/provider/hedge.go`). The clause the synthesizer needs is ≥4
characters, and a copy fast to its first token is not always fast to its
fourth: the 701 and 622 ms first clauses (10-20 19:32, 10-16 23:28) were
winners that then trickled. Pick the copy that reaches
`stream_first_chunk_chars` first instead. Expected: −30 to −60 ms at p90
on the chat-model term, removes the long-first-clause cause outright.
Measure with `phone-brain.sh llm -pool` extended to report time to the
fourth character per copy. Small change, a test, no decision.

### 3.3 A second chat-model vendor, raced — the hour

Decision 1, unchanged: no candidate measured so far is faster than
DeepSeek with the 13k prompt (SiliconFlow/OpenRouter Qwen 1.0–1.6 s;
MiniMax-M3 level on a bad night, behind on a mild one). Two things not yet
tried: a *shorter prompt* for the second vendor (the lead service renders
the template; a 3k summary variant of it for the race partner would take
the long-prompt penalty away from M3 and Qwen), and DeepSeek's own
`deepseek-chat` through a second region/account at the hour (not measured
— the eight copies were one vendor, two accounts). Without this, 1000 is a
daytime target; the owner should decide whether the 23:00 hour is in scope.

### 3.4 Patience and the settled close: shorter where the words are complete

`patient_silence_ms` 700 and `settled_close_ms` 600 wait on a hypothesis
that reads as unfinished. Four of nine turns waited 600 on 10-19 09:37;
none on the call after. The wait is the price of not cutting a sentence in
two, and `speculate_while_patient` already prepares the answer during it,
so the cost is the wait alone. Two refinements worth measuring on the
archive before building: (a) 500 ms patience when the hypothesis ends on
a particle or a question mark and the chat model's answer is already
prepared; (b) the adaptive version the owner proposed on 10-19 — the
pause and patience thresholds learned per situation from calls (quiet
room vs metro). Expected: moves the ~12% of over-target turns down by
200–400 ms each; does not reach them all.

### 3.5 The synthesizer's 400+ ms openings on short clauses

Recurs (413, 320, 755 ms on 10-17 and 10-20): the pool races both accounts
on the first segment and Fish takes over at 320 ms, but Fish's own first
audio is ~420, so a 755 becomes ~740 at best. Two cheaper options: a
third MiniMax account in the pool (first-audio tails on the two accounts
have not coincided in 30 rounds), or the vendor's HTTP endpoint as the
second route (untried). −100 to −200 ms on ~5% of turns.

### 3.6 Small, certain, and not enough on their own

- `early_final_ms` 120 → 100: −20 ms every turn, +18% split recognitions.
- Keep the chat model's prefix cache warm across calls (it is warmed at
  session open; the first turn of each call is still 100–300 ms slower).
- Wired egress for the box instead of the VPN client's tunnel over Wi-Fi:
  tens of ms per vendor round trip, three round trips a turn; needs root.

## 4. What the phone leg adds, which nobody has measured

Both definitions stop at golive's wire. Active Call, RustPBX, the GSM
gateway and the carrier add their own time both ways; the echo estimator
cannot measure it on this line (ISS-022/023). A recording beside the
handset lined up against the box's is still the way. It does not change
the ranking above; it does change what "1000 ms" means to the caller.

## 5. What the numbers say about 1000

- Daytime, quiet line, today's build: p95 1045–1160 on the three calls
  long enough to say. 1000 is 50–160 ms away, which is more than any of
  3.2–3.6 gives alone and less than 3.1 gives.
- With 3.1 and 3.2: p50 ~650–700, p95 ~900 — met with margin, on the
  evidence of the cache-opened turns measured so far (n≈20; not a p95).
- The 23:00 hour: not met by anything in golive; 3.3 or out of scope.
- The metro: the noise fixes hold latency where it was (913/1022 p50 on
  10-20); 1000 at p95 there is harder than in a quiet room and needs 3.1.

Recommended order: 3.1 (a prompt line, two calls to measure), 3.2 (a
build), then 3.4 on the archive's data, with 3.3 as the owner's standing
decision. The journal's target moves to 1000 (`-target 1000` on the
journal, from this build) once the owner says so, so the overview starts
counting against it.

## 6. The pass that would replace the estimates

`~/golive-archive/calls/*/call.json` has every turn's stages. One script
over all of them (numbers only) gives: the pooled stage distributions
(p50/p90/p95 per stage), the share of turns over 1000 by binding stage,
how often the cache-opened turns beat 1000, the patience-wait count and
what each cost, and the hour effect as a curve. Run it when the box is
linked; its output goes into the journal's Benchmarks tab as an L3 record
per build with `-source phone`.

## 7. What happened on 10-25 (the owner set the goal: `/goal 1000`)

Measured on the box bench, 20 turns a run, 21:05–22:06, same chat-model hour,
caller's last sound → first audio:

| run | what | p50 | p95 | notes |
| --- | --- | ---: | ---: | --- |
| A | d23fcb2, first-token race (as deployed) | 888 | 1028 | first token 265 + clause wait 108 |
| B | same, **first-clause race** (3.2, `llm_hedge_clause_chars: 4`) | 891 | 1118 | lead 314 + clause wait 31: −28 ms at the median; the p95 is one turn at n=20 |
| C | openers **chosen by the model** (3.1 as proposed: a prompt line) | 733 | 1031 | 17 of 22 turns from the cache, synthesizer term 0 — and the p95 unmoved: the opener still waited for the model's first clause |
| D | openers **said by the engine at the pause** (`opener: true`), warm key broken | 897 | 1104 | 4 of 24 opened: ISS-057, the warmed phrases keyed under the wrong provider name — every warmed phrase since 10-11 (greeting, prehook) was synthesized on its first use |
| D2 | the same, ISS-057 fixed (af3baab) | **380** | **380** | 22 of 22 opened at the pause; the model's own 好的 dropped once |
| E/E2 | baseline again, same minutes | 881 / 850 | 1127 / 1077 | |
| F | de62d5f, the profile as deployed (opener on) | **380** | **380** | max 422; **the answer's own first word p50 902 / p95 1076** (`first_content_ms`) |

So: the headline figure, as the owner defined it (first audio), is at the pause
and the pause is now the whole of it — 380 at p95, and `min_silence_ms` is
again a lever (300 would give 300). What the caller hears is an acknowledgement
at the pause and the answer ~500 ms later, where it always was; the content
figure is reported beside the headline from now on (`first_content_ms`,
`content_p95_ms`) so nobody mistakes one for the other. The content figure
still has the chat model's tail in it; levers 3.2–3.5 apply to it, and the
opener makes a synthesizer stall a pause after a word instead of dead air.

Open: the owner's choice of phrases (three now; one neutral 嗯 may suit a
question better than 好的), and a real call — the bench's turns are five
fixed sentences, not a conversation.
