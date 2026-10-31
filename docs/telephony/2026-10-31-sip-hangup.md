# Unexpected hangup in the isolated SIP adapter test

## Observed call

The isolated `cb6c639` deployment placed call
`out-sip-cb6c639-20261031-01` on 2026-10-31. SIP answered at 13:05:32 UTC;
the adapter ended the call at 13:05:48 UTC, about 16 seconds later.

The adapter recorded `outgoing audio queue exceeded limit`, then sent SIP BYE
and received its acknowledgement. Its container exited with code 1 and was not
OOM-killed. The media counters recorded 634 transmitted packets, a maximum
accepted queue of 498 ms, and zero detected lost inbound packets. The configured
queue limit was 500 ms; the rejected enqueue is not included in that maximum.

The outbound WAV contains approximately 164 ms and 166 ms of inserted timing
gaps across two 6.34-second stretches of encoded audio. The largest individual
gap was only 1.125 ms. These are small delays accumulating over many packets,
rather than one long transport pause. Archive checks verified all five files
listed in the manifest. Audio and three transcript lines were preserved.

Recordings remain in the private Call Journal/archive. No conversation content,
telephone number or credentials are included here. These recordings measure
the adapter boundary, not sound reaching the handset.

## Cause and fix

The RTP sender scheduled each packet 20 ms after the **actual** preceding send.
Every late timer wake therefore shifted all future deadlines. A producer running
at 8 kHz eventually outpaced the sender, even though both nominally used 20 ms
audio frames. With the engine's initial playback lead, the queue exhausted its
remaining headroom and the adapter intentionally terminated the call.

The sender now advances deadlines on its media timeline. Ordinary scheduling
jitter no longer accumulates. After a substantial stall or idle period, it
rebases the timeline to avoid a burst of overdue packets; recovery never
deliberately schedules packets less than 10 ms apart. First audio still sends
immediately. The 500 ms queue bound and interruption semantics are unchanged.

## Regression evidence

The controlled-clock regression drives the real queue, RTP packet builder and
UDP sender. Its producer starts with 300 ms of audio, then supplies 40 ms chunks.
Against the old pacing logic it reproducibly triggers the same queue-overflow
error after 9.0405 seconds with 0.5 ms wakeup delay, or 2.442 seconds with 2 ms
delay. Zero-delay scheduling passes. Both PCMA and PCMU behave identically.

With corrected deadlines, eight scenarios each send 3,000 frames (60 seconds
of audio) with a maximum queue of 300 ms. Scenarios cover both codecs and zero,
constant and varying scheduling delays. Additional cases check stall recovery,
idle resumption, RTP timestamps and marker bits.

An opt-in 30-second simulation exercises the complete adapter with a local SIP
peer, WebSocket agent, real timers, UDP and WAV recording. It covers both codecs
and both Realtime audio-event formats. No real number or external provider is
used. CI runs this alongside the existing interruption and failure simulations.

Local Mac validation passed all four 30-second simulations with race detection:
1,500 packets per call, 300 ms maximum queue, and approximately 29.981 seconds
between first and last packet. Three shuffled repetitions of the complete
adapter suite passed in 41.9 seconds. Adapter vet and the Linux amd64 build
also passed.

The same isolated Linux test executable passed on the ThinkPad box: all eight
controlled-clock cases, both stall/idle cases, and all four 30-second simulated
calls. Each box simulation transmitted 1,500 packets, peaked at a 300 ms queue,
and played for approximately 29.981 seconds. The runner verified production's
PID, executable/configuration hashes and existing PBX container identities
before and after testing. No deployed service was changed and no real call
was placed by these tests.

```sh
go test ./internal/telephony -run 'Test(SustainedPlayback|PlaybackScheduler)' -count=1 -v
go test -race -shuffle=20261031 -count=3 -timeout 3m ./internal/telephony
GOLIVE_SUSTAINED_SIM=1 go test -race ./internal/telephony -run '^TestSimSustainedPlayback$' -count=1 -v
```

This fixes a sustained-playback stability problem. It does not demonstrate
handset latency below 1,200 ms. The production services and the original
`cb6c639` test deployment are preserved pending the operator's version-switch
instruction. A new handset call is required to validate the fix on the carrier
path.
