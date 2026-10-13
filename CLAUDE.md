# Working on golive

Notes for whoever picks this up next. State as of 2026-10-11 midday, branch
`telephony-realtime` (PR #3, stacked on PR #2 `perf-and-live-parity`, stacked on
PR #1 `prod-hardening`).

## What this is

A Go service that speaks the gpt-live-1 protocol (`/v1/live`) and the OpenAI
Realtime protocol (`/v1/realtime`). There is no single full-duplex model behind
it: a streaming recognizer, a chat model and a synthesizer are run as a cascade
and made to behave like one. That simulation is the point of the project.

Providers in use: Tencent realtime ASR, DeepSeek (`deepseek-chat`), MiniMax
(`speech-2.8-turbo`, standard WebSocket endpoint).

## The goal

**End-to-end latency within 1200 ms at p95, on a phone call.** The owner said
so in as many words: the figure is a p95, not a median. This is the thing to
optimise; everything else is in service of it.

Two definitions are in use. Quote both, and say which.

- **Last sound → first audio**: the caller's last sound to the first audio
  leaving golive. `+pause` in `scripts/call-latency.sh`; what the bench
  measures client-side. This is what a caller waits through, less the phone leg.
- **Turn judged over → first audio**: the owner's latency console (test box,
  `:8768`) calls this the reply: `vad.speech_stopped → phone.audio_sent`. It is
  the `audio` column, and it does not include the pause that ended the turn.

The phone leg — Active Call, RustPBX, the GSM gateway, the carrier, both ways —
is on top of either and has not been measured.

## Where it stands

Real providers on the test box, no phone, 15 measured turns each
(`phone-brain.sh bench`), last sound → first audio:

| Build | What changed | p50 | p95 |
| --- | --- | ---: | ---: |
| d45e519 | first deploy | 1248 | |
| 9da7702 | pause 380 | 1159 | 1395 (p90) |
| 2e2457b | pause 280; a slow hour for the chat model | 1229 | 1572 |
| 42b5f54 | early final + two copies of each completion; pause 380 | **943** | 1244 |
| 42b5f54 | same, one copy per completion (minutes later) | 952 | 1431 |
| ea3a478 | no guess on trailing words while a final is pending | 978 | 1123 (max 1191) |
| b8883c6 | four copies; fixes from the 13:09 call; 30 turns | 922 | 1202 (max 1237) |
| dcf68a8 | shorter first clause; 30 turns | 940 | 1181 (max 1202) |
| ea5d26d | backend warmed at session open; fixes from the 14:22 call; 20 turns | 904 | 1118 (max 1184) |
| c2fbf96 | answer prepared during a mid-thought wait; fixes from the 18:34 call; 20 turns | 878 | 1067 (max 1262) |
| 779002e | fixes from the 20:18 call, with the first segment raced on two synthesis connections; 28 turns, 2 lost to the synthesizer's rate limit | 851 | 1011 (max 1020) |
| 507f334 | the same fixes with synthesis hedging off; 20 turns, none refused | 852 | 1024 (max 1048) |
| f374c41 | each copy of a completion on a connection of its own (one key); 20 turns | 840 | 1076 (max 1143) |

Stage medians of the 943 ms run, from the caller's last sound:

| final asked for | final back, answer begun | LLM first token | first clause to TTS | first audio |
| ---: | ---: | ---: | ---: | ---: |
| 120 | ~211 | +415 | +70 | +216 |

Phone calls through golive, last sound → first audio, from the log:

