# Preset-preserving focus wakeup simulations

Historical operations for the 2026-11-01 scheduling comparison. Do not replay
these jobs: they pin the then-current runtime, configuration hashes, Aria
revision and unique experiment directories. No credentials, private profiles,
conversation transcripts, recordings or binaries are included.

`activecall-baseline.sh` is the staged body used by `activecall-baseline.job`.
It runs the original `call sim` workflow through Active Call, verifies the
selected call's permanent archive and exports only aggregate measurements.
The pushed filename was `activecall-preset-baseline-20261101.body.sh`.

`box-ab.py` starts two owned, temporary processes on loopback ports
18765/18766, copying the original profile and privately loading the existing
credentials and saved prompt/voice. The baseline executable is copied from
the actual production process (`f1fd9a6`). The candidate is the hash-pinned
`411b6d0-focus-wake-sim` build. Both keep Talker disabled. The production
process, its files and Aria binding are checked before/after and preserved.
Cleanup stops only the two processes created by the experiment.

Five synthetic utterances are reused in both arms, with two warmups and
20 measured sessions per arm. Both use the original 16 kHz rate, distinct
call IDs and the realtime WebSocket interface with `?hold=0` (answered
loopback sessions). This comparison includes real ASR/LLM/TTS providers;
it does not include the Active Call transport, SIP/PSTN or handset playback.
The separate controlled Go fixture compares the same source baseline with
and without the scheduling change to isolate its effect.

`collect.py` exports numeric timing distributions and opener source counts,
excluding warmups and private transcript text. It also exports the separate
full Active Call baseline's matched interruption decisions. Raw artifacts
remain private on the box. The collector depends on a hash-pinned copy of
`scripts/call_latency.py` staged as `talker-call-latency.py`.

The manifest retains executed hashes and reviewable saved hashes. Trailing
whitespace was removed from saved Python bodies and the matching saved job
hash was updated; no executable statement changed. Jobs retain original
staging filenames and require their separately staged binary dependencies.
