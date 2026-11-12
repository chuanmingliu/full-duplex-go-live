# Dreaming tab

The Call Journal route `#/dreaming` displays a reviewed record from
`dreaming.json` in the configured notes directory. `GET /api/dreaming` reloads
the file on each request. The view presents each documented dream as a workflow,
with individual stages, an all-stages view, expandable technical evidence and
links to related calls.

The repository includes a fictional schema example in `dreaming.example.json`.
Copy it to `dreaming.json` to try the viewer, or publish a reviewed local record.
The detailed call-derived record is private, ignored by Git, and deployed
separately. The header shows the record's timestamp. Runtime traces
continue to come from each call's existing Skills / MCP section. Operators update
the reviewed record when dream or publication milestones change.

## Updating the record

- Set `version` to 1 and `recorded_at` to the actual review timestamp.
- Give each run a stable ID, title, summary, status, origin, facts and stages.
- Give each stage a unique ID, title, status and summary. Add explanatory details,
  label/value facts, evidence checks, labeled text blocks and related call IDs.
- Preserve the distinction between synthetic evidence, real-call observations,
  reconstructed requests and unverified behavior. Record failed validations and
  unpublished candidates alongside successful outcomes.
- Publish only reviewed, sanitized material. This endpoint serializes the declared
  presentation fields and does not read the private memory database, credentials,
  raw model snapshots or recordings.
- Validate the file with the Journal tests before shipping it through the normal
  reviewed notes workflow. Keep prior versions in Git. After publication, use
  Refresh and verify the timestamp and affected stages.

The API limits input to 2 MiB, 32 documented runs and 20 stages per run. Invalid
records produce a visible load error. An absent record produces an empty view.
The endpoint supports GET only and does not trigger dreams, publish skills or
place calls. Existing installations can omit the file.
