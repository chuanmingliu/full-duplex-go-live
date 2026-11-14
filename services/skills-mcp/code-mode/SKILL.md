---
name: mcp-code-mode
description: Discover and execute MCP business tools through bounded JavaScript workflows.
---

Use the `execute_code` MCP tool on `/mcp/code` to discover capabilities and
compose business operations. Its `code` argument is an async JavaScript function
body with top-level `await` and `return`.

## Discover and inspect

```javascript
return await search("quotation");
```

```javascript
return await describe("tools.quote_total");
```

Use exact returned names. `describe` supplies TypeScript declarations. The
`codemode://tools/typescript` resource provides all declarations on request.
Execute plain JavaScript. Read matched business skills through `skills/get` and
`resources/read` and apply their current business rules when composing code.

## Execute with call context

The trusted caller puts `call`, `memory` and `memory_version` on the outer
`execute_code` arguments. Every nested business call receives that snapshot.
The host overwrites these fields if a script supplies different values.
Supply `catalog_revision` from this skill's `ai.liuchuanming.golive/code-mode`
metadata to check the catalog used for planning. A mismatch requires fresh
discovery before executing.

```javascript
const quote = await tools.quote_total({
  configs: [{"三者险": "200万"}, {"三者险": "300万"}]
});
if (quote.status === "need") return quote;
return quote;
```

The tool returns its structured MCP result. Use the returned fields to filter or
summarize the result when appropriate. Prices and required facts come from the
tool. Preserve the business skill's intake and confirmation rules.

## Execution behavior

- Each execution gets a fresh VM. Local variables exist for that execution.
- `await` every call. Loops, conditionals, try/catch and `Promise.all` work.
  Business dispatch is serialized. Prefer one `configs` comparison request for
  related quotations sharing a base plan.
- Return only information needed for the next model turn. Console output is also
  returned, so treat logging as disclosure to the model.
- Tool errors reject promises. An uncaught error fails the execution. Earlier
  completed operations can persist. Inspect outcomes before retrying a failed
  or interrupted workflow. The runtime does not replay operations automatically.
- The default deadline is 90 seconds including backend time. The caller may set
  `timeout` up to `180s`. Cancellation propagates to pending tool requests.
- Limits are 32 KiB code, 64 KiB input memory, 64 MiB JS heap, 16 host calls
  including discovery, 4,096 console characters and 24,576 returned result characters.
  Output reports truncation. Four executions may be admitted concurrently.
- The sandbox exposes JavaScript built-ins, `tools`, `search`, `describe` and
  captured console methods. Host files, credentials, shell commands, networking,
  timers and imports are unavailable.

The response includes success status, result, call count, elapsed milliseconds,
truncation status and logs. A catalog publication affects subsequent requests.
An admitted request uses the catalog snapshot selected when it started.

## Select companion skills

For large skill catalogs, use `tools.skills_search` to retrieve bounded metadata
by query, conversation `context`, or exact event. `search_mode: "auto"` uses
configured description embeddings and reports lexical fallback. Use `"semantic"`
to require embeddings or `"lexical"` for word matching. Search replies include
`retrieval`, optional `fallback_reason`, and cosine `similarity` for semantic
matches. Supply a concise relevant context and inspect the returned skills.
Use `tools.skills_describe` to inspect full manifests.
Pass the returned revision and candidate URIs to `tools.skills_resolve`, then
load its selected URIs using `tools.skills_load`. Known URIs can be loaded
through a single load call with a current revision.

Supply the host-confirmed `state` projection consistently to every operation.
Resolution enforces availability, state guards, priorities, dependencies,
conflicts, a selected skill count, and a UTF-8 JSON reply budget. Inspect partial
or unresolved results. Never treat a rejected or missing dependency as loaded.
The host verifies each returned manifest and exact Markdown before prompt use.
A revision mismatch requires fresh discovery. Existing `outbound-turn` routing
remains available to clients.

### Confirmed state

The host supplies confirmed string state in the outer `execute_code.state`
object. The executor overwrites the `state` of every nested `skills_*` call
with that projection. Omission supplies an empty state. Request the host's
current state before selecting skills whose guards depend on it.
