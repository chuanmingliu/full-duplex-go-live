# Active Call rollout operations — 2026-11-01

These are exact reviewed runner bodies, not reusable launch commands. Their
state guards and one-attempt receipts belong to the recorded operations.
Never replay a completed job or retry an uncertain dial. The manifest contains
the source-body hashes; transfer wrappers and private receipts remain on the
Mac/box.

- `baseline-9547d22.sh`: simulator-only coverage run with original configs,
  verified chat/stereo audio, and separate normal/quotation latency reporting.
  Two earlier preflight attempts stopped before reservation or dialing because
  `/readyz` returns HTTP 204; this body checks the observed contract.
- `preflight-45b5564.sh`: captured actual runtime identities, protected config
  hashes, saved context, pinned quotation policy and 143 archive fingerprints.
- `deploy-45b5564.sh`: deployed the CI-verified shared engine, CRM and Journal
  changes with original profiles excluded from the payload. Final verification
  stopped on additive `call.json` fields. Its failed rollback restored files
  on disk but did not restart the healthy new processes.
- `reconcile-45b5564.sh`: proved that removing only the two new latency fields
  reproduced all 143 original summary hashes, verified 572 unchanged original
  files, and restored matching candidate files on disk without restarting any
  service. Prior binaries and the failed receipt were retained.
- `candidate-45b5564.sh`: matched simulator-only coverage check gated on the
  successful reconciliation receipt and complete warm-up.

No raw recordings, transcripts, credentials, private prompts, customer numbers
or release binaries are included here. Deployment and performance evidence are
recorded separately under `docs/telephony/evidence/2026-11-01-active-call-rollout`.

- `interruptions-45b5564.sh`: two scripted cut-ins completed, but the original
  verifier rejected the journal greeting field after a later system utterance.
  It must not be replayed.
- `collect-interruptions.py`: read-only collection of that exact ended call;
  proves the initial greeting from the canonical log and retains the failed
  derived-field check. It does not dial or write on the box.
- `cut-decisions.py`: read-only extraction of server VAD-open to cut-decision
  timing for the pinned baseline. The same parser with only CALL changed was
  used for candidate coverage and scripted interruption calls. It does not
  measure audible playout stop.
- `normal-content-tail.py`: read-only extraction of numeric timing for the six
  longest normal responses in each coverage call, excluding quote-only rows.
- `publish-0c2ba18.sh`: publishes the three sanitized retros, merges issue
  updates while preserving unrelated entries, and appends one authorized Desk
  work-log line. The receipt verifies the retros are visible and Desk control
  state is unchanged. Never replay this publication job.
