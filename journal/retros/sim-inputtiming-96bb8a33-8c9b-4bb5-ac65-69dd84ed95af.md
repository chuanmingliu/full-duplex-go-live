# Caller-input provenance: recorded SIP validation

- Call: `sim-inputtiming-96bb8a33-8c9b-4bb5-ac65-69dd84ed95af`.
- Engine `302267b`; diagnostic CRM `3201ec4`.
- Receiver `sim-7b7ed38ec74269e73922d706`; same 90-second quotation scenario.
- Adapter exit 0, scenario duration limit; isolated engine/CRM/adapter exited.
- Original configuration hashes and Active Call identity/start unchanged.
- Five archive files hash-verified, 92.749 s stereo with signal in both channels,
  14 assistant and 12 caller transcript entries; no receiver warnings.

## Separate conversational and quotation report

| Server metric | p95 | Samples |
| --- | ---: | ---: |
| First audio | 1178 ms | 7 |
| Conversational first content | 1178 ms | 6 |
| Verified tool-result first content | 4260 ms | 4 |
| Verified quotation readout | 4260 ms | 2 |

The strict separate-SLA gate **fails**: two unverified task links, two missing
quotation readouts, fewer than 20 conversational samples, and conversational
p95 above 1150 ms. The assumed handset estimate is 1228 ms, not an acoustic
measurement. Quotation latency remains reported separately, with the original
2500–4500 ms quotation-service delay preserved. Comparisons with prior
free-running conversations do not establish a causal latency change.

Four result links contain measured version-1 input provenance and validate.
Two of those callers had no playback metrics; they do not acquire artificial
conversational samples. One separate verified quote-only handoff is excluded
from conversational content. The new Journal parser and analyzer agree on all
four distributions, threshold counts, joined-response counts and integrity
counters when evaluated at 1150 ms against this same immutable archive.
The running Journal reader has not yet been upgraded; this retrospective is
the current candidate analysis, not a claim that its old summary was replaced.

## Remaining evidence and conversation issues

Task t3 was submitted by result turn item_6, and task t5 by result turn item_15.
These turns correctly have no direct caller speech-end timestamp. Their later
results therefore have no caller-origin link and remain unverified. A follow-up
repair must retain an explicit parent-task/result relationship and validate
that lineage; borrowing the engine's latest caller timestamp would be wrong.
The first chained task used a changed memory version; the second kept the
same version. Do not assume both represent redundant work.

Two verified results were interrupted before quotation readout began. Those
missing readouts remain failures, even though their initial result content
played. The 1178 ms conversational outlier was item_7: 380 ms endpoint silence,
577 ms to first LLM token, 618 ms to first segment, and 798 ms to first TTS/output
(relative to the recognized endpoint). It emitted no opener. The provider
response path, not the newly added diagnostic logging, dominates this sample.

## Operation receipts

The original run completed, but its verifier incorrectly required every task
result to have a direct caller origin, including chained tasks. It stopped
with an assertion after archive checks. The original verifier is retained as
`verify-original.py`. A separate collection-only job used `verify.py` to record
unrooted deliveries explicitly; it did not redial or change the analyzer gate.
That job exited 0 at 14:31:39. The gate itself remains failed as reported above.

Chat/audio are retained in Call Journal. No real handset call or production
promotion occurred. Full CI for the candidate is still pending.