| When | Build | Turns | p50 | worst | over 1200 |
| --- | --- | ---: | ---: | ---: | ---: |
| 10-11 morning | before the early final, pause 380 | 12 | 1210 | 1334 (p90) | about half |
| 10-11 13:09 | ea3a478 | 9 | 862 | 1382 | 2 |
| 10-11 14:22 | dcf68a8, the owner's 13k-character prompt | 19 | 972 | 1561 | 3 |
| 10-11 18:34 | ea5d26d, same prompt | 15 | 926 | 1481 | 1 |
| 10-11 20:18 | c2fbf96, same prompt, 9 minutes | 70 | 932 | 1308 (p95 1266) | 5 |
| 10-11 21:51 | f374c41, same prompt, two chat-model accounts, 8 minutes | 52 | 882 | 1354 (**p95 1097**) | 2 |
| 10-11 22:21 | f374c41 again, 7 minutes | 41 | 933 | 2674 (p95 1344) | 5 |
| | **both calls on f374c41** | **93** | **900** | 2674 (**p95 1336**, p90 1140) | **7** |
| 10-11 23:08 | 4b029b3, same prompt, 7 minutes | 37 | 841 | 4603 (**p95 1058**, p90 982) | 1 |
| 10-11 23:34 | e517e3a (same engine), redialled from the box with `phone-brain.sh call`, 2 minutes | 15 | 943 | 1143 | 0 |
| | **both calls since 4b029b3** | **52** | | 4603 (**p95 1091**) | **1** |
| 10-12 09:24 | 9f2ae31 (eight copies), `call`, under a minute | 4 | 783 | 1029 | 0 |
| 10-12 09:34 | c8a8477 (same engine), `call`, 3 minutes | 21 | 806 | 1396 (p90 979) | 2 |
| | **both calls on eight copies** | **25** | | 1396 | **2** |

By the console's definition: 13:09 p50 482, worst 1002; 14:22 p50 592, p95
1181; 18:34 p50 546, p95 805; 20:18 p50 543, p95 861; 21:51 p50 494, p90 690,
p95 717; 22:21 p50 553, p95 964; both on f374c41 p50 518, p90 760, p95 956;
23:08 p50 461, p90 602, p95 678.

The 20:18 call was the first with enough turns for a p95 that means something:
1266 ms, 66 over. Of its five turns over 1200, three had the synthesizer's
first audio at 355–589 ms (median 182), one was a turn closed on stale
hypotheses 5 ms before the final for them arrived, one an answer thrown away
over a trailing "啊".

**The 21:51 call is the first to meet the goal: p95 1097 on 52 turns, 103 ms
inside.** p90 1070; 48 of the 52 turns were answered on a guess that held. Its
two turns over 1200 are one of each vendor's:

- 1354: the chat model. Every copy was slow at once, on both accounts: one
  lane's stream opened in 115 ms and then said nothing for 700 ms, one took
  814 ms to open and won, the two on the second account had not opened by
  then. More keys do not help with this; a second vendor would.
- 1336: the synthesizer's first audio at 613 ms (the other 51 turns: 152–239).

That is 2 in 52, about 4%, and p95 holds only while it stays under 5%. One
call does not show that: two slow turns in 52 is consistent with a true rate
anywhere from under 1% to about 13%. Pool the next calls on this build with it
before calling the goal met.

Also on that call: the console took all of golive's events (367 sent, none
refused) — the first call with the corrected event ids; no rate limit and no
refused key; the four lanes shared the work evenly and each copy won about a
quarter of the time (15/20/20/13); 23 replies were cut short by the caller
speaking, all of them on purpose (the owner said so).

**The 22:21 call, same build, missed: p95 1344 on 41 turns, five over. The
two together are 93 turns at p95 1336, seven over (7.5%). The goal is not met
on f374c41.** The second call's five had two causes:

- The synthesizer stalled for eight seconds at 22:26: first audio at 1284 and
  1763 ms on consecutive turns, and a second segment that had not arrived
  after 3.3 s — the silence in the middle of an answer that the caller said
  "嗯" into. The chat model and the recognizer were as usual in those seconds.
  Three more of its first audios were 350–491 ms.
- Four of the five were answers to "嗯。", begun from nothing when the pause
  ran out. Every turn without a head start on both calls — twelve of 93 — was
  a two or three character turn, turned away by `speculative_min_chars`.

Fixed in c9a0738 and the commit after it: short turns are begun on their early
final; opening clauses are remembered (`phrase_cache_learn_chars`); a second
synthesis connection is asked when the first is slow (`tts_hedge_after_ms`).
See below for what the last of these is and is not good for.

