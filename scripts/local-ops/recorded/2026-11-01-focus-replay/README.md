# Offline speaker-window sensitivity check

Completed-call input from `280fdab1-4078-4627-bf79-4eef8f627719` was replayed
through the deployed CAM++ scoring code in a separate process. No call was
placed, no listener was started, no speaker profile was saved, and production
settings were not changed. The original service was idle before the probe.

The 72 conditions cover eight playback-time utterances, three scoring windows
(800/600/400 ms), and three recorder-to-input-clock offsets (5132/5152/5172 ms).
The eighth utterance is the closing farewell, not an observed interruption.
These are model-window diagnostics, **not simulated SIP calls or handset latency**.

The 5132 ms alignment reproduces all six enrollment voiced-duration and
agreement counts from the real trace. The other offsets reproduce agreement
counts but differ on the first enrollment's voiced duration. None reconstructs
network scheduling, the full echo tracker, or the exact original frame stream.

For the slowest interruption at 15:14:45.139, the 600 ms window advances the
simplified acceptance boundary by 0/200/0 ms across the three alignments;
400 ms advances it by 0/0/0 ms. Shorter windows help some other segments by
100–400 ms, but outcomes depend on alignment and enrollment. One closing
farewell condition loses acceptance within the observation window; another
advances by 1000 ms. There is no independent speech/background ground truth
with which to judge these classifications as correct or incorrect.

**Decision: do not deploy shorter windows or lower confirmation thresholds.**
The real trace's 600 ms speaker-confirmation wait is established; a safe fix
is not. A controlled next comparison needs exact input/label provenance,
caller-versus-background negative controls, and receiver stop boundaries.

Four focused duplex regressions pass in 31.499 seconds: background speech must
not cut playback, 200 ms of caller speech within a longer other-speaker segment
must not count as a takeover, an undecided other-speaker hold stays held, and a
brief other-speaker label must not end a caller's pause. The first local test
attempt was denied access to the default build cache; the successful run uses
`GOCACHE=/tmp/golive-focus-go-cache` and makes no configuration change.

`replay.py` is the exact historical offline probe; `replay.sh` is its runner job.
`receipt.txt` records completion at 15:26:01 Asia/Shanghai. `evidence.json` contains
numeric model scores only. Private raw PCM, transcripts and enrollment vectors
stay on the box. Do not replay this historical job.
