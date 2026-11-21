# Operating the Xiaocui Skills MCP service

Use this runbook to change a published skill, maintain the ThinkPad deployment, recover a previous release, or move the service to another Linux host. For request schemas and examples, use the [developer API](API.md). For how Active Call uses a selected skill, see [prompt rendering](../../docs/architecture/09-skills-prompt-rendering.md).

| What you need to change | Where to change it | When it takes effect |
| --- | --- | --- |
| Instructions, matching rules, consent conditions, enabled skills, tool binding, callback speech, v1/v2 rendering | Skills & tools panel → Publish | The next applicable request, including a later turn of a running call |
| Compiled tool handler, panel code, transport or catalog schema | Build and roll out a new **catalog** image | When the replacement container starts |
| MCP address or runtime credential used by Active Call | GoLive engine profile or its environment and credential file | Activate the engine configuration during an idle window |
| Quote calculation, premiums and backend delay | CRM quotation service | According to the CRM release/configuration. The MCP tool forwards requests; Active Call currently uses its native quotation executor. |

## 1. Identify the running installation

Verified on the ThinkPad on **2026-11-10**. Treat these identifiers as a recovery reference and rediscover the active release before any change.

| Item | ThinkPad value |
| --- | --- |
| Operator access | Mac runner targeting `chuanming@192.168.71.9` |
| Project, service, container | `xiaocui-skills`, `catalog`, `xiaocui-skills-catalog-1` |
| Running image and Compose directory | `xiaocui-skills:concepts-20261110` in `~/golive-skills-mcp/releases/concepts-20261110/` |
| Persistent business configuration | `~/golive-skills-mcp/data/catalog.json`, mounted under `/data`. Schema 3, revision 8, renderer v1 at verification. |
| Published skills | `motor-quotation`, `wechat-service-notification`, `vehicle_insurance_concepts` |
| Credentials on the host | `~/golive-skills-mcp/secrets/admin` for publishing, `~/golive-skills-mcp/secrets/runtime` for MCP, `~/golive-crm/api.token` for the quotation backend |
| Network | Host networking. Health, panel and MCP share `127.0.0.1:8794`. The MCP quotation handler calls `127.0.0.1:8791/api/tools/quote`. |
| Runtime | One non-root container with a read-only root filesystem, read-only credential mounts, writable data directory and restart policy `unless-stopped` |

