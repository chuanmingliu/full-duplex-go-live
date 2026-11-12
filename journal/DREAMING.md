# Dreaming tab

The tab shows live automatic reviews and configurable setup, followed by
expandable reviewed examples.

## Reviewed examples

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

## Automatic review and live status

The Dreaming tab now reads live jobs, capture status, activity checks, model
progress events and candidate lessons from the memory service. Reviewed
workflow examples remain in an expandable section beneath the live view.
The page polls every three seconds. It links each job to the original call,
where Skills / MCP records actual subsequent selection and injection.

Automatic capture and background review default to enabled with a 30-second
idle delay. Simulator calls default to included and keep their simulation
provenance. Setup in the tab controls these three settings. Updates use a
revision check and persist in SQLite across restarts. Disabling pauses capture
and reviews. Re-enabling catches up calls completed since initial setup.
Initial setup records a cutoff so existing historical calls stay outside the
automatic catch-up window.

The collector checks completed archives every five seconds while engines are
idle. Recorded calls wait for their permanent audio archive. Text-only calls
can proceed when recording is disabled. Each call gets one deterministic job
ID and an immutable evidence snapshot, including owner feedback present when
captured. Later feedback can be submitted through the memory API and reviewed
in a new explicitly queued job.

The worker checks all configured engine health endpoints once per second.
Every endpoint must report zero active sessions continuously through the idle
delay. A busy or unavailable endpoint resets that delay. Activity checks also
run during review and cancel/defer it when a call starts. The remote provider
may take time to observe cancellation. Successful review commits candidate
lessons and the completion event together. Failed jobs remain visible and
have an explicit Retry review button. Publication uses the existing reviewed
memory API.

### Connection setup for deployment

Configure the memory service with the existing model endpoint, model name,
credential file and database, plus the Journal origin and voice-engine origins.
The dreaming Compose overlay accepts these environment variables.

```sh
GOLIVE_DREAM_JOURNAL_URL=http://192.168.71.9:8790
GOLIVE_DREAM_ENGINE_URLS=http://127.0.0.1:8765,http://127.0.0.1:9080
```

List all voice engines sharing the agent resources. Every listed endpoint must
be available. The default engine is `http://127.0.0.1:8765`.

The Journal connects to the loopback memory service by default. Supply the
existing skills admin credential file to its backend through
`GOLIVE_DREAM_ADMIN_TOKEN_FILE` or `-dream-admin-token-file`. An alternate
service origin uses `GOLIVE_DREAM_MEMORY_URL` or `-dream-memory`. The credential
stays server-side. The proxy permits live reads, revision-checked setup updates
and failed-job retry. It checks the browser origin for writes.

The automatic review setting is standing authorization to release new completed
calls for the configured review model. Text evidence can reach that configured
model. Original audio remains in the archive. Operator-led call analysis still
uses the owner's `done` workflow.
