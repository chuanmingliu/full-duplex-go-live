# Xiaocui skills over MCP

For ThinkPad maintenance, backups, rollback and migration to another production host, use the [operator runbook](OPERATIONS.md).

The [developer API reference](API.md) covers the runtime MCP methods, admin endpoints, catalog schema, authentication, examples, limits and GoLive integration.

The ThinkPad serves outbound workflow instructions and their associated tools from one catalog. The operator edits workflow triggers, state conditions and content over an SSH tunnel. The supplied turn renderer composes the original business prompt with the skills and tools selected for a specific turn.

| Component | Contract |
| --- | --- |
| MCP endpoint | Streamable HTTP at `http://127.0.0.1:8794/mcp` on the ThinkPad. Use the runtime bearer credential. |
| Skill discovery | `server/discover` advertises `io.modelcontextprotocol/skills`. `skills/list` and `skills/get` return frontmatter, byte length and SHA-256 for each SKILL.md. `resources/read` returns the Markdown. |
| Earlier clients | Use the SDK's legacy initialization and read `skill://index.json`, then `skill://motor-quotation/SKILL.md`. |
| Turn selection | The standard MCP prompt `outbound-turn` takes a normalized event, or `auto` with the current utterance, plus a small projection of confirmed state. Matching skills arrive as resource links. |
| Business tool | `tools/call` with `name: quote_total` posts the existing working-memory envelope to the box CRM at `/api/tools/quote`. The CRM owns intake, plan merging, pricing, configured delays and comparison results. |
| Operator panel | The panel at `/` uses a separate admin token. Publish skill instructions, descriptions and availability together with the tool description, availability and timeout. |
| Persistence | One Compose replica owns `~/golive-skills-mcp/data/catalog.json`. Each publication compares the displayed revision with the current revision and atomically replaces the file. Prior to editing, an operator can copy that file for backup. |
| Deployment boundary | Active Call continues to use its existing quote integration. The optional Active Call host integration loads guidance through MCP. Its existing native quotation executor owns call-time execution. |

## Open the panel on the Mac

