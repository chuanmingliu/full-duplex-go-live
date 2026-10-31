# Expanded adapter simulations — 2026-10-31

These tests run on local UDP and HTTP/WebSocket sockets. They place no real
phone calls, require no provider credentials and do not contact the production
PBX, CRM or Call Journal.

## Added scenarios

| Scenario | Checks |
| --- | --- |
| Interrupt queued speech | Four codec/event-format combinations; queued speech is cut, delayed old chunks are rejected, and a late old completion cannot cancel the replacement |
| Agent faults | Invalid base64, missing response ID, queue overflow, agent error, and WebSocket disconnect each end the SIP call, record failure and release the lock/RTP port |
| Unsafe SDP answer | ACK/BYE the answered call, reject media outside the allowlist, and never release its greeting |
| Missing BYE acknowledgement | Return the bounded hangup failure instead of reporting success |
| Repeated SIP 200 answer | ACK the retransmission without opening another agent session |
| Cancellation during session setup | Stop a blocked session.updated read promptly, before SIP dialing |
| Impaired inbound media | Both codecs: ten omitted packets, one reordered packet, fourteen late/duplicate packets, wrong sender, wrong payload type and wrong SSRC; verify exact counters and silence-preserved timeline |
| Actual GoLive speech pipeline | Both codecs × beta/GA events; paced synthetic speech → mock ASR → mock LLM → mock TTS → RTP; verify input transcript, audio and saved WAV |

This adds **19 concrete simulation cases**, plus an SDP parser fuzz target.
The RTP fuzz target remains enabled.

## Bugs exposed and fixed

Cancelling after the WebSocket upgrade previously left session setup waiting
for its ten-second read timeout. Cancellation now closes that socket immediately.
The regression test requires return within 750 ms.

An agent failure racing the opening HTTP request could previously appear as
`golive HTTP connection failed`. HTTP request/read failures now preserve the
context cancellation cause, so the original agent failure reaches the call log.

## Commands

Local results for this expansion:

| Check | Result |
| --- | --- |
| Entire adapter suite, race detector, seed 20261031, ten repetitions | PASS, 130.0 seconds; includes 190 executions of the 19 new simulation cases |
| RTP parser fuzzing, ten seconds, two workers | PASS, 278,748 executions |
| SDP parser fuzzing, ten seconds, two workers | PASS, 317,926 executions |
| Adapter/command vet | PASS |
| Native Mac and Linux amd64 builds | PASS |

Before this expansion, the complete repository race suite and
[GitHub CI at base commit 4edcaba](https://github.com/chuanmingliu/full-duplex-go-live/actions/runs/41200089197)
also passed. The new changes are confined to adapter setup/error handling,
simulation tests, documentation and CI configuration.

```sh
go test -race -shuffle=20261031 -count=10 -timeout 4m ./internal/telephony
go test -run '^$' -fuzz '^FuzzRTPParser$' -fuzztime=10s -parallel=2 ./internal/telephony
go test -run '^$' -fuzz '^FuzzSDPAnswer$' -fuzztime=10s -parallel=2 ./internal/telephony
go vet ./internal/telephony ./cmd/golive-telephony
```

GitHub Actions now has an independent `simulate` job with three shuffled race
runs and ten seconds of fuzzing per parser. The existing full-repository
regression/build job is retained.

## Mock pipeline timing

One local race-enabled run measured synthetic speech-end to first received RTP:

| Codec | Realtime events | Time |
| --- | --- | ---: |
| PCMA | beta | 408.1 ms |
| PCMA | GA | 406.8 ms |
| PCMU | beta | 408.1 ms |
| PCMU | GA | 408.1 ms |

The fixture uses a **200 ms endpoint silence**, an **80 ms mock LLM first-token
delay**, and a **60 ms mock TTS first-byte delay**. ASR is a deterministic tone
recognizer, speculation is disabled, and there is no greeting to confuse with
the answer. These are deliberately controlled test settings, not the production
profile. Four samples are not a percentile benchmark or a comparison to Active
Call. They verify pipeline cooperation; they do not establish the <1200 ms
handset target, real provider performance, acoustic quality or mobile jitter.
