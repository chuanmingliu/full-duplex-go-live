# Response latency

Last frame of speech sent → first audio byte received, measured client-side.
Warmup runs excluded. Medians, not means: one cold-start turn moves a mean
enough to hide what every other turn did.

| stack | runs | failed | min | p50 | p95 | max | IQR | transcript p50 | VAD close p50 |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| golive-mocks | 6 | 0 | 1024 | **1105** | 1110 | 1110 | 60 | 640 | 640 |

All figures in milliseconds.
