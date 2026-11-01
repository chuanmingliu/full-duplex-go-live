# Active Call interruption prefeed operations

Exact historical Mac-runner bodies for the 2026-11-01 interruption experiment.
`baseline-sim.sh` reserved and dialed only a simulator using the saved original
GoLive configuration. `record-aria-binding.sh` recorded the existing Aria local
backend and retained its actual 45b5564 binary and private launch material on
the box for a future owner-requested rollback. It did not change routing.

These jobs have single-attempt and version guards. Do not replay them. Inspect
current state and prepare a new reviewed job for later operations. Credentials,
private process environments, audio and transcripts are not included here.

`deploy-f1fd9a6.sh` replaced only the idle phone engine after full CI, retained
its existing supervisor and launch context, and verified cache warming and
847 preserved archive files. `candidate-sim-a.sh` and `candidate-sim-b.sh`
replayed the same simulator scenario. `cut-decisions.py CALL_ID` is the numeric
read-only extraction used for candidate cuts; the baseline used the same
pairing rules without the new prefeed diagnostics. `publish-review.sh` shipped
three retrospectives and updated only ISS-095. It did not close the issue or
post owner feedback. `review-payload.json` contains only sanitized report text.
