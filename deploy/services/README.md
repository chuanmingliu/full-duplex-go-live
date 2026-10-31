# golive service deployment models

These files describe the intended managed layout. They do **not** mean that the
running native services have migrated. The [service catalog](../../docs/services/README.md)
records observed host state and source ownership. Existing `phone-brain.sh`
service commands remain native-process commands until an explicit cutover.

| Model | Services | Default activation |
| --- | --- | --- |
| `box/core.compose.yml` | Phone, CRM, Journal, Desk; optional quote service, browser demo and edge gateway | None; select `core`, `quotes`, `demo`, or `edge` |
| `box/telephony.compose.yml` | Existing patched Active Call; RustPBX remains external | None; select `telephony` |
| `box/rustpbx.staged.compose.yml` | RustPBX after explicit state recovery and config relocation | None; staged `pbx-migration` only |
| Simulator's own `deploy/simulator/compose.yml` | Simulator plus its isolated Active Call gateway | Managed separately in its source repository |
| `mac/dev.compose.yml` | Optional mock-provider browser development service on loopback port 18080 | None; select `dev` |

The Mac runner, Mac Aria and Apple-accelerated Talker retain their native
supervisors. The box's Aria originator, telemetry service, tunnel and model
runtimes remain external dependencies of these models. Do not replace native
virtual environments with a bind mount into a generic Linux Python image:
interpreter paths, Python ABI, model files and native libraries must be packaged
and tested first. Do not run a second phone stack against the same ports.

## Validate without changing anything

From the repository root:

```sh
python3 deploy/services/validate.py
```

This expands every owned profile with fixture paths and checks default-off
activation, bind mounts, logging, healthchecks and call-state persistence. It
needs the Docker Compose CLI but not a running Docker daemon or credentials.
The validator passes only CLI discovery and temporary-directory environment
settings to Compose, excluding inherited `COMPOSE_*` and model variables. It
also deliberately pollutes those variables and checks that the rendered models
and inactive default profiles remain identical.
It does not prove runtime connectivity, provider readiness, or audio quality.

Validation recorded on 2026-10-31: all four Compose models expanded successfully
with fixture configuration; all six Go services cross-compiled for Linux/amd64.
A Docker image build and container smoke test have not been completed because
the Mac Docker daemon was unavailable. No live service was changed by those
checks.

For an actual private location file, use quiet validation so settings are not
printed. The file contains paths and endpoint addresses, not credential values:

```sh
docker compose --env-file /private/path/core.env \
  -f deploy/services/box/core.compose.yml --profile core config --quiet
```

Always pass `--env-file` explicitly. Do not dump fully interpolated deployment
models into public logs. Never commit actual `.env` files, call data or weights.

## Build an identifiable service image

Use a clean checkout of the reviewed commit. These six Go services share a
repository and can share an image while remaining independent containers.
This Dockerfile deliberately excludes private configuration and journals; a
Dockerfile-specific context allowlist also excludes unrelated `playground/`.

```sh
SOURCE_COMMIT=$(git rev-parse HEAD)
docker build --platform linux/amd64 \
  -f deploy/services/Dockerfile --build-arg SOURCE_COMMIT="$SOURCE_COMMIT" \
  -t "golive-services:$SOURCE_COMMIT" .
docker image inspect --format '{{.Id}}' "golive-services:$SOURCE_COMMIT"
```

Copy the resulting immutable local image ID to `GOLIVE_IMAGE` in the box location
file. On a Mac, exporting/loading the image on the box is a separate step;
verify its image ID after loading. Base images in this build recipe use release
tags; record their resolved digests with the build receipt if reproducible
rebuilding is required. No Compose `up` automatically builds or pulls production
images. Do not use an image's version label as proof of the running source.

## Box core cutover contract

1. Verify the actual native launch arguments, environment **keys and hashes**,
   private profiles, image/executable identities and all state locations against
   the catalog. Resolve any difference before starting a replacement.
