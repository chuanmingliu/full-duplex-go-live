# golive Call Journal

`codex/service-call-journal` maintains the call evidence browser and permanent
archive. It joins call logs, transcripts, audio, latency measurements and
operator notes without participating in the live media path.

## Functionality

- Shows calls, turn timing and stage-by-stage latency evidence.
- Displays recorded audio, transcript events and interruption outcomes.
- Tracks issues, decisions and per-call retrospective notes.
- Archives completed calls and verifies archived files against manifests.
- Displays configured Talker health/playground links and available filler evidence.

Server-side call recordings do not establish when sound reached a physical
handset. Preserve the distinction between server, simulator and acoustic evidence.

## Owned source

| Path | Responsibility |
| --- | --- |
| [cmd/journal](cmd/journal) | Process flags, archival watcher and verification command |
| [internal/journal](internal/journal) | Evidence parsing, timing, archives and API |
| [internal/journal/ui](internal/journal/ui) | Call Journal panel |
| [journal](journal) | Reviewed issue/decision/retrospective notes |

This branch owns Journal functionality and necessary shared-code integration
with coordinated review. Shared engine code remains in this repository but is
not the subject of this workstream.

## Build and empty local instance

Use the Go version in [go.mod](go.mod), from the repository root:

```sh
go test ./internal/journal
go build -o /tmp/golive-journal ./cmd/journal
JOURNAL_DEV_DIR=$(mktemp -d)
/tmp/golive-journal -addr 127.0.0.1:18790 -log /dev/null \
  -data "$JOURNAL_DEV_DIR" -notes journal -target 1200
```

Open `http://127.0.0.1:18790/`. This fresh instance has no call store or archive.
The UI root is also a liveness check; Journal does not expose `/healthz`.
For an existing archive, the explicit integrity check is:

```sh
/tmp/golive-journal -archive /path/to/archive -verify
```

## Inputs, state and deployment

Live inputs are the canonical phone log, call recording store, saved-call file,
simulator dial ledger and reviewed notes. Journal maintains its own state under
`~/golive-journal` and permanent evidence under `~/golive-archive`.
Its observed box listener uses the LAN address on port 8790, not loopback.

Journal has no built-in login. Preserve the established private listener and
access boundary. Back up archives and verify checksums before and after moves;
never discard evidence to make a parser or timing result look cleaner.

The [Journal Compose service](deploy/services/box/core.compose.yml) is staged;
[adoption requires explicit state/log migration](deploy/services/README.md).
For acoustic measurement definitions, see [HANDSET-LATENCY.md](HANDSET-LATENCY.md).
