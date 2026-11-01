# Isolated playback-receipt comparison

Compare original-settings engine 7fc4237 against 2891b52 with the same saved
prompt/voice, adapter and isolated CRM snapshot. Each scenario lasts at most
120 seconds and asks normal insurance questions, with two scripted interruption
attempts on remote-speech starts. This is a diagnostic comparison; it cannot
establish >=20 real normal samples or actual handset latency.

Historical operations use only simulator SIP destinations, through the existing
Mac runner, and must not be replayed. Original services/configurations and
Active Call are checked before and after. The first request exceeded the
simulator's 5000 ms action limit and was rejected before reservation/dial; its
processes exited. The next preflight hit a transient occupied TCP port before
creating the attempt. No listener remained on inspection. The final distinct
attempt uses SO_REUSEADDR for the bind probe, still rejecting active listeners,
and retains separate attempt/idempotency keys. No simulator limit was changed.

Candidate binary is built from a clean git archive of
2891b52af96194eb8e9dfca5d4d56f599c390f0a, excluding unrelated local edits.
Binary SHA-256: 82152bb45d6ef50a57370e9dd8c6d26caf60db6c4be566015586dd8dd8fa7472.
Results and deployment are recorded only after terminal verification.

The baseline completed 120 seconds: normal server p95 1092 ms, n=12, no
receiver warning and no missing output receipt. The first candidate ended after
42.219 seconds with a simulated-caller `tts_chunk_failed` warning, then the
unchanged silence-hangup rule; n=2 is not a comparable performance sample.
This failed candidate is retained in `verification-first.json`. A fresh candidate
attempt has its own directory and reservation key; it does not overwrite the
failed trial or certify an acoustic latency gain.

Fresh candidate completed cleanly: 10 normal outputs, p50 896 ms, p95/max
1021 ms. Baseline n=12 p50 852.5 ms, p95/max 1092 ms. Both have zero missing,
unverified or duplicate output timing records, five verified archive files,
stereo signal and no receiver warnings. Both strict gates fail minimum sample
count. The candidate's observed p95 is 71 ms lower but its median is 43.5 ms
higher: variable dialogue and small sequential trials do not demonstrate a
causal latency gain. Original configs and Active Call are unchanged.

The fresh candidate completes both scripted interruption actions; the baseline
completes one and skips one after the remote voice stops. These action outcomes
are retained in receiver-evidence.json and are not acoustic stop measurements.
