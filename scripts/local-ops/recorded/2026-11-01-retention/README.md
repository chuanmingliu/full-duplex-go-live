# Call Journal retention: 365 days

Applied on 2026-11-01 through the Mac runner. Working chat/audio recording
retention is now **8,760 hours (365 days)**. The existing permanent finished-call
archives remain permanent; this change does not introduce archive deletion.

`apply.py` records the exact operation used on the ThinkPad. It checks that the
engines are idle, backs up launch configuration, changes retention, and restarts
the recording engines and Journal readers using their existing binaries and
other settings. This is a historical deployment record, not a scheduled job.

## Verified result

- Runner job `journal-retention-365-20261101` completed with exit 0.
- Journal ports 8790, 9876, 9886, 9896, 9906, and 9916 all reported
  `retention_hours: 8760`.
- Each Journal retained every previously listed call ID (176, 2, 8, 2, 4, and
  2 entries respectively).
- All four recording engines had `GOLIVE_STORE_TTL_HOURS=8760` in their live
  environments. Engine versions on ports 8765, 9080, 9875, 9885, and 9895
  remained 9547d22, 9547d22, cb6c639, 32bc369, and 9547d22 respectively.
- Active Call's container ID and start timestamp were unchanged.
- No call was placed and no archive was deleted.

Private backups and the verification receipt are on the box at
`/home/chuanming/golive-telephony/retention-releases/20261101-132353`.
Configuration backups can contain credentials and must not be committed.

The standalone simulator Journal readers now record their launch arguments and
PID in `journal-runtime.json` under their respective stack directories. Their
historical attempt metadata is unchanged. Existing explicit retention overrides
take precedence over repository defaults; the three deployed Compose stacks
were updated explicitly as part of this operation.

## Rollback

During an idle window, restore the relevant configuration from the private
backup and restart only the affected engine/Journal services. For the two
standalone simulator readers, the previous retention was 720 hours; use their
current runtime arguments with that value if rollback is required. Reducing
working retention can expire older working recordings, so do not roll back
retention casually. Permanent archives are separate.
