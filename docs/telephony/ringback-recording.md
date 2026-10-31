# Carrier ringback in Call Journal

The SIP adapter preserves the carrier's actual ringing/early media in the
Call Journal recording. It does not synthesize a ringtone or play sound on the
Mac. A carrier that sends only a SIP 180 without audio leaves the recording
silent until audio arrives.

The adapter accepts early RTP only after a valid SIP 180/183 SDP offer for the
configured G.711 codec and an allowed media address. The negotiated source IP
and port must match. Early send-only media is supported; the final answer still
requires bidirectional media. The answered RTP stream starts its own packet
sequence/SSRC tracking, so a different early-media source cannot corrupt it.

Early audio goes to `ringback.wav` with absolute timestamps in `ringback.json`.
It is never forwarded to ASR and cannot release the greeting. The existing
conversation tracks and response-latency calculations are unchanged.

Enable discovery in the experimental Call Journal:

```sh
journal ... -adapter-records /data/adapter
```

The directory contains the adapter's per-call recording folders. Once a call
finishes, the journal overlays its ringback on the caller channel before answer,
aligning the adapter clock to the conversation clock. Playback, waveforms and
normal WAV downloads include it, with an “Includes recorded carrier ringback
before answer” label. The raw engine recording remains available unchanged via
`audio.wav?raw=1`.

The archive copies both ringback files alongside the original recording and
includes them in its size/hash manifest. Playback continues to include ringing
after the working recording directory expires. Previously discarded ringback
cannot be recovered from older calls.

Tests cover both codecs, a separate early-media peer/SSRC, duplicate packets,
source restrictions, ASR isolation, clipping at answer, clock alignment,
resampling, and playback after source deletion. No real call is required for
these tests.
