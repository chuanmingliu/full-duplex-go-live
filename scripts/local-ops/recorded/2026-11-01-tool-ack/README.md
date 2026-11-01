# Tool and acknowledgment simulator operations

Historical box-side Python bodies and hash-checked Mac runner jobs for the
2026-11-01 native GoLive comparison. They copy private configuration into
separate experiment directories and bind separate engine, CRM, Journal, SIP and
RTP ports. The adapter destination is constrained to the reserved `sim-...` URI;
no real recipient is dialed. Each engine/CRM pair is stopped after its trial.
The Journals remain available for the recorded evidence.

`run-broad` executes baseline–candidate–candidate–baseline with the established
customer scenario. `run-focused` requires that comparison to have completed,
then executes baseline–candidate with explicit synthetic quotation requests.
Fresh CRM processes reset in-memory quote caches in each trial. Both use the
same copied provider/configuration files; original file hashes are checked at
completion and Active Call must still be running. The upstream quotation service
is shared and is not accelerated or modified by these scripts.

`verify-archives.py` checks manifest hashes/sizes, closed session and reports whether both speakers
and stereo channels are present, then extracts metadata-only task and receiver timings.
Raw audio, logs, transcripts, credentials and quote payloads remain private.
Fixed experiment paths/attempt receipts make these historical jobs unsuitable
for replay. Do not remove guards to reuse them. See the comparison report in
`docs/telephony/2026-11-01-tool-ack-comparison.md` for results and qualifications.
