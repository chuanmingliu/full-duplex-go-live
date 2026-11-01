# Native SIP test engine 7fc4237 deployment

Historical idle-only deployment through the existing Mac runner; never replay.
Exact-source full CI `41281964710` passed both verify and simulate jobs before
promotion. The recorded quote-heavy trial exercises the new caller guard five
times and retains verified chat/audio with zero receiver warnings. Its strict
latency gate still fails for count and one unverified chained result/readout.

Runner exits 0 at 14:55:38 on 2026-11-01. `receipt.json` confirms native engine
9895 reports `7fc4237`, ready/idle/capacity one. The no-dial adapter preflight
passes. Original configuration hashes, other GoLive versions, native Journal
and Active Call identity/start are unchanged. Adapter remains `fb11e2c`.

The prior `f12b6f5` executable is backed up in the release directory recorded
in the receipt. The earlier original `9547d22` backup is retained too. The
operation checks idleness twice, requires expected hashes, keeps a deployment
lock and restores the previous binary on a failed verification when no new call
is active. No real call or physical handset qualification occurred.

`manifest.json` binds the binary, exact-source CI receipt and deployment code.
