# Call out-mvnch0jj-fcps — 2026-10-25 12:45, build ff9f0f6

A real call, four minutes, quiet line, midday; the first on ff9f0f6 (the
prehook waits for the floor; no figures from the model after the readout;
quote-api v4 with codes 955–958).

## The quotation — the flow as specified

- **Prefetch during ringing:** asked at 12:45:33.8, answered by the quotation
  service in 3.5 s (api v4, 5 lead vars), long before the first question.
- **Default told directly:** t1 at 12:45:50, `took 0`, 5 lines, no prehook
  ("finished within the ack's grace; nothing to announce"), no posthook,
  delivered after a 448 ms gap. ✓
- **Second quotation (三者 300万):** memory v1 → t2 submitted 12:46:10.83;
  **prehook said in place of the model's sentence** 400 ms later ("saying
  the ack in place of the model", 17 chars) ✓; quotation service 3.5 s;
  posthook + readout delivered at a 42 ms gap, nothing skipped ✓.
- **Third quotation (三者 200万):** v2 → t3, prehook ✓, 3.5 s, delivered at
  33 ms ✓; the total equals the default's, as it should.
- 5 tool calls, 3 async, 3 delivered, 0 superseded; the model repeated no
  figure after a readout (the delivery note fix held).

## Two new focus faults (ISS-055, ISS-056), fixed in this build's successor

- 12:46:34 **the quiet close split a question**: caller quiet 420 ms, 100 ms
  of another label, line silence 0 → the turn was taken; the caller went on
  200 ms later. First half answered (557 ms), cut by the second half, the
  second half (garbled) answered on its own. Fix: the other voice must fill
  half the quiet window.
- 12:46:45 **an answer cut by a join that was then let go**: an utterance
  another voice opened over the answer was taken up on 200 ms of the caller
  in 980 ms, cut the answer at 532 ms played, and 42 ms later was judged
  mostly another voice's at the quiet close — no turn. The caller heard the
  answer stop and asked whether anyone was there (12:46:58). Fix: over the
  floor the caller counts as speaking at 160 ms only when at least as
  present as the rest of the sound, or at 400 ms regardless; the barge-in
  judge and the waited-out hold use the same rule.
- Also: the caller's 嗯 and two short utterances cut answers on purpose (3
  barge-ins judged, 2 kept); 20 judgements, 0 vetoes.

## Latency — midday, quiet

13 answered turns (summary) / 19 with the scripted ones. Console out: **p50
506, p95 675, max 819**. Caller's last sound → first audio (out + 380):
**p50 ~886, p95 ~1055, max 1199**; 5 of 19 over 1000 (out 554–819, first
clauses 378–626 after the request). Chat model first token 72–515 ms from
the request, median ~250; synthesizer first audio ~190. Scripted turns
(prehook, default readout) at 185–221 ms out.

## Hangup

Goodbye net (`said_goodbye`, 6 chars) after the caller wound up at 12:49:31;
end_call 1.3 s later, the farewell whole. 4 min 1 s.

## Follow-ups

- Verify ISS-055/056 on the next call with a noisy pause or background voice.
- The quotation flow has now run as specified on a real call; the remaining
  quotation item is the production service's address (one config version).