If the tunnel is already running, open [the operator panel](http://127.0.0.1:18794/). To create the tunnel from a Mac terminal:

```sh
ssh -fNT -o ExitOnForwardFailure=yes -o ServerAliveInterval=30 \
  -L 127.0.0.1:18794:127.0.0.1:8794 chuanming@192.168.71.9
```

Copy the admin token to the Mac clipboard and paste it into the panel. This command keeps the token out of terminal output and history:

```sh
ssh chuanming@192.168.71.9 'cat ~/golive-skills-mcp/secrets/admin' | pbcopy
```

Select or add a skill, edit its events, automatic-selection patterns and exclusions, state conditions, tool binding and Markdown instructions, then publish. Disabling the quotation tool withdraws dependent skills. Guidance-only workflows remain available. The next MCP request sees the new catalog. If a second editor published first, the panel reports a conflict. Copy any draft text you want to retain before reloading. New executable tools and backend bindings are reviewed code additions. To add a tool, register a typed handler in `buildServer`, extend the catalog and panel for its operator settings, and include an integration test.

## Quotation example

The host supplies call identity and confirmed working memory. To revise only third-party liability, send the real identifier for the current call, the latest selection, and the requested change. To compare alternatives, submit them together under `configs`. Each alternative is relative to the base plan.

```json
{
  "name": "quote_total",
  "arguments": {
    "call": "synthetic-example",
    "memory_version": 3,
    "memory": {"quote": {"交强险": true, "三者险": "200万"}},
    "config": {"三者险": "300万"}
  }
}
```

Inspect `structuredContent.status`. A `need` result identifies the next intake field. A `quote` result carries the calculated data, including any comparison variants. An upstream failure is an MCP tool error. The adapter sends one CRM request per invocation. A calculation timeout is separate from call response latency.

## Render a turn

In the panel, use **Preview a published turn** to try the original prompt, an event and a small JSON state snapshot. The preview reads the published revision and displays the selected guidance and workflow tools. It does not dial, send WeChat notifications or calculate premiums.

| Business event | Confirmed state | Workflow in this turn | Workflow tool |
| --- | --- | --- | --- |
| `conversation.normal` | Any | None | None |
| `wechat.add_requested` or `wechat.notification_help` | `wechat.status` is `consented` or `awaiting_confirmation` | Guide the caller through finding the service notification | None |
| Same WeChat event | Missing, `declined` or `added` | None | None |
| `quote.requested`, `quote.revised` or `quote.compare` | Any | Collect missing information, price a plan or compare alternatives | `quote_total` |

Explicit routing matches one exact event name and **all** state conditions. Automatic routing uses `event=auto` and `utterance`, requires a `match` regular expression and suppresses any `exclude` match, then applies the same state conditions. Schema 3 adds editable patterns to the known seed skills while preserving operator content. Set event `auto` in the panel preview to test these rules. Each condition accepts one of its configured values. Skills without event names remain available for manual discovery. Multiple matches retain catalog order. The sample WeChat workflow checks consent and walks through the caller's actual screen. It makes no assumption that a notification was delivered.

### Prompt components and authority

[turn_renderer.py](turn_renderer.py) is the host-side reference implementation exercised by the deployed smoke test. It accepts the original CRM-rendered prompt, fixed host policy, existing host tools and a versioned session snapshot. Only the normalized event and projected state leave the host during selection.

| Slot | Owner and lifetime |
| --- | --- |
| Original prompt | CRM/Jinja selects and renders the configured business prompt. Keep its exact text as the stable prefix. |
| Host policy and existing tools | The host owns conversational safeguards, permitted actions, tool schemas and interruption behavior. |
| Session state | The host owns the latest confirmed working memory, CRM facts and operation outcomes. Pass a minimal projection and retain its version. |
| Skill context | MCP returns references for this turn. The renderer verifies the selected skill's manifest, exact bytes, frontmatter, origin and host approval, then returns labeled guidance. |
| Workflow tools | Intersect selected bindings with the host allowlist. Keep the native tool schema separate from prose and deduplicate names already supplied by the host. |

The renderer reselects on a publication race and falls back to the original components when the service or verification fails. An operator publication changes the next selection, including subsequent turns of an ongoing session. In-flight speech and completed actions retain their original turn. The host's `accept_skill` policy owns approval for a specific origin, URI and digest. Production enrollment should tie that decision to the authenticated business publishing workflow, independently of the caller.

### Active Call integration

The panel's **Prompt rendering** selector publishes an optional v2 mode. Version 1 remains the catalog default. Version 2 adds verified skill bodies to a `{{ skill_content }}` slot after the existing default system prompt, host policy and memory. The engine retains source and scope metadata separately and fills the template once, preserving literal Jinja inside the original prompt. A no-match turn uses the original prompt directly. Retrieval failures keep the original-prompt fallback.

Deploy the updated engine and catalog service together to use v2. After activation, publishing either version takes effect on the next model request, including within an existing call. Switching the panel back to version 1 restores appended guidance. The [slot implementation guide](../../docs/architecture/09-skills-prompt-rendering.md#optional-v2-skill-content-slot) describes message ordering, substitution and compatibility.

The optional engine integration calls `prompts/get` before the first model completion. The service matches the current utterance against operator patterns and a projection of versioned working memory, then supplies resource links. The host verifies each resource and refreshes the memory snapshot before adding current guidance to the original prompt. This avoids a separate model completion for skill selection. Empty configuration and retrieval failures use the original prompt. The client checks the service’s `auto_selection` capability marker to detect older deployments.

The implemented Go client falls back on a publication race. The Python reference renderer supports a bounded reselect. Both read fresh catalog state on subsequent requests. The engine trusts its explicitly configured, authenticated publishing service for business guidance and retains its own executable tool definitions.

The [prompt integration guide and roadmap](../../docs/architecture/09-skills-prompt-rendering.md) document configuration, components, state expiry, failure handling, measured simulation overhead and remaining real-provider acceptance. The catalog is deployed. Enabling the optional integration in the live phone engine requires a separate idle-only deployment. Business-content edits become visible without an engine restart after that activation.

The implementation follows the current [extension specification](https://github.com/modelcontextprotocol/ext-skills/blob/main/specification/beta/skills.mdx). The additional index follows the [feature-branch resource convention](https://github.com/modelcontextprotocol/ext-skills/blob/feature/python-sdk/docs/sep-draft-skills-extension.md). The [AAIF overview](https://aaif.io/blog/skills-over-mcp) explains the original progressive-discovery design.

## Build, deploy and verify

From this directory, run `go test -race ./...` and `go vet ./...`. Build the static release executable:

```sh
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags='-s -w' -o /tmp/skills-mcp .
```

Bundle the executable as `skills-mcp` alongside `Dockerfile`, `.dockerignore`, `compose.yaml`, `install.sh` and the two Python files `smoke.py` and `turn_renderer.py`. Use the [Mac runner](../../scripts/local-ops/README.md) to transfer the archive, verify its SHA-256 on the box and extract it into `~/golive-skills-mcp/releases/<release>`. Run `RELEASE=<release> ./install.sh` there. Installation creates private token files on first use, builds a scratch image, starts the one catalog container and runs the smoke test. The service listens on loopback and uses the existing CRM credential through a read-only mount.

For a later check, run `python3 smoke.py` in the installed release directory. It verifies the runtime/admin boundary, catalog discovery, file integrity, tool listing and one synthetic CRM intake request. It also renders synthetic normal, WeChat and quotation turns. It prints synthetic routing outcomes and counts. Save a private copy of `data/catalog.json` before deploying a schema upgrade. Schema 3 adds automatic-selection rules. A rollback to a schema-2 release requires restoring its compatible catalog snapshot as well as running the previous installer with its original tag. Preserve the newer catalog separately so later operator edits remain recoverable. To take the catalog offline, stop only its container with `docker stop xiaocui-skills-catalog-1`. The persisted catalog and credentials live outside the release directory.

## Callback response templates

The panel's **Callback responses** section maintains spoken confirmation lines for the trusted `addV` event. Published changes are served through `golive://event-responses/addV` and take effect at the next callback, including during an existing call. The Active Call engine preserves normal floor ownership, interruption guards and quotation scheduling. See the [tracking callback runbook](../../docs/services/tracking-events.md#configurable-callback-speech).
