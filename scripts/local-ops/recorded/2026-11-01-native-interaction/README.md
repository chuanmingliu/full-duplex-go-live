# Original GoLive native SIP interaction call

The owner answered “Ready now” before this real call. The existing Mac runner
executed `call.sh`, with the exact script digest checked before dialing.
Call UUID: `280fdab1-4078-4627-bf79-4eef8f627719`.

Engine `7fc4237`, adapter `fb11e2c`; original saved prompt, greeting, voice and
configured recipient are checked privately before launching. Recording and
365-day retention are verified. The opening is warmed and released only after
SIP answer. Existing calls are protected by idle checks; Active Call remains
available. The unique call/container/receipt cannot be replayed.

Answered at approximately 15:12:30 on 2026-11-01; Call Journal reports audio
available. The owner said `done`; analysis and clean assistant hangup are verified. The assumed 50 ms overhead
is an estimate; physical handset capture has not been verified.

Normal-response p95 is 1043 ms (14 samples), or 1093 ms with the assumed
50 ms overhead. The minimum 20-sample qualification is not met. Seven playback
interruptions take 119–623 ms from detector opening to cancellation; adapter
clear follows within approximately 2 ms. Quotation completion stays separate.

See the [call review](../../../../journal/retros/280fdab1-4078-4627-bf79-4eef8f627719.md).
`analyze.py PRIVATE_CAPTURE.json` reproduces numeric interruption evidence without
emitting transcripts. `latency.json` is the strict analyzer output. Private raw
call logs and audio are intentionally excluded from Git. Historical operations
are evidence, not scripts to replay.

Publication and receipt finalization succeeded through the existing runner at
15:23:07 Asia/Shanghai. Seven archive files, 186.2 seconds of stereo audio and
5.187 seconds of ringback verified. Notes are visible in the native Call Journal
and the operations log. Runtime configuration was not modified.
