# Real backchannel-call review operations

Historical, read-only analysis bodies for `out-mvzfq218-qbqy`, after the owner
said **done**. Do not replay these attempt-bound jobs.

`analyze.job` verifies its three Python payloads, waits for call/archive closure,
checks all five archive files and both audio directions, and exports numeric
latency, interruption and detector evidence. Its generic analyzer payloads are
`scripts/call_latency.py` and the earlier interruption review's
`analyze-interruptions.py`; their exact staged hashes are in the job.
`details.job` correlates each assist with its own utterance and separates CRM
calculation, tool execution and delivery waits. Neither exports conversation
text. Private working copies remain on the box.

`publish.job` creates only the selected call's retrospective and verifies that
Call Journal reads it. It does not alter owner feedback, runtime configuration,
services or call routing. No dialing or deployment occurs in these jobs.
