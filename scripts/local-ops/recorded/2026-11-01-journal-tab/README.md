# RobotGo experiment-tab deployment, 2026-11-01

Historical Journal-only release. `deploy.py` is the exact installation helper;
its arguments were a staged Linux binary, source-list JSON and expected SHA-256.
It captures the running Journal argv and previous executable, changes only the
Journal binary/source list, restarts Journal and checks all original call IDs
remain present plus the three experiment overviews. Failure restores the prior
executable and configuration. Do not replay this historical release blindly.

Final runtime source commit: `5bfb237` on `codex/sip-rtp-adapter`.
Binary was built from `git archive`, excluding unrelated local modifications.
SHA-256: `ddd8bbe672a0ec39cc6fcb6bc991d18d038406535fb95db42306d8a25244f929`.
Journal PID after deployment: `896872`; address remains `192.168.71.9:8790`.

Backups under `/home/chuanming/golive-telephony/journal-releases/`:

- `20261101-123708`: original pre-feature Journal (`journal.previous`), SHA-256
  `654673394efe18a3f5f69c19326b6084bf0cc1331cd8012fe7dd2dd425b12c0e`.
  No experiment source-list file existed before this release.
- `20261101-123814`: first feature version, before asset-cache correction.
- `20261101-124034`: intermediate version, before replacing embedded frames
  with direct call cards. Its `receipt.json` records the final deployed binary.

Each backup includes `argv.json` and `receipt.json`. To undo the feature, restore
`20261101-123708/journal.previous` atomically, remove only the newly introduced
`journal/experiments.json`, and restart only Journal with that saved argv and
working directory. Preserve the PID file `.phone-brain.journal.pid`. Recordings,
archives, call services and their configurations require no rollback.

## Verified

- Journal and command package race tests, `go vet`, JavaScript syntax checks.
- Public signed-in tab displays source selectors and call cards; full-page
  Journal displays real/simulation context and existing timing evidence.
- Source counts: native real 2, broad simulator 4, focused simulator 2.
- All eight call details, retrospectives, transcript downloads and WAV byte-range
  responses checked at the origin. No raw call content copied into this directory.
- Public audio playback advanced with `readyState=4` and no media error;
  paused after the playback check.
- Unauthenticated public experiment API returns HTTP 302 to Access; Access,
  DNS and tunnel rules were not changed.
- Original overview retains all 176 pre-release call IDs.
- Original engine PID `3314112`, CRM PID `3313962`, engine version `9547d22`,
  zero live engine sessions, and Active Call container remained running.
- No real or simulated call was placed during this UI release.

The broad simulator retains its incomplete candidate and TTS failures. This
release presents existing evidence; it does not change latency results.
