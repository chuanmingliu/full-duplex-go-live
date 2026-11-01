# Recorded newer-caller scheduling test

Historical simulator-only operation. Do not replay its consumed attempt or
idempotency key. `run.sh` binds the payloads in `manifest.json`; `run.py`
retains the original agent config and 90-second quote-heavy simulator scenario.
`verify.py` checks archived chat/audio and measures separate conversational and
quote latency without weakening evidence-integrity gates.

Runner exit 0 at 14:52:13; latency gate fails. See `verification.json` and
[the retrospective](../../../../journal/retros/sim-newcaller-16c41c8a-a5b5-413d-ab81-1377d5891a14.md).
The new protection executes five times; six direct/coalesced timing links
validate and one chained task remains unrooted. No real call is made.
