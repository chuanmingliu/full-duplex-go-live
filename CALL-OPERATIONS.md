# Operating the go-gpt-live-1 call improvement loop

Run on the Linux box from `~/golive-telephony`. Use the existing Mac runner queue for the commands below. The active source repository is full-duplex-go-live, with telephony-realtime as the deployment reference. The project emphasis is the Go ASR/LLM/TTS full-duplex simulation and recognizer-owned turn endings.

## Before a call

```bash
scripts/phone-brain.sh record
scripts/phone-brain.sh journal status
scripts/phone-brain.sh archive status
scripts/phone-brain.sh desk status
DESK_AS=codex scripts/desk.sh me
```

`record` reports a **setting**, not the live process. Enabling it is `scripts/phone-brain.sh record on`, followed by the normal idle-only restart. At launch that setting exports `GOLIVE_STORE_DIR`, `GOLIVE_REALTIME_STORE_CALLS=1`, and the retention setting. Check those three names only in the running process environment if live confirmation is needed; never print its entire environment. The current script defaults to `CALLS_TTL_HOURS=720` for the working store. The journal's `/api/overview` `recording` flag only proves that its configured store directory exists.

The journal normally binds the LAN address, e.g. `http://192.168.71.9:8790`, **not localhost**. Read its configured address from `journal status`. Its current latency target defaults to **1200**, reaffirmed on 2026-10-29. Earlier 1000 ms trials remain historical evidence. The strict latency gate has a separate explicit `--target-ms 1200`.

## After a call: prove text and both audio channels exist in the permanent archive

For the owner's real-call workflow, `call` places a real call and `call sim`
uses the simulator. After a real call, wait for the owner's **done** message
before beginning the deep review, fixes, GitHub push and box deployment.
An ended-call status alone does not replace that signal. Keep the selected
call ID throughout the review so a newer call cannot silently replace it.

The journal watches every 20 seconds once the service log has been quiet for 8 seconds. It archives only ended calls and only copies audio once `session.json.closed_at` is set. Poll the selected call after it ends, with a bounded wait; do not equate a temporary recording or HTTP audio availability with archival completion.

Set `TASK_CALL` to the returned `out-...` call ID and `TASK_JOURNAL` to the confirmed journal base URL. This read-only verifier triggers the journal's normal GET refresh, checks the selected manifest and its file hashes, validates the WAV and confirms both transcript speakers without printing their words:

```bash
TASK_CALL='out-REPLACE' TASK_JOURNAL='http://192.168.71.9:8790' python3 - <<'PY'
import array, hashlib, json, os, re, sys, urllib.request, wave
from collections import Counter
from pathlib import Path

call_id = os.environ['TASK_CALL']
assert re.fullmatch(r'[A-Za-z0-9_-]{1,80}', call_id), 'invalid call id'
url = os.environ['TASK_JOURNAL'].rstrip('/') + '/api/calls/' + call_id
with urllib.request.urlopen(url, timeout=15) as response:
    data = json.load(response)
call, archive = data['call'], data.get('archive', {})
assert call['id'] == call_id, 'wrong call'
assert call.get('ended_at') and not call['ended_at'].startswith('0001-'), 'call still open'
assert archive.get('archived') and archive.get('audio'), 'permanent audio not ready'
root = Path(os.environ.get('ARCHIVE_DIR', str(Path.home() / 'golive-archive'))).resolve()
directory = Path(archive['dir']).resolve()
assert directory.is_relative_to(root), 'unexpected archive path'
manifest = json.loads((directory / 'manifest.json').read_text())
assert manifest['id'] == call_id and manifest.get('audio'), 'manifest incomplete'
required = ('audio.wav', 'transcript.txt', 'call.json', 'session.json', 'golive.log')
for name in required:
    expected = manifest['files'].get(name)
    assert expected and expected['bytes'] > 0, name + ' missing from manifest'
    path = directory / name
    assert path.stat().st_size == expected['bytes'], name + ' size mismatch'
    digest = hashlib.sha256()
    with path.open('rb') as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b''):
            digest.update(chunk)
    assert digest.hexdigest() == expected['sha256'], name + ' checksum mismatch'
saved = json.loads((directory / 'call.json').read_text())
session = json.loads((directory / 'session.json').read_text())
assert saved['id'] == call_id and saved['session'] == manifest['session'], 'identity mismatch'
assert session['id'] == saved['session'], 'session identity mismatch'
assert session.get('closed_at') and not session['closed_at'].startswith('0001-'), 'session not closed'
speakers = Counter(line.get('who') for line in saved.get('transcript', []) if line.get('text', '').strip())
assert speakers['caller'] and speakers['assistant'], 'two-sided conversation not present'
assert manifest['transcript_lines'] == len(saved.get('transcript', [])), 'transcript count mismatch'
nonzero = [False, False]
with wave.open(str(directory / 'audio.wav'), 'rb') as wav:
    assert wav.getnchannels() == 2 and wav.getsampwidth() == 2, 'expected stereo PCM16'
    assert wav.getnframes() > 0 and wav.getframerate() > 0, 'empty audio'
    duration = wav.getnframes() / wav.getframerate()
    while chunk := wav.readframes(16000):
        samples = array.array('h', chunk)
        for side in range(2):
            nonzero[side] |= any(samples[side::2])
assert all(nonzero), 'one audio channel is entirely silent'
print(json.dumps(dict(call=call_id, build=manifest['build'], complete=True,
                     files_verified=len(required), audio_seconds=round(duration, 3),
                     caller_lines=speakers['caller'], assistant_lines=speakers['assistant'],
                     audio_channels_with_signal=sum(nonzero))))
PY
scripts/phone-brain.sh archive verify
```

