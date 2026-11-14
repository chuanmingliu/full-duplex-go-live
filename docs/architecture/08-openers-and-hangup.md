# Preset opener selection and hangup decisions

This guide describes the original Active Call path, verified on 2026-11-01.
The running engine is `f1fd9a6`; its original profile has preset openers enabled
and Talker disabled. The live lead-service configs are `openers` version 3
(eight scenarios, 17 phrases) and `hangup-rules` version 3. Those panel configs
can change independently of a code release. Examples below describe source
behavior and built-in rules, not a dump of private conversation data.

## Which decisions are semantic?

| Decision | Implementation |
| --- | --- |
| Question versus statement for an opener | Punctuation, suffix and keyword heuristics |
| Matching an opener scenario | Literal keyword substrings, first matching enabled scenario |
| Choosing a phrase within that scenario | Least recently selected, with immediate-repeat avoidance and randomized ties |
| Generating the substantive answer | Main LLM, instructed to continue the selected opener naturally |
| Contextual goodbye, wrong number, refusal, completed business | Main LLM interprets conversation and invokes `end_call` |
| Explicit complaint/do-not-call rule | Configured regular expression against final caller words |
| Assistant has already said goodbye | Configured regular expression against the completed assistant reply |
| Silence and maximum call duration | Engine timers plus floor/quiet checks |

There is no embedding search or vector-similarity threshold in these opener
or hangup-rule paths. The disabled Talker experiment is not involved.

## How opener candidates are chosen

1. **Load configuration.** At session startup, `loadOpeners` fetches
   `/api/configs/openers` from the lead service. Failure leaves the original
   profile's question/statement phrase pools in use. Cache warming prepares
   phrase audio with the original provider, voice, speed, language and synthesis
   rate. A panel save can warm the new phrases through `/v1/phrases`.
2. **Classify the caller's words.** `ReadsAsQuestion` checks `?`/`？`, endings
   such as `吗`/`呢`/`吧`, and expressions such as `多少`, `怎么`, `什么`,
   `能不能`, `有没有` and `多久`. This is a lightweight heuristic, not an
   LLM intent classification.
3. **Choose a scenario.** `Openers.Match` walks the configured scenarios in
   order, skips disabled scenarios and incompatible question/statement kinds,
   then uses `strings.Contains(transcript, keyword)`. The first matching
   scenario wins. There is no scoring or combination of several scenarios.
   For example, a question mentioning both 理赔 and 手续 can match the claim
   scenario before the process scenario if claim is listed first.
4. **Select a candidate phrase.** Without a scenario, use the profile's
   question or statement pool. Within a matched scenario, prefer the phrase
   with the smallest last-selection counter. Shuffle iteration order to break
   ties. Avoid the immediately previous phrase when alternatives exist. If
   every scenario phrase appears in the recent-three selection history, add
   the generic pool to the candidates. The implementation records *selection*
   history; a canceled speculative turn can still affect that history. It is
   not a strict ban on all of the last three phrases or a frequency count.
5. **Coordinate with the main LLM.** Save the choice in `Turn.OpenerPlanned`
   and append `openerNote` to the model request. Its instruction is effectively:
   “The system has already spoken this opening; continue naturally after it,
   do not repeat it, and do not add another 好的/嗯.” The speech path also
   handles the model repeating an opener that was already emitted.
6. **Play cached audio at the normal boundary.** `sayOpener` checks that this
   is an eligible non-scripted turn, no answer audio has started, the player
   can accept an opener, and the call is not ending. The cache lookup must
   match provider, voice, speed, language, rate and text. A cache miss skips
   the standalone cached opener; it does not block the answer while making
   a new TTS request or automatically search a different phrase.

The existing audio handling trims leading/trailing silence, caps the preset
opener at 540 ms and fades its last 60 ms. First-content latency excludes this
opener. If the caller continues within the opener's playback window plus a
300 ms grace, the engine can finish that short opener as a listening
acknowledgment, cancel the premature substantive answer and suppress another
opener for the immediate continuation (1,500 ms window). These rules, phrase
pools and continuation instructions are preserved by the scheduling work.

