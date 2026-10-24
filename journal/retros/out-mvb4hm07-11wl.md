# Simulated call out-mvb4hm07-11wl — 2026-10-16 23:28, build d0bb53d

The second conversation with the simulated customer, on a fresh draft of
the same real call (run `sim-7636b3724be6d31e3eb3cb10`, scenario
`from-call-mv9oqusr-3h0z`, reserved from the desk). 145 s, ended by the
customer's gateway again (ISS-039).

## What golive measured

| turn | at | begun before close | chat model first token | first clause | synth first audio | first audio |
| --- | --- | ---: | ---: | ---: | ---: | ---: |
| item_3 | 23:29:02 | 269 | 1072 | 1178 | 1430 | **1430** |
| item_5 | 23:29:21 | 281 | 890 | 1027 | 1214 | **1214** |
| item_8 | 23:29:29 | 194 | 458 | 544 | 779 | **779** |
| item_11 | 23:29:48 | 280 | 594 | 1216 | 1471 | **1471** |
| item_13 | 23:29:55 | 193 | 563 | 724 | 889 | **889** |
| item_15 | 23:30:11 | 270 | 925 | 1048 | 1242 | **1243** |
| item_16 | 23:30:15 | 167 | 556 | 657 | 657 | **657** |
| item_19 | 23:30:29 | 286 | 993 | 1037 | 1239 | **1239** |
| item_24 | 23:30:51 | 320 | 179 | 340 | 581 | **581** |

- 9 turns: p50 **1214**, worst 1471 (+pause 1594 / 1851); 7 of 9 over 1200
  by the last-sound definition.
- Chat model, fastest of eight copies, 17 answers: p50 775, p90 1196, max
  1341 — against 656 fourteen minutes earlier and 255–485 in the first
  call's turns. Synthesizer p50 194, max 341, as usual. The slow turns are
  the chat model's alone: item_11's first clause took 622 ms after the
  first token (a long opening clause), the rest is the first token.
- Guesses 20 held, 6 abandoned; 14 answers cut by the customer (4 after
  audio played). 59 customer utterances against 27.
- Line quiet 8 times, 91.3 s filled.

## What the customer's report says

- source `a4f77d5`; assistant 27, customer 59 utterances, overlap 39 ms;
  the same three checks pass; ended "Active Call control disconnected".

## What it says about the optimisation

- Same build, same persona, 14 minutes apart: p50 655 → 1214 ms. Nothing
  in golive changed; the vendor's hour did. This is the clearest measure
  yet of what a second chat-model vendor is worth (decision 1), and the
  simulated customer is how it will be measured without a person on the
  phone.

## Open

- ISS-039. Daytime runs of the same scenario for a baseline.
