# Simulated call — 2026-10-28 10:18, several options in one answer (82700a1)

`sim-compare-tiers`, out-mvrhjmc3-v8ay, after the owner's 09:39 call (10:07:
"optimize the scenario where the user posts many options at once … focus
on comparisons between changed options and changes in total, instead of 3
repeated responses by template"; and "I hear 保费呢 more than once").

- The default: 0 ms, direct. The three tiers at once: **one task, one
  comparison, 1 ms** — every variant from the pre-quotation cache — read once
  as the current total and each option's difference (the compare template,
  `tools/quotespeech.go`). The model passed two variants the first time and
  three the second (the simulated customer asked again).
- **A config beside configs changed the plan**: the model sent
  `config: {三者险: 500万}` and `configs` in the same call (and a second
  single-change call in the same breath), `recordConfigChange` folded the
  500万 into the memory's plan, and the next change (驾乘险) was priced on a
  plan nobody had chosen (11118.8). Fixed: a call with `configs` never
  changes the plan (engine), a config beside configs is one more option
  (lead service), the rule says not to send both.
- Openers: scenario phrases fired (保费呢，/价格嘛，/费用上， on price, 保额嘛，/
  这个档， on tiers, 这个险，/这一项， on coverage); once the two-phrase tier
  pool was used up, the generic 好的，/好，/对， took the next three tier
  turns, because unused generic phrases out-ranked a scenario phrase said
  once. Fixed: the generic pool joins only while every phrase of the
  scenario is within the last three openers; the scenario's phrases come
  back as soon as one has left the window. The choice is the least recently
  said, never the one said last (the 09:39 call's 保费呢 twice in five).
