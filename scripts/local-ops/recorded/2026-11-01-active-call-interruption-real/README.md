# Active Call real interruption review

`call.sh` is the exact guarded Mac-runner body for the owner's one requested
real call on f1fd9a6, `out-mvxr0ein-wwqh`. The receipt already exists; do not
replay the historical job. It preserves the original prompt, greeting, voice,
recipient and profile, and verifies recording, Aria route and idle state.

`analyze-interruptions.py PRIVATE_ARCHIVE_DIRECTORY` reads a verified local
archive and writes numeric interruption evidence next to it. It does not print
transcripts. Use only explicit speech-over-playback cuts for the primary
comparison. Non-barge playback cancellations are retained separately; a tool
result cutting an answer must not be timed from an unrelated earlier VAD open.
The broader historical 14-cut figure included three non-barge cancellations;
the matched previous-call population is 11 explicit barge-in cuts.

Raw archives, transcripts, private prompts and credentials stay outside Git.

`publish-review.sh` ships the sanitized retrospective and appends the call
comparison to ISS-095. It preserves other issue history and owner feedback.