The channel check proves non-silent signal, not intelligibility or acoustic end-to-end latency. The permanent WAV is caller on the left and assistant on the right. The raw working store is `~/golive-calls/<session>/{input.pcm,output.pcm,session.json}`. `session.json.history` is the model's retained history and may be trimmed; the full journal transcript is `call.json.transcript` plus `transcript.txt`.

`archive verify` verifies every **listed** file against the manifest; by itself it can pass an archive containing only text. The explicit five-file/audio/transcript checks above establish selected-call completeness. `archive status` is only an aggregate inventory. `archive mirror DEST` copies to an already established backup destination without deletion; do not invent a destination. Audio, session record and raw log are written once; parsed text is refreshed when the journal's reading changes. All are on one local disk unless mirrored.

Useful read-only endpoints, consumed through a filter rather than dumped:

- `GET /api/overview`: extract `recording`, `retention_hours`, `archive.calls`, `archive.with_audio`, `target_ms`, and the selected call's IDs/flags.
- `GET /api/calls/<call>`: `archive.archived`, `archive.audio`, `audio.available`, transcript **counts**, `retrospective != null`. Full payload contains conversation text.
- `HEAD /api/calls/<call>/audio.wav?raw=1`: HTTP 200, `Content-Type: audio/wav`, `Content-Length > 44`; availability only, not archival proof.
- `GET /api/calls/<call>/audio.wav?side=caller|assistant`: mono export when explicitly needed for analysis. Avoid routing audio bytes into runner stdout.

## Keep the external journal current

Retrospectives and issues are repository files shipped to the box, not journal write APIs:

- `journal/retros/<call-id>.md`: call/build/time, real vs simulated, sample count, first-audio and first-content p50/p95/max, strict gate result, recording/archive verification, observed cause, change, next measurement. No transcript, phone numbers, tokens, or personal quote details.
- `journal/issues.json`: preserve the `issues` array and existing IDs; update/add `id`, `title`, `area`, `status`, `found`, `found_on`, `found_call`, `symptom`, `cause`, `fix`, `commits`, `verified`, `verified_call`, `effect`. A unit test is not live latency verification.
- `journal/decisions.json`: maintain only when a decision changes. Entries use `rank`, `title`, `gain`, `basis`, `cost`, `guard`, `undo`, `needs`, `status`.

Retros/issues are read from disk on each HTTP request, so shipping those files updates the UI without restarting an active call. Verify `retrospective` exists and the intended issue ID/status via filtered GET `/api/calls/<call>` and `/api/issues`. `/api/calls/<call>/feedback` POST/PUT is the **owner's** rating/note; do not fabricate owner feedback to store agent findings. GitHub gets sanitized repository notes only, never the archive or raw call payloads.

`scripts/sim-retro.py`/`sim-brain.sh retro` use the shared latency analyzer, preserving zero-ms cached openers and showing first content separately with nearest-rank p95. Use the selected call's complete session log with `scripts/call-latency.sh ... --gate --target-ms 1200 --metric both --min-turns 20`, separately labelled from receiver/handset latency. Do not filter out tool-delivery lines: their `origin_turn`, result `turn`, task ID and monotonic `origin_to_result_start_ms` allow a quotation result to be joined to the original caller. The caller's pause is added once; a cached result's zero-ms content is not a zero-ms caller response. Missing/ambiguous links remain failures, and historical wall-clock reconstructions are diagnostic estimates only.

