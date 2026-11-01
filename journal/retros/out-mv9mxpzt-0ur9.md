# Call out-mv9mxpzt-0ur9 — 2026-10-15 22:29

## What happened

4.5 minutes, from the box, first call on `f3bd554` (barge-ins judged by the
speaker labels, `focus: veto`), the English show on in the background. 16
answered turns; console p50 644, p95 947, max 1258. 34 answers cut, 20
guesses abandoned, 9 floor raises (the floor tracker is doing its job
against the room; a show is a voice with gaps and does not trigger it).

The owner's verdict: the show still had a huge effect. Right.

## What the fix did, and why it was not enough

The new path worked exactly once: 20 s in, a sound over the answer was
judged "another voice" with the labels 155 ms behind, and the answer carried
on (`barge_ins_kept=1`). Then: **38 sounds judged, 1 kept, the rest "labels
not in yet; waited out"** — coverage 0 to 0.3 after the 900 ms wait. The
labels fell behind as the show went on: 340–480 ms early in the call, 960,
1080, 1200 ms on the utterances judged later, and by the second minute not
there at all inside the window. Two of the show's lines were answered before
the labels came; one was vetoed in time.

The diarizer runs at about real time on this CPU and a show is continuous
speech: it never gets a gap to catch up, and skipping chunks did not keep it
under a second either. The judgment is right; its witness is too slow for
this room.

## What I changed (next build)

The owner's rule, stated on this call: only the primary voice counts; a
second voice is ignored outright. Diarization answers a wider question than
that. A target-speaker service (`edge/tsv_server.py`: CAM++ speaker
embedding, zh/en, ONNX on the CPU, 10 ms a window) scores every 100 ms
window of the caller's audio against an enrolment of the caller's own voice,
taken from their first utterance alone and extended by the next few; the
labels are one window behind the audio however much the background talks.
It speaks the diarizer's wire, so the tracker and every judgment are the
same, and the engine now also judges the *open* utterance as it is said: not
the caller's, and it goes by with no guess, no turn, no interruption, unless
the caller joins in. Measured on this line's archive: the caller's own later
utterances score 0.45–0.68 against a 2 s enrolment, the assistant's voice
0.14–0.21, the show's lines under 0.25 (threshold 0.35). Decision 11,
ISS-035.
