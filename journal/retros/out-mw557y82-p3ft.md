# out-mw557y82-p3ft — silence reminders and recording verification

- Real call, original GoLive + Active Call, build `40c20f6`, 2026-11-06; reviewed after owner done.
- Permanent archive: five files verified; 75.04 seconds; 1 caller and 6 assistant transcript lines; both audio directions contain signal.
- Normal responses: n=0; p50/p95/max unavailable. Interruption cuts: n=0; delay unavailable. No valid comparison to earlier calls.
- Quotation: one result/readout at 901 ms; first audio 380 ms. Tool duration 0 ms, not an uncached calculation benchmark; original configured calculation delay preserved.
- Strict gate: not passed due to insufficient samples. No missing/unverified timing records. Server output only, no handset verification.
- Silence config v2 used the owner's 3–6 second range. Attempts selected 5,767 ms and 5,843 ms, both template index 0; both lines appear in the transcript.
- Existing silence hangup rule fired; Active Call accepted the hangup command (HTTP 200), and the call ended.

The repeated template comes from independent random selection in one pool.
The follow-up adds ordered rounds with distinct templates and per-round intervals,
while preserving accepted-speech reset, echo/background rejection, task suppression,
preset flow and quotation delay. Keep real-call verification distinct from tests.
Next measurement: continued silence across rounds, resumed speech, and an attempted
interruption; only after a separate owner request to call.
