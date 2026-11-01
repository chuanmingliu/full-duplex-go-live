# Native SIP real-call operation evidence

Historical sources for the explicitly authorized real handset call
`out-native-sip-20261101-01`. These files are not reusable launch recipes.
They contain fixed process/build/path guards and an already-used call receipt;
do not remove guards to replay them. No private credentials, recipient, prompt,
transcript or audio is included.

- `deploy-initial.py`: exact initial box-side deployment source. **It did not
  reach readiness**: provider key paths needed container mounts. It is retained
  as historical evidence, not presented as the final successful setup.
- `compose.deployed.json`: actual final Compose snapshot, after adding read-only
  provider key-file mounts. Relative private/state/bin paths require the prepared
  deployment directory; secret values are not included.
- `call-01.py`: exact successful one-call body, with exclusive receipt, saved
  configuration checks, native opening warmup and no automatic redial.
- `call-01.sh`: exact Mac runner job body that validates the Python source hash
  before executing it on the box. It has already run.
- `finalize-call.py`: verifies all seven archived files, both audio channels,
  transcript speakers, clean SIP teardown and live Journal notes, then finalizes
  the private attempt receipt. It does not dial or restart services.
- `manifest.json`: hashes/sizes of these operation artifacts.

The date-based call ID in this historical source was rejected by the separate
latency console's privacy validator. Future calls should use a prefixed UUID as
described in the [deployment runbook](../../../../docs/telephony/2026-11-01-native-sip-real.md).
The original archive keeps its identity. Active Call and the original engine
were preserved throughout.
