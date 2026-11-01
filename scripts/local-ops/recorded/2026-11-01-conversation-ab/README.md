# Recorded conversational simulator A/B

Historical operation; do not replay. The unique attempt and idempotency keys
are already consumed. The existing Mac runner executed `run.sh` with the
hash-bound payloads in `manifest.json`, then `verify.py` checked both archives.
`scenario-overrides.json` changes only the simulated customer.

The job exited 0 at 14:50:12 on 2026-11-01. `verification.json` retains numeric
results and failed as well as passed gates. Engine `9547d22` is the retained
native backup; `302267b` is the isolated candidate. Both use CRM `3201ec4`.
No real recipient, saved agent config, quote delay or Active Call was changed.

See [analysis](../../../../docs/telephony/2026-11-01-conversation-ab.md).
