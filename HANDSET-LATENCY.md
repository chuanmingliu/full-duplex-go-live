# Measure the actual handset response

The box archive measures audio received and sent by golive. It cannot prove
when the caller spoke into a physical phone or heard the phone's reply.
This recorder captures the caller and the phone's speaker through one
nearby microphone, so both boundaries share the same sample clock.

## Record on the Mac

```bash
python3 scripts/handset-capture.py --open
```

The page is served only at the printed `http://127.0.0.1:<port>/` address.
An available port is chosen automatically; `--port` can select a specific
one. The server serves static files; it does not receive audio.
The browser retains audio in memory until it is downloaded. Closing the
page before downloading loses that recording.

1. Put the real phone on speakerphone beside the Mac's microphone. Both
   the caller's voice and the phone speaker must be clearly captured. Keep
   their positions and the microphone device fixed through the call.
2. Confirm that setup in the page and start the recorder **before dialing**.
   Grant microphone access to this local page if the browser requests it.
3. Place the real call through the established `runner.sh` workflow. Keep
   the box's recording enabled too. Enter the actual `out-…` call ID from
   the dial receipt before exporting the local recording.
4. After the conversation ends, stop the recorder and download both its
   WAV and metadata JSON. Keep the original files together. Do not trim,
   resample, normalize, denoise or otherwise replace the original WAV.
5. Say **done** to begin the normal call review. Preserve these private
   files outside Git. Only sanitized numeric findings belong in the
   repository and shared work log.

The page requests microphone echo cancellation, noise suppression and
automatic gain control off, then records the settings actually returned
by the browser. Missing or enabled settings cannot establish unprocessed
capture. Device interruptions, missing frames, context suspensions and a
recording limit make the evidence incomplete; the export must retain those
facts. A browser or device that cannot supply the required evidence may
still produce useful diagnostic audio, but cannot silently pass the gate.

Starting the server does not start the microphone. The recorder does not
dial, start another golive session, or change the deployed service.

## Review the acoustic boundaries

Inspect the complete WAV and identify every caller turn. For each turn,
record sample indices on this WAV's own clock:

- `caller_start_sample`: first caller sample, inclusive.
- `caller_end_sample`: boundary immediately after the final caller sample.
- `first_audio_sample`: first audible assistant sound from the phone,
  including any cached acknowledgment.
- `first_content_sample`: first audible answer content after fillers.
- `quote_result_sample`: for a quotation request, first audible quotation
  result, separately from an explanatory lead-in.

Use the acoustic recording to verify the words and boundaries. Silence
thresholds, ASR timestamps or server logs may help locate a region, but do
not automatically certify who spoke or whether a reply contained the
requested result. Preserve unresolved or interrupted turns as such; do
not omit them to obtain twenty fast samples. Pair each reply with the
actual request instead of the nearest convenient sound.

The annotation JSON binds the call ID, WAV SHA-256, sample rate and frame
count. It explicitly records complete-call coverage, omitted/unresolved
turns and review status. These attestations are review claims; software
checks their consistency, not whether a person actually performed the
review. A completion audit must still inspect the underlying recording.

Each interval is `(assistant_sample - caller_end_sample) * 1000 /
sample_rate`. Do not add the server's endpointing pause again, align to a
different clock, or subtract a guessed phone/network delay. Report
first-audio, answer-content and quotation-result distributions separately.
The strict goal uses at least twenty valid caller samples and a p95
strictly below 1200 ms; equality fails. Always report the maximum too.

The existing box-side `scripts/call_latency.py` remains useful for locating
provider, scheduler and playback delays. Keep its server-output scope
distinct from this physical measurement.

## Validate the reviewed recording

```bash
python3 scripts/handset_latency.py recording.wav capture.json annotations.json \
  --target-ms 1200 --min-turns 20 --json
```

`capture.json` is the unchanged metadata downloaded beside the WAV.
`annotations.json` uses schema `handset-acoustic-annotations/v1`; run
`python3 scripts/handset_latency.py --help` for the complete example and
sample-boundary definitions. Copy the call ID, WAV hash, sample rate and
frame count from the capture metadata, then add every reviewed caller turn.
Mark `quote_requested` explicitly; quotation requests require a separate
`quote_result_sample`. An interrupted or unresolved response cannot be
silently removed from this acceptance sample.

The gate is always active. Exit 0 means the supplied reviewed boundaries
satisfy the numeric gate, exit 1 means valid supplied evidence misses the
target or minimum sample count, and exit 2 means evidence is invalid.
Neither a successful exit nor checked metadata replaces independent
acoustic review. The target and sample count can only be made stricter.

Run `make handset-test` to test recording, file validation and arithmetic
using synthetic audio. Those tests do not access a microphone or establish
real handset performance.