**The 23:08 call on 4b029b3: p95 1058 on 37 turns, one over.** All 37 turns
had an answer prepared (none "answered without speculating"); four ordinary
turns opened from the phrase cache ("好的，" twice, "您说，我听着。", "您别急，")
and were among the fastest of the call. The one slow turn was 4603 ms: the
synthesizer stalled for four seconds on both connections — the second, asked
after 260 ms, had nothing for 3.7 s and both spoke at the same moment. On the
segment after it the first connection stalled again and the second won (611 ms
against over 1.4 s), the only time it has. That is a stall of three to eight
seconds once in each of three runs (22:26, the 22:44 probe, 23:09). 37 turns is
too few to call the goal met; pool further calls on this build.

A five-minute probe afterwards (40 turns paced like a call, with a TLS
handshake a second to the synthesizer's host and to the chat model's) saw no
stall: worst first audio 624 ms, every handshake under 160 ms. So whether a
stall is the path to the vendor or the vendor is still not known; rerun it
(`.runner` job 063 is the recipe) until one is caught.

On fewer than ~40 turns p95 is the worst turn or the one before it; pool calls
made on one build.

## What was learned, so it is not learned again

- **The pause was being waited through for nothing.** The recognizer's partials
  trail the audio by 300 ms and more, so the words in hand during the pause were
  short of the sentence, the guess made on them was thrown away at the final,
  and the answer began a round trip after the turn closed. What the recognizer
  does do promptly is finish: told the audio has ended, the final is back in
  ~95 ms. `duplex.early_final_ms` ends the recognition 120 ms into the pause,
  begins the answer on that final, and opens a continuation to hear anything
  that follows. The pause length then stops mattering to latency until it
  exceeds the time the answer takes to prepare — choose it for turn-taking.
- **It also makes semantic endpointing work.** "你好，" followed by a 400 ms
  pause used to be taken as a turn, because "你好" had not arrived as a partial
  when the pause ran out. With the final in hand 215 ms in, the engine knows it
  is an opener and waits.
- **The chat model's first token is the largest term and is not one number**:
  170–880 ms for the same request within one run. `llm_hedge: 2` takes the
  faster of two. It bought ~70 ms at the median and ~190 ms at p95.
- **Copies of a completion are independent within a minute, not across
  them.** One copy is over 600 ms nearly half the time and two left one turn in
  eight slow, as predicted. Four should have left one in a hundred and left one
  in ten: the model has slow spells in which every copy is slow. Each of four
  copies wins about equally often. More copies will not fix the tail; a second,
  independent route to a model would.
- **A pool of keys, and what a second account was worth.** The owner will not
  give up the four copies and can supply chat-model keys freely, so each copy
  now goes down a *lane* — one key on one connection of its own
  (`internal/provider/deepseek/pool.go`, `provider.WithLane`). Before, all four
  were streams on one HTTP/2 connection on one account. Measured with
  `phone-brain.sh llm -pool -runs 40`, fastest copy of each round, first token:
  two accounts (two copies each) p50 319, p90 416, max 554; one account p50
  349, p90 504, max 605. Rounds with every copy over 550 ms: 1 of 40 against 2
  of 40. So a second account is worth something at the tail (~90 ms at p90 in
  that sample) and does not remove the moments when the whole service is slow.
  Not separately measured: how much of the improvement over the earlier "one
  turn in ten" is the connections rather than the keys.
- **The latency console refuses a batch for one unknown key in `extra`.** It
  takes `coverage`, `endpoint`, `port`, `revision`; anything else is "Invalid
  canonical field: extra" and nothing in the batch is kept. golive sent
  `agent` on every event, so none of its turns were ever there. Fixed in
  8382c52 and seen working on the 13:09 call (61 events, none refused).
- **The phone gateway does send turn detection, in camelCase**
  (`silenceDurationMs: 150`), which read as nothing until the shape of its
  `session.update` was logged. Both spellings are read now; the telephony
  profile declines it (`honor_turn_detection: false`) because 150 ms on an
  energy detector answers every hesitation and, since the early final, buys no
  latency.
- **A final asked for in a pause ends in "。" whatever was said.** Read as the
  recognizer's opinion it made "就是。", "呃。" and "我想问你一下啊。" finished
  turns on a real call. Ignored now for deciding whether the caller is done.
- **An interruption was being weighed on words that arrive late.** The early
  final used to be skipped while a floor hold was undecided; the slowest turn
  of the 13:09 call was three syllables said over the assistant.