2. Copy `box/core.env.example` to a private location and correct every path.
   Confirm the configured UID/GID owns the state. Bind mounts use
   `create_host_path: false`: missing paths must fail, not silently become empty
   data directories. Keep call and business data private (normally mode 0700).
3. Preserve archives, call store, CRM, Desk, quote data, profiles and private env
   files. Check archive manifests before and after. Retain native binaries and
   launch configuration for rollback. There is no `down --volumes` step.
4. Migrate the **saved-call directory** and canonical log deliberately:
   copy the existing `~/golive-last-call.json` to
   `LAST_CALL_DIR/last-call.json`, preserving its bytes and mode; preserve the
   existing `golive-telephony.log` as `LOG_DIR/golive-telephony.log`. Update every
   runner/native caller to use the new host paths (`LAST_CALL`, `LOGFILE`) before
   enabling the new writer. A file-only bind is invalid for the saved call,
   because golive writes a temporary file and atomically renames it. Keep the
   old files and hashes for rollback; do not overwrite them on every restart.
5. Verify Aria `:3000`, telemetry `:8768`, speaker verification `:8032`, and the
   patched telephony path independently. Core healthchecks are liveness checks,
   not proof that these external services or provider accounts are ready.
   The optional `edge` gateway additionally needs ASR `:7860`, TTS `:8024` and
   diarization `:8031`. Match any current custom endpoint/token-file settings.
6. Establish that phone and demo are idle. Stop only the native supervisors for
   the services being moved, using their established lifecycle commands. Verify
   the old processes have exited and ports are free. Avoid `pkill` patterns and
   do not let native keepalive loops respawn beside Compose.
7. Start the selected group, then inspect container status and health. This is
   a production mutation, to be run only after the preceding migration steps:

   ```sh
   docker compose --env-file /private/path/core.env \
     -f deploy/services/box/core.compose.yml \
     --profile core --profile quotes --profile demo up -d --wait
   ```

8. Verify actual container image IDs, private configuration hashes, working
   stores and archive manifests. Warm the providers, then run an isolated
   simulator replay and a separately authorized real call. Check call ID,
   transcript, both audio tracks, interruption clearing and saved-call updates.
   Container liveness does not prove the handset latency goal.

### Existing configuration and container paths

The example locations describe the observed deployment; they are not an
auto-discovery mechanism. The phone model expects `telephony.json` and the demo
expects `tencent-deepseek-minimax.json` below `CONFIG_DIR`. If the live process
uses another filename, preserve that profile and adjust the corresponding
container command. `PROVIDERS_ENV` is mounted as a private file and read by
golive; it is not expanded into Compose output.

Any file path stored **inside** a profile or provider env file must resolve in
the container. Host absolute paths do not automatically exist there. Review
custom token files, caches and other file-valued settings; add narrow mounts
and map paths deliberately. Preserve the host files and their hashes for
rollback. Likewise, existing process environment overrides are not inherited
by containers automatically: carry every relevant override explicitly.

`FOCUS_TOKEN_FILE` is the container path for the phone's
`GOLIVE_FOCUS_TOKEN_FILE`. The example `/edge/api.token` maps to the existing
`EDGE_DIR/api.token` through a read-only directory mount and matches the native
launcher when that token exists. If the configured direct TSV service needs no
authentication, blank is permitted when the profile's `focus_token_file` is also
blank. A blank Compose setting retains a nonempty profile value but prevents a
same-named value in the provider env file from loading; preserve an existing
override explicitly in the location file. Never silently discard a required
token. Do not replace the current `focus_url` simply because the edge gateway
exists.

`CRM_QUOTE_API_BASE` defaults to blank. The CRM applies it only when nonempty,
so blank preserves the persisted `quote-api` configuration's `base_url`.
Copy any existing native URL override into the private location file. Avoid
credential-bearing URLs; secrets belong in the existing private configuration.

