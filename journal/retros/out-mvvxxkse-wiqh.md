# Quotation replay out-mvvxxkse-wiqh — 2026-10-31

Simulated customer, build `f842928`, run `sim-eac1cda817afbc70707a8ccd`, session `sess_fa89c2eadbfb25c76c7b56f8`. The 68.897-second stereo recording and all five raw archive files were collected and hash-verified.

The existing analyzer reports five caller samples, first-audio p95 380 ms and first-content p95 1020 ms. Three deterministic quote readouts are 882, 921 and 1003 ms. These figures describe the retained rows only: **the complete answer population is not verified**, despite the old report's missing=0. The strict gate already fails the twenty-sample minimum.

At 13:09:28.369, item_9 starts an opener. At 13:09:28.865 it starts body content. At 13:09:29.186 the engine incorrectly declares the opener a backchannel, discards playback ownership and emits no latency row for that started answer. The parser's explicit-backchannel exemption then hides the omission. This is ISS-087; do not describe this replay as clean evidence or infer the missing latency from its transcript timestamps.

The correction preserves ordinary interruption evidence when any body chunk has already committed to output. True opener-only continuations still drain as backchannels. This replay has no coalesced task events, so it does not verify ISS-085's coalesced path. Its twenty canonical segments show no timestamps backdated beyond 5 ms, supporting ISS-086 on the observed paths. The archived files and their original report remain intact.
