# Recorded ready-acknowledgment comparison

Historical runner jobs for the completed 2026-11-01 simulator comparison of
`1e38bac` and `151b2e3`, using the original GoLive provider configuration.
`run.sh` uploads hash-checked binaries and runs `run.py` on the box. `verify.sh`
checks archives, runs the strict analyzer, and appends the authorized Desk log.

The fixed attempt directory is deliberately non-reusable. Do not replay these
jobs, remove their terminal-state guards, or reuse their call identities.
Both processes use isolated ports and private configuration copies. Only the
reserved simulator destination can be dialed; the original services and Active
Call remain intact. The existing focused Journal on port 9916 serves both calls.

Both runner receipts ended with exit 0. Each call has five archive files verified
by SHA-256 and size, two-sided stereo audio and both transcript speakers.
Both latency gates failed. See
[comparison](../../../../docs/telephony/2026-11-01-ready-result-handoff.md).
Raw conversations, recordings, private configs and credentials are not included.
These historical checks used 1200 ms. Subsequent server checks use 1150 ms with
the user's assumed 50 ms combined telephony/handset overhead; that assumption
is not a measured handset result.