- **Every bench turn is a first turn.** The bench opens a session per turn, so
  the slow first completion was in every bench figure before ea5d26d; warming
  at session open took the bench's median first token from ~430 to 342 ms.
- **A long prompt costs nothing once cached, and the first completion of a
  call is slow regardless.** 13,116 characters of instructions: ~7k prompt
  tokens served from the provider's cache on every turn, median first token
  444 ms — the same as with a 70-character prompt. But the first completion
  took 1101 ms with the cache already holding the prompt.
  `duplex.backend_prefix_warm_start` sends a one-token request while the phone
  rings and again as the greeting starts.
- **The owner's prompt lives in the originator's web page**, in that page's
  browser storage, and is sent only by a dial made from the page. A dial from
  the box (`to` + `greeting`) runs on the profile's own short instructions.
  For a test of the real prompt the owner dials from the page.
- **A bare "呃" is not a turn.** It was being waited on for 700 ms as a
  mid-thought and then answered anyway. `duplex.ignore_hesitations` leaves it
  with the caller.
- **A mid-thought wait was costing a whole backend round on top of itself.**
  Words that read as unfinished get 700 ms before the turn is taken, and the
  answer was begun only then: 700 + ~780. `duplex.speculate_while_patient`
  begins it on the early final, during the wait.
- **A deadline on the first clause will flush one character** if that is all
  the model has produced: "加", then "上了吗？". The flush now waits for
  `stream_first_chunk_chars`.