**On the ThinkPad**, run the following read-only checks. When operating from the Mac, submit box commands through the [existing runner](../../scripts/local-ops/README.md#small-reviewed-job). Use its completed receipt and exit status. The SSH tunnel in section 2 is for browser access.

```sh
UNIT=xiaocui-skills-catalog-1
docker inspect -f '{{.Config.Image}} {{.Image}} {{.State.Health.Status}}' "$UNIT"
docker inspect -f '{{index .Config.Labels "com.docker.compose.project.working_dir"}}' "$UNIT"
docker inspect -f '{{range .Mounts}}{{println .Source .Destination .RW}}{{end}}' "$UNIT"
curl -fsS --max-time 3 http://127.0.0.1:8794/healthz
```

Expect `ready`. This proves the HTTP process responds. To verify the running catalog and an actual routing decision, run this on the same host. It prints only the revision, renderer, selected names and tool names. It makes no business request.

```sh
python3 - <<'PY'
import json, os
from pathlib import Path
from urllib.request import Request, urlopen

stack = Path(os.environ.get("STACK_DIR", Path.home() / "golive-skills-mcp"))
token = (stack / "secrets/admin").read_text().strip()
turn = {"event": "auto", "utterance": "交强险一般保障什么？", "state": {}}
req = Request("http://127.0.0.1:8794/admin/preview",
              data=json.dumps(turn).encode(),
              headers={"Authorization": "Bearer " + token, "Content-Type": "application/json"})
with urlopen(req, timeout=5) as response:
    result = json.load(response)
print(result["revision"], result["prompt_rendering"]["version"],
      [s["name"] for s in result["skills"]], result["tools"])
PY
```

The verified catalog returns `8 1 ['vehicle_insurance_concepts'] []`. After a publication, compare with the newly intended revision and selection.

## 2. Edit skills and check the result

From the **Mac**, open the existing [local panel](http://127.0.0.1:18794/). If the tunnel is absent, run the following in a terminal and leave it open. In a second terminal, copy the credential. A successful clipboard command produces no terminal output; check the pipeline exit status and paste directly into the panel.

```sh
ssh -NT -o ExitOnForwardFailure=yes -o ServerAliveInterval=30 \
  -L 127.0.0.1:18794:127.0.0.1:8794 chuanming@192.168.71.9
```

```sh
set -o pipefail
ssh chuanming@192.168.71.9 'cat ~/golive-skills-mcp/secrets/admin' | pbcopy
```

1. Open the catalog, note its revision and take the backup in section 3. Select the skill. Edit its instructions and the rules that select it. Event names match exactly. Automatic selection uses the utterance's inclusion and exclusion patterns. Every configured state condition must hold. Leave the tool binding empty for an explanation-only skill.
2. Publish the complete catalog. **Preview a published turn** reads the published revision; previewing before publishing exercises the old rules. For draft testing before production publication, use an isolated copy of the catalog. On HTTP 409, retain your draft, reload the current revision, reconcile the other changes and publish again.
3. Preview a positive example and a case that should yield no skill. In the current catalog, the insurance question above selects only the concepts skill; “服务通知在哪里找？” with `{"wechat.status":"consented"}` selects WeChat guidance; repeating it with `{"wechat.status":"added"}` suppresses that skill. Check the returned tools as well as the skill names.
4. For a change affecting the caller, run the original **call sim** workflow, then inspect **Skills / MCP** in Call Journal. Check the selected URI, digest, catalog revision, render outcome and time for the turn. The panel preview tests routing; the Journal trace shows what reached the engine's prompt preparation. See [call operations](../../CALL-OPERATIONS.md).

The panel also publishes `addV` response templates. The tracking backend sends its authenticated HTTP callback to **GoLive**. GoLive reads the response template from MCP. Moving the catalog therefore changes the guidance and template lookup route; the event producer continues to target GoLive. See [tracking events](../../docs/services/tracking-events.md).

## 3. Back up and restore state

The release binary embeds the panel and initial seed data. Operator changes live in the catalog file. A new, empty data directory starts with the quotation and WeChat seeds; it omits the separately published vehicle concepts skill. **Copy the running catalog when moving hosts.** Call recordings, conversation history and leads belong to other services.

Before a publish, upgrade or move, run this as the service owner on the source host. Coordinate publishes during a migration so the final snapshot includes the last edit.

```sh
set -eu
umask 077
STACK_DIR="${STACK_DIR:-$HOME/golive-skills-mcp}"
UNIT=xiaocui-skills-catalog-1
mkdir -p "$STACK_DIR/backups"
SNAP=$(mktemp -d "$STACK_DIR/backups/catalog-XXXXXXXX")
cp "$STACK_DIR/data/catalog.json" "$SNAP/catalog.json"
docker inspect -f '{{.Config.Image}} {{.Image}}' "$UNIT" > "$SNAP/image.txt"
docker inspect -f '{{index .Config.Labels "com.docker.compose.project.working_dir"}}' "$UNIT" > "$SNAP/release-dir.txt"
(cd "$SNAP" && sha256sum catalog.json > SHA256SUMS && sha256sum -c SHA256SUMS)
python3 -c 'import json,sys; c=json.load(open(sys.argv[1])); print("schema",c["schema"],"revision",c["revision"])' "$SNAP/catalog.json"
echo "$SNAP"
```

Retain the matching release bundle, manifest and image (or registry digest) alongside the backup record. Copy the backup to the organization's private backup store and test restoring it on an isolated host. Keep secrets in a separate protected backup or secret manager; the catalog snapshot alone cannot restore authentication. The older ThinkPad backup at `~/golive-skills-mcp/backups/concepts-20261110/` contains **pre-publication** state in `catalog.before.json`; it predates the concepts skill.

**Undo a content edit:** use the panel to restore only the affected fields and publish a new revision. For a full compatible snapshot, the admin API also accepts a full catalog with the currently displayed revision. Compare all differences first; copying the latest revision number into stale content can overwrite another editor's work.

**Recover an incompatible or damaged installation:** stop publications, save the current file and image for investigation, and choose a verified snapshot compatible with the previous executable. In an idle maintenance window stop only the catalog container. Verify the snapshot checksum, then copy the catalog to a temporary file **inside** the mounted data directory, set owner/mode for the container UID (on the ThinkPad, the service owner and mode 0600), and rename it to `catalog.json`. From the recorded earlier release directory, select its original Compose variables and run `docker compose up --no-build --wait --wait-timeout 60 catalog`. Verify the running image ID, published revision, preview, runtime discovery and a simulator turn. The process reads the catalog into memory at startup; replacing the file under a running container is insufficient. Older executables may require their original schema snapshot.

## 4. Build, upgrade and rotate credentials

Build from the desired commit in `services/skills-mcp/`, a separate Go module. These commands create a portable release bundle outside the working tree. Set the architecture to match the **destination Linux machine**. The ThinkPad uses `amd64`.

```sh
set -eu
BUNDLE=$(mktemp -d)
TARGET_ARCH=amd64
go test -race ./...
go vet ./...
python3 -m unittest test_turn_renderer
CGO_ENABLED=0 GOOS=linux GOARCH="$TARGET_ARCH" go build -trimpath -ldflags='-s -w' -o "$BUNDLE/skills-mcp" .
cp Dockerfile .dockerignore compose.yaml install.sh smoke.py turn_renderer.py "$BUNDLE/"
(cd "$BUNDLE" && tar -czf ../"$(basename "$BUNDLE")".tgz skills-mcp Dockerfile .dockerignore compose.yaml install.sh smoke.py turn_renderer.py)
```

1. Assign an immutable release tag and record the source commit, target architecture and bundle SHA-256. Transfer through the runner to the ThinkPad, or through the production deployment system to the target. Verify the bundle against that SHA-256 **on the destination before extraction**. Extract into a fresh `$STACK_DIR/releases/<tag>/`. Keep the previous image and directory.
2. On the destination, check Docker Engine, Compose with `up --wait`, Python 3 and OpenSSL. Confirm the CRM endpoint and credential, backup, data ownership and free port. On the ThinkPad verify `http://127.0.0.1:8765/healthz` reports zero sessions immediately before replacing the service. Coordinate the same idle window with the engine owner on a split deployment.
3. From the extracted release directory run the block below, substituting your chosen release tag. The installer creates missing admin/runtime tokens, runs the catalog as the invoking UID/GID, builds its image, waits for container health and runs the integration smoke. A smoke failure can leave the candidate running; inspect the actual container and use section 3 to roll back if needed.

```sh
export RELEASE='<release-tag>'
export STACK_DIR="${STACK_DIR:-$HOME/golive-skills-mcp}"
export CRM_TOKEN_FILE="${CRM_TOKEN_FILE:-$HOME/golive-crm/api.token}"
sh ./install.sh
```

For subsequent manual Compose commands from that directory, also export `SERVICE_UID=$(id -u)` and `SERVICE_GID=$(id -g)`; exports made inside the installer end with that process. Use `docker compose ps catalog` and `docker compose logs --tail 80 catalog` to inspect it locally. On a later verification, `python3 smoke.py` checks access separation, skill discovery and content digests. **When the quote tool is enabled, the smoke sends one synthetic intake request to the configured CRM.** Use the section 1 preview and authenticated discovery for checks that must avoid business calls. The [API examples](API.md) cover runtime discovery.

**Rotate by credential role.** The server reads all three secret files at startup. For the runtime token, prepare the new secret in the engine's secret store and the matching value in the catalog's runtime mount. Rotate in an idle window, recreate the catalog and reload the engine's client via its normal release procedure. The client also reads its token from disk at initialization. For an admin rotation, recreate only the catalog and reconnect the panel. Coordinate CRM credential changes with the issuing CRM and its other consumers. After an atomic replacement of a bind-mounted secret file, use `docker compose up --no-build --force-recreate --wait catalog` so the container mounts the new file. Verify the new credentials through their own endpoint, retire the old values and record only secret version identifiers.

## 5. Move to a production host

The smallest move that works with the current binary is **one Linux host, one catalog replica, durable local storage**, with a private route to the engine and CRM. For example, set `STACK_DIR=/srv/xiaocui/skills-mcp` and `CRM_TOKEN_FILE=/run/xiaocui-secrets/crm` on the destination. Provision those paths for the service UID. Copy the final catalog under `$STACK_DIR/data/`; provision admin and runtime secrets under `$STACK_DIR/secrets/`. Supply the engine with its own readable copy of the runtime credential. A path inside the MCP container has no meaning on a separate engine machine.

| Dependency | Works with the current build | When changing the topology |
| --- | --- | --- |
| Engine → MCP | Same host uses `http://127.0.0.1:8794/mcp`. A managed private forward can expose that loopback endpoint to a separate engine. | Alternatively run a TLS reverse proxy on the **MCP host** with access to its loopback listener. Route `/mcp` to the service and preserve bearer and MCP request headers. Configure the engine with the authenticated private HTTPS URL and a trusted certificate chain. |
| MCP → CRM | Place CRM on the same host at `127.0.0.1:8791`, or supply a private local proxy/forward on that port to the intended CRM. | For a native remote CRM URL, first implement and test a configurable backend URL in [main.go](main.go). Setting a Compose variable alone has no effect. Ensure the smoke targets an appropriate CRM environment. |
| Admin panel | Keep using an SSH forward or equivalent managed private access to the MCP host's loopback HTTP port. | A browser-facing HTTPS panel needs a reviewed change to origin validation. The current handler accepts a present `Origin` only when it equals `http://` plus the request Host. Preserve that protection and explicitly allow the intended HTTPS origin before exposing the panel this way. |
| Persistence and replicas | One writer, one persistent data directory; image updates preserve mounts. | Concurrent-publish protection is an in-process mutex and revision check. Multiple writable replicas need shared transactional revision control and cross-instance catalog refresh before scaling. A shared file mount alone does not provide either. |
| Network and image | Stock Compose uses host networking and the binary binds to loopback. Build the static binary for the target CPU. | Container port publishing by itself cannot expose a process bound to the container's own loopback in bridge networking. A different listener requires changes to the server, health probe and smoke configuration. |

For a trial where GoLive stays on the ThinkPad and MCP moves to another Linux server, run the following on the **engine host**, substituting the SSH destination. It leaves the original loopback port available for rollback. Point the isolated simulator at `http://127.0.0.1:28794/mcp` with a destination runtime token file readable on the engine host. Verify that port 28794 is free before starting the forward. For sustained service, supervise the forward with restart-on-failure or use the private HTTPS route above.

```sh
MCP_SSH_DEST='operator@new-mcp-host'
ssh -NT -o ExitOnForwardFailure=yes -o ServerAliveInterval=30 -o ServerAliveCountMax=3 \
  -L 127.0.0.1:28794:127.0.0.1:8794 "$MCP_SSH_DEST"
```

**Cutover sequence**

1. Stage the candidate on the new host using a copy of the catalog. Keep the current host serving. Validate the destination from the **engine network**, including unauthenticated rejection, authenticated discovery, a matched resource and its digest, and a normal no-match. Run positive and negative previews. Use synthetic CRM intake only against the intended backend.
2. Pause catalog publications on the source. Take the final snapshot and checksum, stop the destination catalog, install the final data and restart it. Compare revision, rendering version, skill identities/digests, tool availability and callback configuration. Keep only one administrative writer through cutover.
3. Save the engine's current effective endpoint and token-file settings. Configure `skills_mcp.url` and `skills_mcp.token_file` or the nonempty environment overrides `GOLIVE_SKILLS_MCP_URL` and `GOLIVE_SKILLS_MCP_TOKEN_FILE`. Keep the original phone profile and start the engine with the new settings while idle. See the [host configuration](../../docs/architecture/09-skills-prompt-rendering.md). If using a private forward with the same engine-side URL, cut over the forward during that window and verify its target.
4. Run **call sim** for a concept explanation, quotation, and consented WeChat guidance. Verify the original-prompt fallback in an isolated simulator with MCP unavailable. Inspect Journal skill status, URI, digest, revision and timing. Accept the move when the intended skills and callback templates are available, fallbacks behave correctly and response latency meets your agreed baseline. Resume publication on the destination, then retain the source and backup through the rollback window.
5. To reverse the move, freeze destination edits. Capture any newer revision and reconcile it before reopening the source for publication. Restore the engine endpoint and matching credential (or switch back the forward), activate while idle, and rerun the same checks. This rollback does not require moving call audio, changing Aria's phone binding, or redeploying Active Call's transport.

**Latency across hosts.** An unmatched turn makes one `prompts/get` request. A turn matching one skill makes four sequential MCP requests: selection, manifest, content, then a catalog-revision recheck. An additional 10 ms of round-trip time can therefore add roughly 40 ms to matched retrieval, before queuing and processing. All four share one client deadline (150 ms by default). Test from the actual engine host with the proposed TLS/proxy path. Use the Journal's per-step timings and distinguish lookup time from first substantive audio and quotation calculation. As a same-host reference, the November 10 warm replay measured matched p50/p95 of 2.49/2.84 ms and unmatched p50/p95 of 0.57/0.97 ms over 30 of each. These are reference measurements, not a production latency guarantee.

## 6. Troubleshoot by symptom

| Symptom | First check and recovery |
| --- | --- |
| Panel fails to open on the Mac | Check `/healthz` on the MCP host, then the SSH forward and the Mac-side port. A blank clipboard command is normal; a nonzero SSH exit needs investigation. |
| Health is green but MCP returns 401 | Check that the engine uses the runtime token and `/mcp`. Admin access uses the admin token and `/admin/*`. After a secret swap verify container mounts and client reload. |
| Admin publish returns 400 / 409 / 500 | For 400 inspect schema and routing validation; for 409 reload and reconcile edits; for 500 inspect writable data ownership, free space and local logs. Verify the published revision after recovery. |
| 403 only from a production browser | Compare browser Origin and proxy Host against the origin check in [main.go](main.go). Use the private tunnel while correcting the proxy/application configuration. |
| Skill appears in the panel but never runs | Check published revision, enabled status, regex exclusions, exact projected state, and whether its bound tool is disabled. Test the recognized wording with `event=auto`. A Journal `no_match` means selection found nothing; `unavailable` indicates a retrieval or validation failure. |
| Engine falls back after the move | Check routing and TLS from the engine, runtime file permissions, and the first failed or slow Journal step. A publish during retrieval or changed digest can cause fallback. A changed memory version can expire already retrieved guidance. |
| Skills work but CRM quote smoke fails | Check the MCP host's path to `127.0.0.1:8791/api/tools/quote`, its CRM token and the published tool timeout. Record the synthetic call ID privately for CRM investigation. Catalog health does not exercise CRM. |
| New host is healthy but has only two skills | Confirm the data mount and revision. A missing catalog creates seed data; stop the destination writer and restore the final source snapshot before cutover. |
