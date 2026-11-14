# Session-linked rendered request recording

Set `GOLIVE_REQUEST_LOG_DIR` to a private persistent directory in the voice
engine's environment. Restart the engine through the normal idle-only release
workflow. The shared OpenAI-compatible adapter then records each physical
chat-completions request, including DeepSeek and other providers using that
adapter. The directory is independent of the audio store and its expiry policy.

```sh
GOLIVE_REQUEST_LOG_DIR=/home/chuanming/golive-request-records
```

Each session has a directory containing one randomly identified directory per
request attempt.

```text
<root>/<session_id>/<request_id>/request.json
<root>/<session_id>/<request_id>/metadata.json
```

`request.json` contains the exact UTF-8 HTTP request body after system prompt,
conversation, tools, skill rendering and provider-specific fields are assembled.
Hedged copies and retries have distinct request IDs. Prefix-warming requests
carry the session ID and `purpose=prefix_warm`. Normal requests also carry the
turn ID, response ID and `purpose=turn`. Requests outside a voice session use
an explicit `unscoped` directory.

Metadata includes the provider, model, endpoint origin/path, lane and key-slot
number, request byte count, SHA-256, UTC timestamps, duration, HTTP status,
provider request ID and completion outcome. Authorization headers are excluded.
The request ID also appears in stream timing logs when debug logging is enabled.

Directories use mode 0700 and files use mode 0600. Initial request bytes and
metadata are synced and atomically published together before HTTP dispatch.
Completion replaces metadata atomically. A `prepared` outcome after a crash
means final transport outcome is unknown. Storage errors produce a metadata-only
error log and the voice request continues. Synchronous initial persistence adds
local disk I/O before request dispatch.

Recording starts when the environment setting is enabled. Retention is managed
on this persistent directory independently. The recorder preserves rendered
prompt content verbatim, so access should follow the existing private call
archive controls.

The Journal's **Requests** tab lists recent recorded sessions and supports
searching or opening a session ID directly. Select an individual request to
inspect its rendered system prompt, ordered conversation, injected memory skill
text and provenance, metadata, and exact raw JSON. The detail view checks the
recorded byte count and SHA-256 and offers a raw JSON download. Skill text comes
from that request's saved body, so later catalog edits do not change the view.

Configure the Journal with `-request-log-dir /home/chuanming/golive-request-records`
or `GOLIVE_REQUEST_LOG_DIR`. The standard phone launcher passes this directory
explicitly. The read-only endpoints under `/api/requests/sessions` use the
Journal's existing access boundary and send `Cache-Control: no-store`.
Lists have 50 rows per page. Individual bodies up to 64 MiB can be displayed.
The `#/requests/<session_id>/<request_id>` route can be bookmarked. Refresh
reloads the selected session and request, including newly completed metadata.