Source: [scenario data and matching](../../internal/tools/openers.go),
[question heuristics](../../internal/endpoint/endpoint.go),
[selection, cache playback and continuation note](../../internal/duplex/listen.go),
[LLM request coordination](../../internal/duplex/think.go),
[cache identity](../../internal/duplex/phrasecache.go).

## How hangup scenarios are recognized

The engine loads `/api/configs/hangup-rules` at startup. If unavailable or
unusable, it falls back to the built-in rule set. Invalid regex rules are
reported and dropped; enabled valid rules continue to work.

### Conversational meaning: main LLM plus `end_call`

`EndCallTool()` puts enabled model rules and their farewell instructions into
the tool description. The main LLM receives conversation context and decides
whether a situation applies. It says the farewell and requests `end_call`
with a rule ID/reason. This is semantic reasoning by the main LLM, not a
separate semantic classifier or small Talker model.

The live version-3 model rule IDs are `goodbye`, `refused_twice`,
`wrong_number` and `done`. Both `refused_twice` and `done` require two counted
caller turns in the live configuration; the global model minimum is zero.
The shared engine guard still rejects hangup during the first ten seconds.
The counter is advanced by committed caller turns with words; it does not
prove that two complete answers were acoustically heard by the handset.

The engine does not independently rerun an LLM to verify the model's reason.
The tool description guides the model; minimum age and configured turn counts
are the deterministic safeguards. `MinTurnsFor` first matches an exact rule
ID. If the ID is absent, it can use a literal substring match between the
reason and the rule's condition/name; otherwise it uses the global minimum.
The rule argument is not enforced as a strict enum, so an unknown ID does
not independently prove that a configured semantic scenario was satisfied.
A refusal for insufficient turns also
suppresses the assistant-goodbye regex fallback for 20 seconds, so the same
premature farewell cannot bypass the rejected tool call.

### Literal matching: caller and assistant rules

- A `keyword` rule compiles its `pattern` as a Go regular expression and runs
  it against finalized caller words, not each unfinished ASR partial. The
  built-in complaint pattern is `投诉|别再打|不要再打|骚扰`. If matched, the
  engine abandons speculation, speaks the configured farewell and requests
  hangup.
- An `assistant` rule checks the current assistant reply after it finishes
  speaking naturally; truncated or superseded playback cannot trigger it.
  The built-in fallback pattern is `再见|拜拜|不打扰您了`. It handles a model
  that says goodbye but forgets the tool; it does not add a second farewell.

**Regex matching does not understand negation, quotation or conversational
context.** A phrase containing a configured pattern can match even when the
caller is discussing that phrase rather than expressing the intent. This is
an existing limitation, not a semantic guarantee. The latency change does not
alter these rules or widen their matches.

### Timers and physical hangup

The verified live configuration uses 25 seconds of silence and a 900-second
maximum call duration. Silence is measured from the later of caller speech
and the assistant becoming quiet. Silence/duration rules wait while the caller
is speaking or the assistant still has the floor; the ten-second initial-call
guard applies here too.

After accepting hangup, the engine marks the call as ending and protects the
farewell from interruption. `endCallWhenQuiet` allows an initial 600 ms for the
farewell to start, polls for empty/quiet playback, then gives the final audio
700 ms to reach the line. It rechecks playback after that grace and bounds the
wait at approximately 20 seconds. Those waits protect the farewell; they are
not ordinary-response or interruption latency.

Finally the engine emits `session.hangup`. The server calls the configured
Active Call command endpoint with `{"command":"hangup"}` and the associated
call ID, then closes the session. Closing the WebSocket alone is insufficient
to prove the phone call ended. A carrier/remote-party hangup is a separate
transport event, not one of the conversational intent rules.

Source: [rule parsing and model tool description](../../internal/tools/hangup.go),
[guards, timers and farewell completion](../../internal/duplex/tools.go),
[session-to-gateway hangup](../../internal/server/session.go),
[gateway integration regression](../../internal/server/realtime_test.go).

## Caller silence reminders

The optional Leads `silence-reaction` config adds bounded reminders after a
randomized quiet interval, using literal templates and the normal guarded speech
pipeline. Existing hangup rules take priority. See the
[silence reaction guide](../services/silence-reaction.md) for configuration,
quotation-work exclusion, timing and rollback.
