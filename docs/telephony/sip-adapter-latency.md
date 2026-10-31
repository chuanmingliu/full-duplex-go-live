# SIP adapter latency estimate

## Expected benefit

The deployed Active Call recipe already fixes old oversized RTP packets and
unpaced bursts. Those historical fixes cannot be counted as new savings. Its
current recipe drains complete frames on a free-running 20 ms timer: a newly
completed frame can wait 0–20 ms for the next tick.

The adapter sends the first complete G.711 frame immediately after idle. With
uniform arrival phases, this should save **about 10 ms on average**, roughly
**0–20 ms per first frame**. Receiver jitter buffering can mask the difference,
and unfavorable routing/load can regress it. Direct G.711 avoids intermediate
PCM conversion/resampling, but that benefit is unmeasured and excluded here.

ASR endpointing, LLM, TTS, mobile transport and handset playout remain. This
change alone is not expected to remove hundreds of milliseconds or establish
the **<1200 ms handset target**.

## Local synthetic result — 2026-10-31

```sh
GOLIVE_PACING_EXPERIMENT=1 go test \
  -run '^TestPacingExperiment$' -count=1 -v ./internal/telephony
```

60 samples per sender, 160-byte frames, local UDP on the development Mac, normal
Go build without race detection. The baseline uses a Go ticker to model the
current Active Call patch; it does **not** run Active Call. Candidate samples
use the adapter's actual sender. Both measure start/enqueue to receiver read.

| Sender | Median | p95 | Maximum |
| --- | ---: | ---: | ---: |
| Modeled 20 ms timer baseline | 9.549 ms | 20.061 ms | 21.171 ms |
| New adapter | 0.150 ms | 0.214 ms | 0.256 ms |

Difference between medians: **9.399 ms**. Difference between p95 values:
**19.847 ms**. These are not paired handset savings. The experiment excludes
providers, real PBX/GSM, remote playout, sustained queues and recording load.
Baseline recipe reference: existing local `~/telephony/patch-rtp-pacing.py`.
Check the actual deployed image/hash when collecting the live baseline.

## Operational benefit

We gain direct control over queue limits, packet pacing, interruption flushing
and timestamps. `first_rtp.enqueue_to_send_us` distinguishes time held in the
adapter from time spent generating the answer. Queue and packet counters expose
buildup. The default 500 ms ceiling does not intentionally add 500 ms delay.

## Real A/B procedure

1. Capture Active Call image/hash, GoLive revision, provider/model, codec,
   endpointing, playback lead, prompt and topology. Use identical engine settings
   and the intended `go-gpt-live-1` call configuration on both paths.
2. Verify the adapter's PBX route and advertised media address. Test answer,
   intelligible two-way speech, local/remote hangup and interruption first.
3. Alternate Active Call and adapter calls with the same handset and acoustic
   recorder. Collect at least 30 comparable substantive answers per path across
   multiple calls; retain call/turn IDs and recordings.
4. Measure **end of the user's audible speech → first substantive audible answer
   at the handset**. Report filler/acknowledgement timing separately; a fast
   filler must not satisfy the substantive-answer target.
5. Compare median, p95, worst turn, interruptions, dropouts and packet losses.
   Correlate acoustic measurements with engine telemetry and first-RTP events.
6. A proposed conservative acceptance criterion is handset p95 <1200 ms, with
   every slower outlier reported. Agree the metric and require handset evidence
   before declaring success.

**Status:** local implementation/simulation complete; live RustPBX/GSM
interoperability and handset A/B savings remain unmeasured.