- **Event ids must survive a restart, and the count in them must stay short.**
  They were boot id + producer + a count from one, so each restart reissued the
  ids of the process before it and the console refused them as duplicates.
  Starting the count from the clock made them unique and all invalid ("Invalid
  canonical field: eventId"): a whole 70-turn call's events were refused. The
  console takes a producer of any shape and a count of a few digits. The id is
  now `boot:<id>:golive-<component>-r<run>:<count>`. **Probe the console with a
  new id shape before deploying one.**
- **The synthesizer has a tail too, and the account cannot afford the cure.**
  MiniMax first audio: 182 ms at the median, over 300 on 7 of 70 turns, 589 at
  worst. `tts_hedge: 2` races the first segment of each answer on two
  connections (`provider.WithUrgent` marks it) and on the bench took p95 from
  ~1070 to 1011. But the account allows about 20 synthesis requests a minute
  (burst ~20): paced like a call at 36 a minute it was refused from the 65th
  second on (`ttsprobe -call 36 -every 5s -hedge 2`), and the hedged bench lost
  2 turns in 30. A refused request is an answer nobody hears. Asking both
  every time is therefore off in the telephony profile. A 70-turn call without it ran at 16–19 requests a
  minute — under the limit, not by much; a faster conversation would hit it.
- **A second synthesis connection does not cure a stall; it is the vendor
  that stalls, not the connection.** `tts_hedge_after_ms` asks the second
  connection only when the first has produced nothing for that long, so an
  account with no room for two requests a turn can afford it. Probed on the
  box, paced like a call, with the wait at 220 ms: nine of twenty turns asked
  the second connection and it won none (the first spoke within 10 ms each
  time), and the one stall, 1847 ms, was a stall on both — the second had been
  asked 1.6 s before the first spoke and had nothing. It is at 260 now and is
  insurance against one connection going bad or failing, nothing more. Nor are
  the stalls a matter of load: over two calls the slow syntheses came at 13–17
  requests in the minute before, and 38 syntheses at 20–31 a minute were as
  quick as any. (That also says the account's limit is not a flat 20 a minute;
  a bucket of ~20 refilled at ~20 a minute fits both this and the refusals.)
  Untried: the vendor's HTTP streaming endpoint as the second route; a probe
  that asks two connections at once and prints both times.
- **What helps with the synthesizer is not asking it.** One answer in eight on
  those calls opened with words already said on them ("好的，", "是的，",
  "您说，我听着呢。", "再见。"). `duplex.phrase_cache_learn_chars: 12` keeps
  any segment that short and plays it from memory next time: no synthesis in
  the caller's silence, and one request fewer against the rate limit. The
  cache is the process's and is lost on restart.
- **`speculative_min_chars` is for hypotheses, not for a final asked for in a
  pause.** Applied to both it left every "嗯。" to be answered from nothing.
- **Where a turn's time goes now** (55 turns on 4b029b3/e517e3a, medians, from
  the caller's last sound): 120 ms waited before the final is asked for; +91
  for it to come back (so the answer is begun ~211 ms in, 169 before the turn
  closes); +120 to the chat model's response headers and +235 more to its
  first token (361 for the fastest of four copies); +105 collecting the first
  clause; +195 for the synthesizer's first audio; 0 between that audio and the
  wire. About 225 ms of the ~870 is golive's own waiting and the rest is three
  vendors' round trips. The recognizer is sent 20 ms frames as they arrive,
  the synthesizer one `task_continue` per segment on a warm connection, the
  detector has no hangover: there is nothing left to tidy on the path.
- **The chat model's speed depends on the hour more than on anything here.**
  `llm -pool`, first token of the fastest copy: 602 ms at the median at 23:53
  (every single copy ~667), and 161–202 ms from 00:08 on the same night. The
  23:34 call's turns were 100 ms slower at the median than the 23:08 call's
  for that reason alone. Compare builds within the same half hour.
- **Eight copies against four, in the same rounds** (`DEEPSEEK_LANES=8
  phone-brain.sh llm -pool`, which now prints what the first 1, 2 and 4 lanes
  of each round would have waited for). After midnight, twice: p50 170 and 161
  for eight against 183 and 180 for the first four; p90 198 and 219 against
  225 and 317. In the slow hour: p50 602 against 617, p90 678 against 687. No
  sign that eight at once slow each other. So eight is worth 15–20 ms at the
  median and 30–100 at p90, for twice the requests, and the owner has said
  chat-model spend is not the constraint: the telephony profile sends eight
  (`llm_hedge: 8`; `phone-brain.sh` sets `DEEPSEEK_LANES` to match). It does
  nothing for the minutes when the whole service is slow.
- **The owner's latency console has only golive's own events** (`coverage:
  application`; its `tts_phone` span is 0 because golive reports both ends).
  Nothing times the phone leg. golive now does, by its own echo: `listen: echo
  return time` at the end of a call, and `listen: pauses inside utterances`
  for choosing `early_final_ms` from data ("Measuring the phone leg" in
  `TELEPHONY.md`). First call with them (09:24 on 10-12, five utterances):
  fifteen pauses the caller carried on from — 60 60 60 80 80 100 100 100 160
  160 180 200 240 240 320 ms — so 120 ms splits at seven of them, 100 would at
  ten and 80 at twelve: 20 ms a turn for half as many splits again, on a small
  sample. Left at 120. The echo estimator took no measurement at all on that
  call, though the line echoes at −28 dB, and could not say why; each attempt
  now logs its reason (`listen: the assistant's voice coming back was not
  timed`) and the summary counts them (`not_timed`). The 09:34 call gave the
  answer: 4 measurements (60, 100, 300, 600 ms — a noise, two candidates, the
  caller) against 49 not timed, 34 of them because the idle line is never
  under the bar and 2 because nothing came back at all. **The echo does not
  measure the phone leg on this line; do not quote its p50.** A recording made
  beside the handset, lined up against the log, would. The same call's pauses
  (51 in 25 utterances: 11 of 60–79 ms, 7 of 80–99, 5 of 100–119, 28 of 120
  and over) say the same as the first: 100 ms would split 18% more
  recognitions than 120 for 20 ms a turn, 80 ms 43% more for 40.
- **A budget on the account's total requests starves the hedge it is there to
  ration.** `tts_hedge_rpm` was "bring the account to no more than 18 a
  minute". The 09:34 call made 22.5 synthesis requests a minute by itself (44
  segments, 19 tasks abandoned at interruptions and dropped guesses, the
  connections) with up to 26 in a rolling minute, unrefused — so there was
  never room, all four slow syntheses went unhedged, and the two turns over
  1200 (first audio 938 and 646 ms) were two of them. It now bounds what
  hedging adds: four second requests a minute. The vendor's real allowance is
  still not known; 26 in a minute passed and 36 a minute sustained did not.