The `quotes` profile is for the existing local quotation service. Keep the
current CRM quote backend configuration; do not replace a business backend with
this service merely because the profile exists. The `edge` profile is optional
and does not start or reconfigure model runtimes.

The phone writes its canonical log to the shared log directory for Journal to
parse. Other services write container logs plus their existing application
logs. Container logs rotate; canonical phone and business logs need their
existing archival policy. Do not rotate/truncate the phone log mid-call.

## Independent operations and rollback

After cutover, the same Compose project must own restarts. Do not use the old
native `phone-brain.sh restart`, `journal start`, or `desk start` commands on
migrated services. Dial-only commands can remain once their file paths and
service URLs have been verified. Example independent Journal replacement:

```sh
docker compose --env-file /private/path/core.env \
  -f deploy/services/box/core.compose.yml up -d --no-deps journal
```

For rollback, establish idle state, stop only the migrated containers, reconcile
the latest saved-call and canonical log with the retained native paths, restore
any changed private settings, then start the retained native supervisors.
Do not revert business state or archives to an older snapshot after new calls.
Do not delete volumes, image rollback references, or retired containers until
state restoration has been checked.

## Mac development

The optional mock service uses no provider credentials and cannot dial a phone:

```sh
SOURCE_COMMIT=$(git rev-parse HEAD) docker compose \
  -f deploy/services/mac/dev.compose.yml --profile dev up -d --build
```

It exposes `http://127.0.0.1:18080/`, avoiding the existing native Aria ports.
Docker Desktop must be running. Stopping this project has no effect on native
Talker, Aria or the SSH runner. Keep native model and recorder lifecycle details
in the Mac service runbook.

## Telephony and PBX persistence

`box/telephony.compose.yml` preserves the verified patched Active Call image and
its three current mounts. The example pins the image ID observed on 2026-10-31;
verify it is still the intended image on the box. Keep the existing RustPBX
running while preparing this model. Active Call listens on UDP 13050 and TCP
8081; its configured RTP range is 25000–29999. Linux host networking preserves
those SIP/RTP contracts. No `ports:` translation or Docker Desktop host-network
assumption is involved. The same SIP/codec/playbook settings must survive any
move. Model validation does not verify audible RTP or interruption cancellation.

**The current RustPBX must not be recreated from its image alone.** Inspection
found only its config mounted; the database URL is relative
`sqlite://rustpbx.sqlite3`, and `/app/storage` plus `/app/config/recorders` contain
writable-layer state. `box/rustpbx.staged.compose.yml` is a separate migration
model, not part of the normal telephony start command.

Before that staged model can be activated:

- Recover the actual database and SQLite sidecars consistently (stop writes or
  use a verified SQLite backup), storage tree, recorder tree and private config.
  Inventory all other writable-layer changes; these known paths are not proof
  that no additional state exists. Retain the original container and backup.
- Place recovered data in the three explicit host directories. Verify file
  counts/checksums and database integrity. Preserve owner/permissions.
- In a **copy** of the private config, relocate the database URL to
  `sqlite:///state/rustpbx.sqlite3`, matching the mounted directory. Confirm the
  deployed binary accepts that absolute URL in an isolated test. Do not make a
  new empty database and mistake a successful boot for a successful migration.
- Test the prepared config, routes, registrations, console, recordings and
  media with isolated ports before switching the live ports. The live values
  observed were SIP UDP 5060, console TCP 8080 and RTP 20000–23999.
- Move only when calls are idle and rollback can restore the retained container
  and the latest state. Keep PBX/Active Call upgrades separate from golive engine
  changes so a broken audio path has an identifiable cause.

The current simulator keeps its own Compose project and named volumes. Its
observed deployment also uses `compose.providers.yml` and
`compose.sim-domain.yml` next to the simulator home. Preserve those private
provider and routing overlays when operating it; running the upstream base
file alone does not reconstruct the deployed simulator. Do not rename the
project or create fresh `run-data`/`gateway-config` volumes accidentally.
