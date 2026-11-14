# Expanded Chinese backchannel test operations

Historical exact jobs for the existing Mac runner, 2026-11-02. Do not reuse
completed operation names or remove their one-attempt guards.

The inventory found six completed `f1fd9a6` audio archives. The test selected
up to eight most recent eligible archives before inspecting model results,
including the missed call. It never dials a recipient or restarts production.

The isolated copy of the running original GoLive binary uses the same profile
and saved effective voice. It synthesizes 40 new public fixture texts, reuses
only the exact 12 prior regression WAVs and stops before model evaluation.
The disposable evaluation container uses the existing model-service image;
its source is mounted read-only. Model service port 18087 and its live control
policy are unchanged. All audio and per-frame traces stay on the box under
`~/golive-telephony/codex-latency/backchannel-matrix-20261102`.

`manifest.json` binds the source bytes that ran, including the fixed protocol.
The later local summarizer and its two tests are not part of that remote payload;
they process only the returned numeric summaries and the Go phrase baseline.
Production executable/profile/environment/saved-context hashes and the Aria
routing state are compared after cleanup. New operations need fresh preflight,
new receipt names and a reviewed payload, not edits to these historical jobs.