- **A second synthesis account, as a pool** (on since 10-12 22:35; measured:
  first audio worst 689 ms on one account, 337 for the faster of two, never
  slow together in 30 rounds; the two voices differ a little and the owner
  wants that). `internal/provider/minimax/pool.go` keeps accounts apart (lane n is
  account n; a refused key is rested and the next takes the session);
  `provider.HedgeTTSPool` puts a session's second connection on the other
  account, counts every request per account (`tts_account_rpm: 10`), asks both
  accounts at once for an answer's first segment while the other has room
  (`tts_pool_after_ms: 0`), and moves a session off an account that has had its
  minute. One key behaves exactly as before. The second account's settings go
  in the env file as a pair, `MINIMAX_TTS_API_KEY_2` and
  `MINIMAX_TTS_VOICE_ID_2`: the voice on the box is a clone made on the first
  account, the owner is making it again on the second, and each account is
  asked for it by its own id. Unknowns, all answered by `phone-brain.sh tts
  -pool 30 -every 8s -save ~/golive-voices` once they are filled in: whether
  the two clones sound alike (which account speaks changes from turn to turn;
  `-save` writes a sample from each); whether slow first audios and the multi-second stalls happen
  on both accounts at once; and what an account's allowance really is — the
  vendor's page says 10 a minute, calls have run at 22–31 a minute unrefused.
- **Synthesis accounts are on different tiers; measure each.** The second
  account, paced alone at 30 requests a minute, refused 42 of 76 from the 28th
  second on; the first has carried 22–31 a minute. The pool had one limit for
  both and, with both counted full, left a session wherever it was — on the
  small account, past its real limit. Each account's allowance is now written
  beside its key (`MINIMAX_TTS_RPM=20`, `MINIMAX_TTS_RPM_2=8` on the box; the
  vendor's page gives a free account 10 and a topped-up one 20, and the
  second account behaves like a free one);
  both full → the roomier one; a refused request goes to the other account at
  once. First call on the pool (22:41 on 10-12, 14 turns): both accounts
  spoke, nothing refused, p50 960 with the chat model in a slow hour (first
  token 324 ms after headers against 234 at midday), one turn over (1307, the
  chat model).
- **Between about 23:00 and midnight the chat model alone misses the target.**
  The 23:14 call on 10-12 (13 turns, `b1e3c88`): p50 1309, 12 of 13 over
  1200, every turn with the first token 577–631 ms after the headers and every
  other stage as usual. `llm -pool` two minutes later: fastest of eight copies
  707 and 712 ms at the median, every copy over 550 ms in 23 of 24 rounds. The
  night before: 602 ms at 23:53, 161–202 from 00:08. Copies and keys do not
  help; a second vendor would. Say the hour with every call's figures, and do
  not read a build's worth from a call made in that hour. The same build at
  14:28 the next day: 6 turns, p50 831, none over, first token 123 ms after
  the headers (`llm -pool`: 213 and 192). On that same call
  all 13 turns were asked of both synthesis accounts (7 and 6 first), nothing
  refused.
- **Measure a chat-model candidate with the call's real prompt, not a short
  one.** Qwen3.8-27B on 10-13 at 20:40, one request each in rotation, first
  token at the median: with a 30-character prompt SiliconFlow (reasoning off)
  409 ms against DeepSeek's 606; with the owner's 13,116 characters
  (`llm -last-call ~/golive-last-call.json`) SiliconFlow 1548, OpenRouter
  1048, DeepSeek 581. DeepSeek serves the long prompt from its cache; these
  two showed no sign of one in 20 requests. Reasoning left on is 1.3–3 s
  (`enable_thinking=false` on SiliconFlow, `reasoning={"enabled":false}` on
  OpenRouter, as the fifth part of `-try`). Four copies at once on the
  SiliconFlow account: 148 of 160 refused, HTTP 429. OpenRouter is reached
  through the VPN tunnel (5.2 s to set up a connection). Neither is a fix
  for the 23:00 hour.
- **The settled close must not fire while an early final is pending**: it
  judges "no new words" on hypotheses that trail the audio.
- **A trailing "啊" is not a different turn.** `sameWords` keeps an answer
  begun on "也可以是吧" when the final says "也可以是吧，啊".
