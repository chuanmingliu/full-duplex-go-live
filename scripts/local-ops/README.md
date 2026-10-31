# Mac runner and Codex operation scripts

These are the shell operations used for the `codex/telephony-latency` work, together with the small Python helpers that submit them. The Mac queue transports jobs; the job body executes on the Linux box. Running these scripts is separate from committing or pushing them.

Chat commands select a backend explicitly: **`call`** uses the original GoLive
configuration, **`call sim`** runs the simulator, and **`call gpt-live-1`** uses
Speko. Follow the [mode routing rules](../../CALL-OPERATIONS.md#chat-command-modes);
the last call's backend does not change the next plain `call`.

## Reusable local tools

| File | Runs on | Purpose |
| --- | --- | --- |
| [runner.sh](runner.sh) | Mac | Processes `.runner/in/*.job`, transfers declared files, runs the shell body over SSH, and saves output/completed jobs |
| [enqueue.py](enqueue.py) | Mac | Checks a reviewed job hash, recent runner heartbeat, empty queue and unused job name before publishing the job atomically |
| [stage-job.py](stage-job.py) | Mac | Converts a large shell operation into a short runner wrapper and a separately transferred, hash-checked body |
| [probe-speko-latency.py](probe-speko-latency.py) | Mac | Replays private 8 kHz caller audio through an idle local Speko bridge or the Router Realtime endpoint without dialing; records timed audio/transcript events |

### Speko replay without dialing

This diagnostic requires Python and `websocket-client`, a running local bridge,
its private bearer-token environment file, and a private call template containing
`instructions` and `greeting`. It always uses `marin`. It never contacts the SIP
adapter or changes the saved call template. An occupied bridge rejects the probe.

```bash
python3 scripts/local-ops/probe-speko-latency.py \
  --call-template /private/tmp/speko-call-template.json \
  --input-wav /private/tmp/caller.wav \
  --input-offset 6.186362 --seconds 90 --ring-seconds 8 \
  --output-dir /private/tmp/speko-replay-unique
```

Choose the offset from that recording's SIP answer; the value above is only an
example. The output directory must not already exist. It contains **private**
input/output JSONL events with base64 G.711 audio and transcript fragments, plus
a metadata summary. Keep it outside Git. The first-signal measurement uses RMS
300 and a monotonic clock from opening release. It is not a content, SIP, or
handset latency measurement. Recorded caller audio does not adapt to new replies,
so this is a fixed replay, not a conversational quality evaluation.

Use `--instructions-suffix /private/tmp/policy.txt` for an isolated prompt
experiment. Compare the same input and retain both instruction hashes. The
suffix is applied to the probe only, never to the saved prompt or running service.

`runner.sh` comes from the local `~/golive/runner.sh`. The repository copy requires an explicit `BOX` and defaults its data directory to `~/golive`, even when launched from this checkout. The existing installed runner was not overwritten or restarted. [provenance.json](provenance.json) records the source and small portability changes.

Use the existing runner when it is already active. For a new installation, configure and start one instance in a Mac terminal:

```bash
export BOX='user@box'                 # actual SSH destination or SSH config alias
export GOLIVE_RUNNER_HOME="$HOME/golive"
bash scripts/local-ops/runner.sh
```

The SSH control socket defaults to `~/.ssh/golive-runner.sock`; `GOLIVE_RUNNER_SOCKET` can override it. Python helpers accept `--runner-home` or the same `GOLIVE_RUNNER_HOME` environment variable. Use one operator/enqueuer per queue: the empty-queue check is not a distributed lock.

### Small reviewed job

Create a uniquely named `.job` outside the repository. For example, this only reads service health on the box:

```bash
cat > /tmp/golive-health-check.job <<'JOB'
#timeout 30
set -eu
curl --fail --silent --show-error http://127.0.0.1:8765/healthz
JOB
```

Read the entire job and record its checksum, then submit those exact bytes:

```bash
cat /tmp/golive-health-check.job
shasum -a 256 /tmp/golive-health-check.job
python3 scripts/local-ops/enqueue.py /tmp/golive-health-check.job REVIEWED_SHA256
```

`REVIEWED_SHA256` is a placeholder for the digest you inspected. Use a new operation name for a new attempt. The checksum binds the reviewed bytes; it does not determine whether their commands are appropriate.

Output appears first at `~/golive/.runner/out/golive-health-check.part`, then at `golive-health-check.txt`; the input moves to `.runner/done/`. A stale heartbeat or reused name is an instruction to inspect existing state, not to delete the receipt and retry.

### Large body or release payload

Large inline SSH commands can exceed command-line limits. `stage-job.py` preserves the original body and emits a small wrapper that reads, hashes and executes the same opened bytes:

```bash
python3 scripts/local-ops/stage-job.py /tmp/reviewed-operation.sh /tmp/new-operation.job \
  --remote-home /home/USER/golive
```

The operation must contain exactly one `#timeout` directive. Existing `#pull` directives are carried into the wrapper. Move any `#push` directives out of the body and pass payloads explicitly:

```bash
python3 scripts/local-ops/stage-job.py /tmp/reviewed-deploy.sh /tmp/new-deploy.job \
  --remote-home /home/USER/golive \
  --push /tmp/release.tar.gz /home/USER/golive/release.tar.gz
```

Preparation creates `.body.sh`, `.job` and `.review.json` siblings. It does **not** copy or enqueue anything. Inspect the body, wrapper and review record. Copy the body and any payloads to the indicated `local_runner_body` / `local_runner_source` paths under `~/golive`, confirm their hashes, then use `enqueue.py` with the wrapper's reviewed hash. The remote staging directory must already exist. Payload jobs must verify payload hashes themselves before installation.

The inherited runner logs transfer failures but still attempts the body. Check push/pull receipts, process results and expected artifacts, not just the final exit line. A timeout stops the local SSH process and does not prove that a remote operation or phone call stopped. Never automatically repeat a dial or deployment after an uncertain result.

## Direct Realtime model comparison

Use `--router-model gpt-realtime-2.1-mini` with the replay command above to
connect to `wss://router.speko.dev/v1/realtime` using the private Speko key.
Check the authenticated Router catalog before choosing a model. The harness
requires the returned session model to match; it does not silently fall back.
This mode does not use, restart, or reconfigure the local bridge or SIP adapter.

The Router Realtime route uses 24 kHz PCM. The harness interpolates the same
8 kHz recording to that rate; its JSONL audio is then PCM16, **not G.711**.
`summary.json` identifies encoding, rate, model, and measurement boundary.
An unsolicited initial `session.updated` is ignored until the supplied
instructions are acknowledged. `--listen-first` omits the opening request
for a separate caller-first scenario; it is only valid in Router mode.
`--vad-silence-ms` selects a 100–1000 ms end-of-speech pause for Router tests
(default 300). The harness verifies the accepted PCM format and VAD setting.

After the replay finishes, derive diagnostic caller-to-reply gaps with:

```bash
python3 scripts/local-ops/analyze-realtime-replay.py /private/tmp/speko-replay-unique
```

The analyzer requires the completed 24 kHz PCM capture. It joins each VAD-stop
notification to the next response created before another VAD-stop, finds the
last voiced input frame in that interval, and detects a packet with output
signal. Unmatched observations remain invalid. This is a diagnostic heuristic,
not the handset gate or a semantic-turn scorer.

Router measurements are packet arrival observations without paced speaker
playout. The first signal may belong to a greeting or acknowledgment. They
cannot establish handset delay, interruption-to-silence, or a matched latency
improvement over an already paced bridge. Integration with the SIP path still
requires PCM/G.711 conversion, paced output, cancellation/truncation of unheard
audio, answer-gated opening, and call termination.

## Exact operation snapshots

[recorded/2026-10-31](recorded/2026-10-31) contains the successful operation bodies used during this work. Their bytes match the original temporary scripts, with original names, sizes and SHA-256 hashes in [manifest.json](recorded/2026-10-31/manifest.json).

| Snapshot | What it did |
| --- | --- |
| [real-call-f842928.sh](recorded/2026-10-31/real-call-f842928.sh) | Checked idle health, running binary/profile, saved prompt/voice/greeting and recording; placed one call to the locally configured number; saved a dial receipt |
| [collect-real-out-mvw03ge0-j0y3.sh](recorded/2026-10-31/collect-real-out-mvw03ge0-j0y3.sh) | After the owner said **done**, bound the selected call/session, verified the five permanent archive files and collected the strict server latency report |
| [simulate-quotation-f842928.sh](recorded/2026-10-31/simulate-quotation-f842928.sh) | Reserved and ran the quotation scenario, verified runtime and archive state, collected reports and checked preserved settings |
| [simulate-coverage-f842928.sh](recorded/2026-10-31/simulate-coverage-f842928.sh) | Ran the coverage scenario with the same runtime, recording and evidence checks |
| [preflight-9547d22.sh](recorded/2026-10-31/preflight-9547d22.sh) | Bound the prior `f842928` services/settings and 16 archived calls before release |
| [deploy-9547d22.sh](recorded/2026-10-31/deploy-9547d22.sh) | Verified full CI and release payload; preserved runtime settings/archives; installed and warmed the release with rollback handling |
| [verify-9547d22.sh](recorded/2026-10-31/verify-9547d22.sh) | Checked actual executables, Journal/UI evidence, saved configuration, Talker availability and 80 archived files after release |
| [publish-notes-36e638e.sh](recorded/2026-10-31/publish-notes-36e638e.sh) | Published the three reviewed note files with rollback on mismatch and appended a checked deployment summary to Desk |

These are **historical snapshots, not generic current commands**. They deliberately retain the reviewed LAN addresses, box paths, build IDs, receipt paths and archive fingerprints. They depend on private runtime files, prior receipts, simulator state and release bundles that are not committed. Several now refuse execution because those attempts completed or the running state changed. The post-release note update also means the earlier release payload's note hashes no longer describe the current notes.

For another operation, make a new reviewed job, bind the actual current state and use a new receipt/name. Do not simply replace a version string or remove a failed guard. `.sh` identifies a shell body here: runner `#push`, `#pull` and `#timeout` lines are comments to ordinary Bash and only the runner interprets them. Historical snapshots are stored without executable mode.

Raw recordings, transcripts, private prompts, phone numbers, credentials, `.env` contents and release binaries are excluded. The snapshots read private values locally when needed and compare hashes without embedding those values.

## Existing box-side entry points

- [phone-brain.sh](../phone-brain.sh): real calls, simulated calls, recording settings, service lifecycle, Journal, archive and Desk commands.
- [sim-brain.sh](../sim-brain.sh) and [sim-bundle.sh](../sim-bundle.sh): simulator operations and packaging.
- [desk.sh](../desk.sh): authenticated Desk access; use `DESK_AS=codex` for Codex operations.
- [call-latency.sh](../call-latency.sh): server-side latency analysis.
- [handset-capture.py](../handset-capture.py) and [handset_latency.py](../handset_latency.py): separate acoustic capture and measurement.

Follow [CALL-OPERATIONS.md](../../CALL-OPERATIONS.md) for the **call → done → analyze → fix → CI → deploy → verify** workflow, and [HANDSET-LATENCY.md](../../HANDSET-LATENCY.md) for actual handset measurement. Server recordings and simulator metrics do not by themselves prove the physical handset target.
