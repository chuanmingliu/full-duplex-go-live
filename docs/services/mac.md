# Mac services and native boundaries

Observed on 2026-10-31, Asia/Shanghai. This is an inventory snapshot, not live
health monitoring. See [codebase ownership](codebases.md) for source and branch
mapping. No running Mac service was changed during this inventory.

## Services relevant to golive

| Service | Observed listener or interface | Runtime and supervisor | Operational role |
| --- | --- | --- | --- |
| Local Talker | TCP `*:18081`; `GET /health` returned 200 | Native Homebrew `llama-server`, llama.cpp b8920; detached process launched by private `mac-service/serve.py` | Experimental short-clause model used by the Desk playground and Journal health display |
| Native Go Aria | `127.0.0.1:3000`, `127.0.0.1:8765`, `*:8767`, `127.0.0.1:8768` | One `live-aria` process; launchd label `com.openai-gpt-live.aria` | Separate Go Aria implementation with operator, realtime, PBX and telemetry interfaces |
| Mac runner | Filesystem queue under `~/golive/.runner`, SSH to box | Native Bash loop, `~/golive/runner.sh` | Transports reviewed jobs and artifacts; does not host the conversation engine |
| Handset recorder | On-demand browser/local Python service | Started for individual acoustic measurement sessions | Records the actual handset's audible reply and caller speech on one clock |

The native Mac Aria's four listeners are one process, not four independent
services. The Mac and box both use some of the same port numbers; always include
the host in a service reference. Mac Aria is also not the TypeScript Aria
originator running on the box.

The inventory found no recorder listener at the previously used port 65187.
This does not establish whether a browser retained an earlier recording.

## Talker: preserve the native Metal runtime

The running executable resolves to
`/opt/homebrew/Cellar/llama.cpp/8920/bin/llama-server`. Loaded libraries include
the Metal backend. Its working directory is
`~/.local/share/golive-talker/mac-service`; the private model is
`~/.local/share/golive-talker/models/tuned/talker-q4_k_m.gguf`.

The existing launcher selects device `MTL0`, 99 GPU layers, one parallel slot,
a 2048-token context, four compute/batch threads, no context shifting and no
warmup. These are observed launch settings, not recommendations to change them.
Replacing this with a Linux CPU container would change the execution backend
and invalidate timing comparisons.

Source and model workflow live in the `codex/local-talker` branch of
`full-duplex-go-live`, checkout `~/golive-local-talker`. The running launcher
is outside Git in the private model directory; it detaches `llama-server`, writes
`service.json` and waits for health. No dedicated Talker LaunchAgent was found.
Consequently the present detached process is not evidence of restart-on-crash
or restart-at-login coverage.

| Operation | Existing mechanism |
| --- | --- |
| Health | `curl --noproxy '*' --fail http://127.0.0.1:18081/health` |
| Start | `python3 "$HOME/.local/share/golive-talker/mac-service/serve.py"`; only after verifying the port is unused |
| Stop | Read the PID from private `service.json`, verify its executable and listener still match, then terminate that exact process |
| Logs | Private `mac-service/server.log`; may contain request-related information |
| Preserve | Model GGUF and export manifest, launcher, selected training provenance and private evaluation artifacts |
| Upgrade/rollback | Preserve the old executable version, model hash and arguments; compare readiness and a synthetic request before redirecting clients |

Do not combine a new launchd restart policy with the current detached launcher:
the supervisor should execute the foreground `llama-server` process directly.
That migration requires first recording the current private model/configuration
and stopping the exact old process so two supervisors cannot compete for 18081.
Talker availability is not evidence that the phone engine uses Talker for filler;
the current phone filler path remains a separate engine feature.

## Native Go Aria: already supervised by launchd

The LaunchAgent is
`~/Library/LaunchAgents/com.openai-gpt-live.aria.plist`. It uses `KeepAlive`,
`RunAtLoad` and a five-second throttle. Its executable and working directory are
under `~/Library/Application Support/Aria`, with logs under `~/Library/Logs/Aria`.
The service reads a private telephony environment file from its retained rollback
directory. Preserve that reference when changing the launch configuration.

The installed binary's Go build information identifies
`github.com/chuanmingliu/openai-gpt-live/cmd/live-aria`, revision
`051b03f4a8ef9f1fcae860c276f73249fe183e8d`, with **`vcs.modified=true`**.
The exact build cannot be reproduced from that revision alone. Its current
editable checkout has moved to a different revision. The planned maintenance branch
`codex/service-mac-aria` is tracked separately in the central service catalog;
branch creation does not reconcile the dirty installed build. Do not replace the
installed binary merely because the checkout builds successfully.

Read-only checks returned HTTP 200 at `:3000/healthz`, `:3000/readyz` and
`:8765/health`. Other attempted route shapes returned errors; that is a route
compatibility issue, not evidence that the process was absent. Health does not
prove that the configured remote PBX route can place a call.

| Operation | Mechanism |
| --- | --- |
| Inspect supervisor | `launchctl print "gui/$(id -u)/com.openai-gpt-live.aria"` |
| Health | `curl --noproxy '*' --fail http://127.0.0.1:3000/healthz` |
| Build identity | `go version -m "$HOME/Library/Application Support/Aria/bin/live-aria"` |
| Logs | `live-aria.stdout.log` and `live-aria.stderr.log` in `~/Library/Logs/Aria` |
| Upgrade/rollback | Preserve the binary, LaunchAgent, private referenced files and runtime state before replacing anything; verify all four interfaces afterward |

For an authorized stop, use `launchctl bootout` on the existing LaunchAgent;
for an authorized start, use `launchctl bootstrap` with the same plist and GUI
domain. `launchctl kickstart -k` restarts the process and can interrupt calls.
Do not run those lifecycle commands as part of an inventory.

## Runner and handset capture remain native

The runner depends on the Mac's SSH identity/control socket and local artifact
queue. Keep a single queue consumer. The checked-in portable implementation,
enqueue helper and operation history are in
[scripts/local-ops](../../scripts/local-ops/README.md). That copy is distinct
from the currently installed `~/golive/runner.sh`.

Check the `.runner/alive` timestamp, queue and receipt for the exact job before
submitting work. A stale heartbeat or SSH timeout is not evidence that a remote
call or deployment never happened. Never retry an uncertain dial automatically.
Preserve the installed runner and private receipts; do not commit queue output.

Acoustic recording needs the Mac microphone/browser and the real handset.
Containerizing the box recording service does not replace this measurement.
Follow [HANDSET-LATENCY.md](../../HANDSET-LATENCY.md).

## Adjacent services, outside this migration's default scope

The Mac also had native inference listeners on 18001 and 18002, belonging to
`~/responsible-api` (`chuanmingliu/inference-response-api`). Their LaunchAgents
are `local.responsible-api.native` and `local.responsible-api.sglang`.
They are separate vLLM/SGLang model services; this inventory did not establish
that the phone engine or Talker depends on them.

OpenClaw, CLIProxyAPI, Claude memory, Reason, messaging apps and macOS listeners
also appeared. A listening port alone is insufficient reason to move them into
the golive Compose project. Their lifecycle and data remain owned by their
respective applications.

Docker Desktop's daemon was unavailable during the parent inventory. Compose
syntax validation can still be performed, but Mac container runtime checks must
not be claimed until a Docker daemon is available. The native services above
were observed independently of Docker.