The journal's content panel uses explicit output-presence flags and the same handoff relationship. Its first-audio charts include fillers; first content excludes them but can still be a generic lead-in. Linked tool-result latency is measured separately even when the original turn already has content. Content/both gates also check this result p95; tool results do not become extra caller samples and need no separate minimum of twenty results. Older saved call details can be recomputed from archived logs without rewriting their source archive. Legacy overview figures may retain the historical grouping until that log is reindexed. Neither panel certifies the physical phone leg.

## Desk integration

`DESK_AS=codex scripts/desk.sh me` safely reads the identity and owner control switches; never use the default `claude` identity. `scripts/phone-brain.sh desk verify` verifies the append-only note chain. `GET /api/calls` on the desk, with the Codex token read from its file internally, lists **simulated** calls paired via `~/golive-sim/data/dials.jsonl`; `GET /api/calls/<run>/report` retrieves their simulator report. Real calls belong in the journal. These APIs do not independently persist call audio.

The desk reads the journal live. Repository retro updates and recording verification require no messages to Claude, no note posting, no task closure, and no owner pause/mode changes. Any approved future work-log entry should contain only IDs, build hashes, stage names and numeric outcomes; never pipe raw call/service logs into it.

Sources: `TELEPHONY.md:536,627`; `scripts/phone-brain.sh:88,251,472,881,1102`; `internal/journal/archive.go:25,227,294,345,396`; `internal/journal/server.go:136,160,214,379,424,520,715`; `internal/store/store.go:56,256`; `scripts/desk.sh:1`; `internal/desk/calls.go:255`.

## Release loop

1. Read the running `/healthz` version as the build identity; a bundle VERSION file can be newer than the running binary. Require idle service before a restart.
2. Capture a baseline with the same stored prompt and scenario. `call show` prints only the prompt hash/length. `call sim RUN_ID` is the repeatable simulated route; plain `call` dials the configured real recipient. Reserve immediately before dialing and never retry a dial with an unknown outcome.
3. Keep real, simulated-PBX and loopback measurements separate. Use the simulator's valid `application_audio_boundary` observations for received first audio. Do not subtract its debounce blindly: current receiver versions backdate that boundary. Neither loopback nor a SIP simulator proves GSM/handset latency.
4. Reproduce a defect before changing it. Run the relevant tests, race checks and the latency-report tests. Push a task branch and inspect CI before deploying. Keep private recordings, prompts and raw transcripts off GitHub.
5. Build an identifiable bundle and retain the actual running executable plus replaced files for rollback. Preserve the running process options/environment and journal paths internally without printing credentials. `phone-brain.sh restart` also restarts the journal and any enabled browser demo: require both main service and demo to be idle. Check the running version, recording configuration, completed warming in one stable process, and journal archive/retention settings. Preserve the CRM, simulator and retained data. A failed startup may be rolled back once the service is proven stopped; an unknown or unresponsive process is not evidence that it is idle.
6. Repeat the same scenario, verify the complete archive as above, publish the sanitized per-call retro and issue status, and verify the journal reads the new notes. The desk work log may contain build/call IDs and numeric outcomes; never pipe raw logs into it.
7. A first-audio pass is distinct from content latency. Report both; leave any unverified phone leg or unobserved performance effect explicit. Current content timestamps mark answer audio emitted by the player, not synthesis readiness or an inferred opener duration. Older content estimates are not directly comparable at interruptions or when audio waits behind an opener. The receiver still measures first audio, including openers; it does not establish when meaningful answer content reaches a handset.

When a release changes CRM code, replace and restart the CRM explicitly:
the ordinary phone-service restart retains an already-running CRM. Back up
its actual running executable and preserve its exact arguments, environment
and data directory. Allow its HTTP shutdown to drain before starting its
replacement; the launcher's short stop wait is not proof that shutdown has
finished. Verify the new CRM version and executable hash. Verify a journal
update by its running executable hash: the journal's overview build is the
phone service's build, not the journal binary's identity.

Startup banner verification must use the first space-delimited field of the
bundle's VERSION file, exactly as phone-brain.sh does. New bundles may put a
full commit there while /healthz uses the short build version; verify both
against their intended identifiers rather than assuming they are identical.