- **Dialling from the box**: `POST /api/outbound/dial` needs `to`; the locked
  number is `ALLOWED_CALLEE` in the originator's env file on the box. A job can
  read it there and dial without it ever being printed. Never copy it out.
  `scripts/phone-brain.sh call` does exactly that, with the instructions and
  opening line of the last answered call, which golive keeps in
  `~/golive-last-call.json` on the box (`last_call_file`, mode 600, written
  when a call that brought instructions is answered). So after one dial from
  the page, the owner's prompt can be redialled from the box: the owner writes
  "call" in the session and a runner job runs it. `call show` says what is
  kept. Verified at 23:34: the redial reached golive with the same 13,116
  characters and the same `instructions_sha` as the page dial before it. The originator's own source on the box is root-owned and not readable;
  a button inside it is not something a session can add.
- **The box reaches its providers through a VPN client's tunnel** (names
  resolve to 198.18.x.x; TCP connects in 1 ms because the client terminates it
  locally) and over Wi-Fi. Domestic traffic is sent on directly — TLS to the
  chat model is 30 ms — but every provider round trip passes through that
  process, and one morning it sent the recognizer's traffic abroad. What it
  costs per round trip could not be measured without root.
- **A phone line is never quiet, and returns the assistant's voice.** The
  second call held a finished question open for 3.3 s on line noise and threw
  the answer away eight times. See "A line that is never quiet" in
  `TELEPHONY.md`: `duplex.settled_close_ms`, `vad.echo_tail_ms`,
  `vad.barge_in_margin_db`. Written from that one call's log; **not yet
  verified on a line.**
- `deepseek-v4-flash` on the same key: 8 of 10 completions came back empty and
  the rest took over a second. MiniMax's bidirectional endpoint is ~2 s slower
  on a clause that does not end a sentence.
- Bench clips are synthesized afresh on each run, so a pause inside one
  ("好的，谢谢。") is sometimes longer than the end-of-turn silence and the clip
  is, correctly, heard as two turns. A first-audio figure of a few hundred ms
  is that.

## What to work on, in order

1. **Calls on 1287536 and later** (eight copies; the echo and pause
   measurements), dialled from the page or with `phone-brain.sh call`. Pool
   them with each other. Read first: `listen: echo return time` (the phone
   leg), `listen: pauses inside utterances` (whether `early_final_ms` can go
   from 120 to 100 or 80: each pause at or over the new value that the caller
   carries on from is a recognition split in two), `llm hedge: a copy answered
   first` (do copies 5–8 win their share).
1b. **What is left is mostly decisions, not iterations.** Ranked by what they
   would buy, none taken without the owner:
   - *A faster or second chat-model vendor in the race.* The chat model is 361
     of ~870 ms and several times slower in its busy hours; a second vendor
     also covers the minutes when every DeepSeek copy is slow.
     `llmprobe -try 'label|base URL|model|KEY_ENV'` measures a candidate.
   - *Answers that open with words already in the phrase cache.* Such turns
     skip the synthesizer (515–756 ms from last sound against ~870) and are
     immune to its stalls; one answer in eight does it by chance. Asking the
     model to open with one of a few short clauses would make it most turns,
     and changes how the assistant talks. With that, `min_silence_ms` (380)
     becomes the floor and is the next thing to lower.
   - *A holding phrase on a slow turn* (`holding_filler`, built, off): from
     the cache, once a turn has waited 1.5× this call's median — about 1.2 s
     from last sound. It does not make the answer faster; it ends the dead air
     of a 4.6 s synthesizer stall. First audio would then be the phrase, so
     report it separately from the answer's.
   - *The network path on the box*: provider traffic leaves through a VPN
     client's tunnel over Wi-Fi. Wired, and routed directly, is worth a few
     tens of ms per vendor round trip and possibly the synthesizer's stalls.
     Needs root on the box.
   - *The synthesizer's HTTP endpoint as a second route* in a stall; untried.
2. **Recognition on phone audio is poor** (wrong words in several turns of the
   real calls). The recognizer is run with its 16 kHz engine on audio that is
   8 kHz band-limited by the phone network. `TENCENT_ASR_ENGINE=8k_zh` exists
   but the adapter would have to send 8 kHz audio. Not tried.
3. **The remaining terms**, largest first: LLM first token (415), TTS first
   audio (216), final round trip (95), first clause (70).
