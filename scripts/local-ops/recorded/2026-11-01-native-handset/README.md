# Completed original GoLive handset test

Call UUID `3af753f8-d623-43ff-a619-6c4a6557cccb` ran 15:32–15:37 Asia/Shanghai,
November 1, 2026, through the existing Mac runner. Engine 7fc4237, adapter
fb11e2c, original saved settings and Active Call preserved. Owner said done
before deep analysis. Exact dial operations are historical: **never replay**.

Normal content p95 1260 ms over 30 measured responses; estimated handset p95
1310 ms using the owner's 50 ms assumption. Gate fails latency and one missing
started-output receipt. Thirteen playback interruptions have detection-to-cancel
p50 232 ms, p95 2087 ms; all 16 cancellations reach adapter clear within about
2 ms. Quote readouts and their already-prepared results are separate.

Seven archive files, stereo audio and chat verified; assistant SIP BYE succeeded,
adapter exit 0. Local handset WAV/sidecar hashes match, but capture integrity is
invalid (`sample_clock_discontinuity`). Raw audio, transcripts and private
metadata stay outside Git. Numeric results do not certify acoustic latency.

See the matching Call Journal retrospective for full interpretation. `analyze.py`
accepts the private captured JSON and emits only numeric interruption evidence.
`latency.json` is the strict server-output result with 1150 ms target, 20 samples,
`--metric both --quote-sla separate`. No missing historical record is imputed.
