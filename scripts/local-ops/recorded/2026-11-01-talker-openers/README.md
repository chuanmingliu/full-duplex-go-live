# Isolated Talker comparison

Historical runner job and Python body for the 2026-11-01 experiment. The job
expects separately staged, hash-pinned Linux binaries; no binaries, private
profiles, environment files, prompts, audio or transcripts are committed.
Do not replay it: its attempt directory must not already exist.

The final `d` job copies the actual live `f1fd9a6` executable as its baseline and starts
two temporary GoLive processes on loopback ports 18765/18766. Both use copies
of the original profile and the saved call context; only the candidate adds
Talker settings. Store and last-call paths are isolated. The normal phone
service, original files and Aria backend binding are checked and retained.
Only the two processes created by this job are stopped in its cleanup.

Synthetic clips are made once through the native session interface. Both
comparison arms then use the realtime interface with `?hold=0`: these are
already-answered loopback sessions with no opening/greeting to release. The
original `hold_for_opening` setting remains intact. Both arms use the original 16 kHz rate and distinct `-call-ids`, with ten
measured sessions and two warmups per arm. No SIP, PSTN or real recipient is dialed. The benchmark is not a handset measurement.

An earlier attempt lacked the session hold override and is excluded from
latency conclusions. Its original configurations and production service were
verified unchanged and its temporary processes were stopped before this run.

`golivebench` now permits explicit `-a-kind` and `-b-kind`, so both arms can
use the same protocol. `-speak` requires a native GoLive first endpoint; create
clips in a separate step before a realtime-versus-realtime comparison.

`collect.job` / `collect.py` read the completed private experiment and export
only timing distributions, source/fallback counts and preservation receipts.
They exclude the two warmup sessions per arm. Enqueued openers are counted as
server-emitted only when their session, turn and phrase match a canonical
first-output transcript event. The collector uses a hash-pinned copy of
`scripts/call_latency.py`; private text and audio remain on the box.

The retained `diagnostic-c.*` and `collect-c.*` artifacts describe an excluded
trial at 8 kHz with no call IDs. Its warmed 16 kHz cache did not match, and
background audio admission rejected the anonymous sessions. It must not be
used to claim the original Active Call latency or generated-opener usage.
The final run changes only simulator parameters to address these mismatches.

The saved Python bodies have trailing whitespace removed; the saved job
checksums are updated to match those reviewable copies. The manifest also
retains the original executed body/job hashes under
`executed_before_whitespace_normalization`. No executable statements changed.
