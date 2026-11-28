# Original GoLive Active Call — real call, 2026-11-01

Call `out-mvxo348d-w5pb`, session `sess_a52c7ffea23fb1cce1b7d80e`,
engine `45b5564`, Journal `0c2ba18`. Original saved settings; Active Call
transport. The owner said **done** before this review. No redial or runtime
configuration change was made during analysis.

## Normal responses

| Server-output measure | Samples | p50 | p95 | Maximum |
| --- | ---: | ---: | ---: | ---: |
| First audio, including acknowledgments | 35 | 380 ms | 381 ms | 393 ms |
| Normal first content, quotation-only handoffs excluded | 32 | 963.5 ms | **1078 ms** | 1081 ms |
| Quotation readout, reported separately | 3 | 1017 ms | 1084 ms | 1084 ms |

The strict server gate passes with quotation SLA separate. No missing,
unverified, duplicate or malformed timing records were found; interrupted
answers remain in the population. Four non-speech records are classified
separately. The 380 ms endpoint silence is already included in these figures.
First content excludes the cached opener, but is not an independently reviewed
semantic-completion or handset-audibility measurement.

The six slowest normal responses span 1044–1081 ms. On those turns, engine
first-token stamps are 446–504 ms after the endpoint. Zero first-segment/TTS
stamps describe the cached opener; they do not mean the answer synthesis took
zero time. No normal response exceeded 1200 ms at the server output boundary.
The earlier simulator repeat was 1049 ms p95 over 30 normal responses; different
dialogues and transport conditions prevent a causal comparison.

## Interruptions

Fourteen cuts occurred while the assistant was playing. VAD-open to server
cut decision was **275.5 ms median, 686 ms p95/max**. Eleven were already marked
barge-in at VAD-open: **265 ms median, 680 ms p95/max**, range 116–680 ms.
Three opened before playback was marked active and cut it later; they remain
in the all-cuts population. Fourteen answered records were interrupted.

Every paired VAD-open reports 320 ms accumulated voiced input. Adding that
to the slowest post-detection interval gives about **1006 ms** as a detector-based
diagnostic; accumulated voice is not necessarily continuous wall time, and this
is not an acoustic speech-onset-to-handset-stop measurement.

At 18:12:14.803 (+08:00), the engine held an interruption. ASR partials arrived
at 18:12:15.044 and 18:12:15.482; caller focus and the cut followed at
18:12:15.483. The trace localizes 680 ms before the server cut decision. It does
not identify the exact contribution of the intervening backchannel/focus gates.
At 18:14:07.584, another utterance was initially classified as another voice;
the caller joined at 18:14:08.063 and playback was cut immediately, 563 ms after
VAD-open. Whether that initial classification was wrong requires acoustic review.

ISS-095 remains open. The next optimization target is recognition and caller
confirmation before cancellation, with negative controls for echo, background
voices and backchannels. This call does not justify weakening those safeguards.
There is no Active Call clear-to-audible-stop measurement in these logs; earlier
SIP adapter clear timings must not be substituted for it.

## Quotation calculation, kept separate

Three foreground quote tool executions took **0 / 1 / 2 ms**. Their deterministic
readouts began **903 / 1084 / 1017 ms** after the originating caller's last sound.
Two readouts were subsequently interrupted; start latency does not prove the
complete amount was heard.

CRM recorded 17 completed API calculation/poll cycles at **3004–4509 ms**,
including 16 marked ahead-of-request. These durations include polling and HTTP
overhead; they are not isolated backend calculation times. All succeeded.
The very short foreground tool durations are consistent with prepared/cached
results, not fresh calculations completing in 0–2 ms. Calculation settings were
preserved and these cycles are excluded from the normal-response SLA.

## Recording, hangup and remaining evidence

All five permanent archive files match their manifest hashes. Stereo PCM16 has
signal in both channels for **354.94 seconds**, with **43 caller / 40 assistant**
transcript entries. Signal and transcript counts do not alone prove intelligibility.
The end-call tool requested hangup at 18:14:17.483; the session closed about
6 ms later and the Journal call is ended. The service was subsequently idle.
This establishes server-side teardown, not the exact handset disconnect time.

Actual handset latency remains **unverified**: the owner confirmed recording,
but this call's acoustic WAV and metadata were not available for review when
this report was written. The older UUID recording is a different call and was
not reused. With the owner's assumed +50 ms, normal content p95 would be
**1128 ms**, explicitly an estimate. Save the current recorder's original WAV
and JSON with this call ID to finish acoustic boundary and interruption review.

Sanitized evidence is in
`docs/telephony/evidence/2026-11-01-active-call-real/` on the Codex branch.
Private audio, transcripts, prompts, numbers and credentials are excluded.
