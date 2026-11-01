# Call out-mvgqu7vv-cfqc — 2026-10-20 21:53, build df22ed2

A real call from the metro, 180 s, evening.

## The ending — the goodbye net on a real line

21:56:21 the model answered the caller's goodbye with a thirteen-character
farewell (from the phrase cache, first audio 204 ms) and no tool call;
`hangup: the assistant said goodbye; ending the call rule=said_goodbye`;
1.3 s later `the gateway was told to hang up status=200`. ISS-047 verified
on the phone. The farewell was whole (nothing over it).

## The noise

Five utterances judged another voice's and let go by; **three barge-ins
kept** — announcements over the answer that did not stop it (the first
`keep`s on a real call since the veto mode went in); 30 judged, 0 vetoed,
0 too late. 12 quiet closes, 10 guesses abandoned.

## What golive measured

17 turns, last sound → first audio: p50 **1022**, max 1649, **4 over
1200**. Two of the four were the synthesizer: first audio 320 and 755 ms
after the first clause (items 33, 34 — the evening stalls again, the Fish
fallback at 320 did not fire on the first and was not seen on the
second); two the chat model (531, 536 first token). Console's definition
p50 595, p95 1227.

## Also: the quotation tool was asked and refused

`tools: task done tool=quote_total … HTTP 422: 车损险 needs the variable
vehiclePrice` — the model asked for a total with 车损险 in the plan and no
vehicle price on the lead; the lead service refused. The model carried on
(the result was never read out). Either the prompt asks for the price
before quoting, or the service quotes without the coverage and says so.
Owner's call; filed as ISS-048 (open).

## Open

- The synthesizer's 755 ms first audio with the fallback configured at
  320 ms: check whether Fish was asked (`speak: fallback`) on item_34.