4. The phone leg has never been measured. The console has the events to do it
   once golive's are accepted.

## The call journal

`scripts/phone-brain.sh journal start` on the box serves a page on the home
network (port 8790) with every call: figures, the per-turn waterfall, the
conversation, the recording when one was kept, and what was written about it.
See "The call journal" in `TELEPHONY.md`. **After every call**: write
`journal/retros/<call id>.md` (what happened, slow turns and causes, what
changed, what is open — no transcript, nothing personal), add or update
entries in `journal/issues.json` (status, commit, the call that verified a
fix) and `journal/decisions.json`, and ship them with the bundle. The owner
reads the journal; an issue fixed but not recorded there is not visibly fixed.

## What is public

The tunnel on the box (`/etc/cloudflared/config.yml`, root's; DNS in the
dashboard) publishes: `liuchuanming.ai` → Aria (3000); `golive.` → the browser
demo, a second golive of the current build (`phone-brain.sh demo on`, port
9080, apart from the phone line's); `fullduplex.` → 3100; `robotgo.` → the
call journal (the owner's choice, 10-12). Neither golive's demo nor the
journal has a login of its own; Cloudflare Access on the hostname is the only
gate. Never change the tunnel or DNS: give the owner the commands.

## The archive

`~/golive-archive` on the box keeps every finished call for good: `audio.wav`
(stereo, caller left), `transcript.txt`, `call.json`, golive's `session.json`,
the call's own lines of the log, and a manifest with checksums; one directory
per call under `calls/YYYY/MM/`. The journal feeds it (`internal/journal/
archive.go`), between calls, and plays from it. `phone-brain.sh archive
[status|verify|mirror DEST]`. See "The archive" in `TELEPHONY.md`. To analyse
an old call, read its `call.json` or re-read its `golive.log` with the current
parser; print numbers, never what was said.

## Build and test

```bash
make vet test          # go vet, go test ./...   (internal/duplex takes ~3 min)
make race              # with the race detector
make run-sim           # mock providers with realistic stage latencies
make bench-sim         # measure a server started with run-sim
make bundle            # dist/golive-telephony.tar.gz for a box with no Go
```

`internal/duplex` has failed once in a full run and passed on rerun; the test
was not identified. Treat a single failure there as worth a second look, not as
noise.

For every fix: a test that fails without it. Check that it does.

## The test box

A LAN machine runs the phone path; see `TELEPHONY.md` for the diagram and the
runbook. Things the runbook does not say:

- It is not reachable from a cloud shell. The owner copies the bundle over and
  runs commands; outbound calls are originated by Aria, the call originator on
  that box, which is also reachable at its public hostname.
- `runner.sh` (kept in `~/golive` on the owner's Mac, not in this repo) runs
  job files left in `~/golive/.runner/in` on the box over ssh and writes their
  output to `.runner/out`. With it running, a session that can write that
  folder can deploy and bench without the owner pasting anything. No sudo:
  use `phone-brain.sh restart`, not `golive`.
- `scripts/phone-brain.sh` is the whole procedure: `status`, `golive`, `restart`,
  `bench`, `net`, `log`, `llm`, `tts`, `sync`, `restore`. It needs `sudo` for docker.
  golive runs under a keep-alive loop it starts; it is not a system service, so
  after a reboot run `phone-brain.sh golive` again.
- The GSM gateway changes its SIP port when it re-registers (seen three times
  in two days) and once changed its address. RustPBX's trunk must point at the
  current one or every outbound call ends in `408 Request Timeout` after 32 s
  with the phone never ringing. A timer on the box (`gateway-port-sync`) keeps
  it in step; it was given the gateway's new address on 2026-10-11.
- Provider reachability from that box has been unreliable once (MiniMax
  handshake timeouts, Tencent refusing a recognition as cross-border) and was
  fine after a reboot. Cause unknown. `phone-brain.sh net` shows the egress.

## Rules of the road

- This repository is public. No keys, phone numbers, SIP accounts or passwords
  in files, commit messages or PR text. Provider keys live in an env file on the
  box and are never printed; the scripts print key *names* only.
- Latency claims come with how they were measured and on how many turns. A
  figure from mock providers is labelled as such.
- Debug logs contain transcripts.
