# Companion skill selection

The existing Skills over MCP service now offers bounded discovery and composition
through `skills/search`, `skills/describe`, `skills/resolve`, and `skills/load`.
When activated, they are custom MCP methods on both `/mcp` and `/mcp/code`, using the runtime
bearer credential. The server advertises the extension
`ai.liuchuanming.golive/skill-selection` version `1`.

When activated, the code endpoint also exposes these operations inside JavaScript as
`tools.skills_search`, `tools.skills_describe`, `tools.skills_resolve`, and
`tools.skills_load`. Its outer MCP tool remains `execute_code`.

`outbound-turn`, `skills/list`, `skills/get`, resource reads, and the existing
automatic host loader retain their contracts. Hosts can choose the companion
flow for larger catalogs or explicit composition. The automatic host loader
continues to limit a turn to four skills. Legacy list methods and the operator
panel still load the full catalog.

## Activation

The default `auto` policy activates companion skill selection when the published
catalog contains **more than 50 available skills**. At 50 it is disabled, and at
51 it is enabled. Availability means the skill is enabled and its bound tool is
available. Per-request state guards are evaluated during retrieval. The built-in
code-mode usage skill is excluded from the count.

The optional `skill_selection` object in the catalog configures this policy.
Existing catalogs without the object use `auto` and threshold 50.

```json
{
  "skill_selection": {
    "mode": "auto",
    "threshold": 50
  }
}
```

`mode` accepts `auto`, `always`, or `off`. `always` enables the companion for
any catalog size. `off` disables it. `threshold` accepts an integer from zero
to 100,000 and is used by `auto`. Edit the object through the existing
`GET /admin/catalog` and `PUT /admin/catalog` flow with the current revision.
The existing panel preserves this field when publishing other edits.

The gate covers the four companion MCP methods and their JavaScript wrappers.
When disabled, discovery reports `enabled: false` and an empty `methods` array,
and calls to those methods return MCP method-not-found. The runtime skill and
TypeScript discovery describe only the registered operations. Business tool
code execution on `/mcp/code` is controlled independently of this skill gate.

The selection extension in `server/discover` reports `mode`, `threshold`,
`available_skills`, and `enabled`. Each publication recomputes activation and
publishes capabilities, methods, usage instructions, and the index together.
Already admitted requests use their original revision snapshot. Disabled
selection skips description-embedding preparation, including at startup.
Enabling selection with an embedding provider configured prepares its index
before publication.

The local measurements establish vector-search cost. Choosing always-on as a
general default still requires end-to-end skill-selection comparisons on small
catalogs, including selection accuracy, model tokens, and latency. Threshold 50
is an operator policy supplied for this implementation.

## Request flow

Check the selection extension's `enabled` flag before using the following flow.
When disabled, discover through `skills/list` and read the selected resources.

```javascript
const found = await tools.skills_search({
  query: "quotation",
  event: "quote.compare",
  limit: 10,
  max_bytes: 4096
});
if (!found.matches?.length) return found;
const selection = await tools.skills_resolve({
  revision: found.revision,
  uris: found.matches.map(skill => skill.uri),
  max_skills: 4,
  max_bytes: 12288
});
if (!selection.selected?.length) return selection;
return await tools.skills_load({
  revision: selection.revision,
  uris: selection.selected,
  max_skills: 4,
  max_bytes: 12288
});
```

For direct MCP use, the same arguments are the `params` of the corresponding
custom method. A caller with known URIs and a current revision can call
`skills/load` directly. `skills/describe` returns full manifests for explicit
URIs, including composition metadata, without reading bodies.

Each operation accepts `state`, the host's projection of confirmed string state
values. Supply the same state for search, describe, resolve, and load. Availability
and existing state guards are checked at every step. The authenticated caller
owns the accuracy of this projection. Quote call identity and memory binding
remain specific to `quote_total`.

## Index and bounds

An immutable inverted index is built once per catalog publication and shared by
the two endpoints. It indexes names, descriptions, and event names. Queries use
case-insensitive words and Chinese characters and adjacent character pairs.
Ranking uses the number of matching terms, then skill priority, then catalog
order. `event` is an optional exact filter. Search requires a query or event.
Search accepts `context` with a recent conversation window or summary. The
server concatenates `query` and `context` for retrieval. `search_mode` defaults
to `auto`. A configured embedding endpoint enables cosine similarity search
against skill descriptions. `semantic` requires embedding retrieval to succeed,
and `lexical` explicitly selects the inverted index. Event-only requests use
the inverted index.

Semantic retrieval applies availability, state guards, and the exact event
filter before admitting a candidate. It ranks by cosine similarity, then priority,
then catalog order. `min_similarity` ranges from -1 to 1 and defaults to 0.3.
This starting threshold needs calibration on representative contexts for the
chosen model. Each semantic match includes `similarity`.

Every search reply includes `retrieval`, either `semantic` or `lexical`.
In `auto` mode, `fallback_reason` reports `embedding_not_configured` or
`embedding_unavailable` when applicable. Provider failures and query embedding
timeouts trigger lexical fallback. Caller cancellation propagates as an error.
A semantic search that succeeds with zero matches returns `no_match`.

```javascript
const found = await tools.skills_search({
  context: "The customer wants to know how much it costs to cover their car.",
  search_mode: "auto",
  event: "quote.requested",
  limit: 5,
  max_bytes: 4096
});
```

### Embedding configuration

Set the complete OpenAI-compatible embeddings URL and model when starting the
service. For example, with a separately running local embedding service,

```sh
SKILLS_EMBEDDING_URL=http://127.0.0.1:8080/v1/embeddings
SKILLS_EMBEDDING_MODEL=text-embeddings-inference
```

The embedding endpoint receives the available skill descriptions at startup
and publication, and the supplied search text during retrieval. It accepts
batched string `input`, `model`, and `encoding_format: "float"`. Hugging Face
Text Embeddings Inference supports this protocol.
[TEI protocol documentation](https://huggingface.co/docs/text-embeddings-inference/quick_tour)

`SKILLS_EMBEDDING_TOKEN_FILE` optionally supplies a bearer token.
`SKILLS_EMBEDDING_QUERY_PREFIX` and `SKILLS_EMBEDDING_DOCUMENT_PREFIX` optionally
provide the prefixes required by the chosen model. Configure a fixed model
revision so cached description and query vectors use the same embedding space.
HTTPS endpoints require a trusted CA bundle in the runtime environment. The
scratch service image can receive it through a mounted `SSL_CERT_FILE`.

Descriptions are embedded in batches of 32, normalized, and stored as float32
vectors in the immutable catalog index. Unchanged descriptions reuse vectors
across publications. A query computes one vector and scans eligible vectors
using cosine similarity and a bounded top-k heap. Query vectors use a bounded
256-entry FIFO cache keyed by a hash of the input. Both caches live in memory.
A process restart rebuilds description vectors.

Single-provider query embedding has a 500 ms deadline. Description batches have a 10 second
HTTP deadline, and startup/publication preparation has a two minute deadline when selection is enabled.
Publication prepares the new index while the current revision serves requests,
then commits the catalog and index together after checking the revision again.
Embedding preparation failure leaves the current publication active and returns
an error. Startup with configured embeddings requires successful preparation.

### Embedding service pool

`SKILLS_EMBEDDING_POOL_FILE` optionally names an ordered JSON pool configuration
and takes precedence over the single-provider environment settings. It accepts
2–8 members with `name`, `url`, `model`, `revision`, and optional `token_file`,
`query_prefix`, and `document_prefix`. Names are unique and at most 64 characters.
Revision identifiers are at most 128 characters. Both start with an ASCII letter
or digit and otherwise allow letters, digits, `.`, `_`, `/`, and `-`. These limits
keep provider metadata within the selection reply budget. The configured model
revision must match the model served by the endpoint.

Each member owns a complete description index and query cache for its model.
Unchanged descriptions can be reused within that member across catalog
publications. Selection replies identify the chosen `embedding_provider` and
`embedding_revision`. Similarity thresholds apply independently to each model
and need calibration using representative requests.

The pool tries members in configured order within a shared 500 ms search budget.
Each attempted member gets an equal share of the remaining time reserved for
itself and later members. With two members, the first attempt has at most 250 ms.
A failed attempt opens that member's circuit for five seconds. Subsequent
requests then allow one recovery attempt while other requests use the backup.
A cached query can succeed without contacting the endpoint. Caller cancellation
propagates without declaring a provider unhealthy. In `auto` mode, exhaustion
of the pool falls back to lexical retrieval. Explicit `semantic` mode returns
an error.

Startup and publication prepare members concurrently and require at least one
complete index for the new catalog revision. An unavailable member is repaired
on a later search with a single background build, a two-minute limit and a
five-second retry cooldown. Publication fails when every member fails, retaining
the current catalog and index. Model vectors from an older catalog never fill
missing entries in the new catalog. Memory consumption includes one vector
index per ready member.

The deployed pair is BGE-M3 followed by Qwen3-Embedding-0.6B. Both services share
the ThinkPad and GPU, so host or GPU failure affects both. Deployment settings,
measured results and the isolated live-test scope are in the
[embedding service notes](../../services/skills-mcp/embeddings/README.md).

The scan is exact, with O(n*d + n*log(k)) time and O(k) temporary ranking space
for n eligible skills, d vector dimensions, and k retained results. The vector
store uses approximately n*d*4 bytes plus indexing overhead. Context embedding
latency is additional to scan time.

| Setting | Default | Maximum |
| --- | --- | --- |
| Search results, `limit` | 20 | 50 |
| Candidate URIs per request | Caller supplied | 50 |
| Selected skills including dependencies, `max_skills` | 4 | 16 |
| Encoded selection reply, `max_bytes` | 12,288 bytes | 16,384 bytes |
| Query | Caller supplied | 2,048 bytes |
| Conversation context | Caller supplied | 8,192 bytes |
| Catalog skills | Existing catalog | 100,000 |
| Serialized catalog | Existing catalog | 64 MiB |

`max_bytes` has a minimum of 1,024 and measures UTF-8 bytes of the JSON selection
payload, including escaped Markdown and metadata. The MCP envelope and outer
`execute_code` result wrapper have their own overhead. These are byte limits.
Search descriptions are clipped to 256 UTF-8 bytes and marked when clipped.
Search replies mark `truncated` when further candidates exist. Refine the query
or exact event to narrow a broad result set.

The count ceiling is an accepted-input limit. The essential scale test publishes
10,000 skills through the admin HTTP route and checks indexed retrieval and
bounded results. The semantic benchmark exercises 10,000 and 100,000 synthetic
384-dimensional vectors with a cached query, including filtering, top-k ranking,
and reply construction. Production memory use and latency at 100,000 skills require
deployment-specific measurement. Broad search terms can still visit many index
postings. Publication and legacy listing process the whole catalog.

## Composition metadata

Catalog schema 3 accepts three optional fields on each skill. Existing entries
use priority zero and empty relations.

```json
{
  "priority": 20,
  "requires": ["insurance-basics"],
  "conflicts": ["legacy-quotation"]
}
```

Relations refer to catalog skill names. Publication rejects missing references,
self references, dependency cycles, and dependency paths deeper than 64 edges.
Each relation list allows 16 names. Priority ranges from -1000 to 1000.
The manifest includes these fields under the selection extension key.
The existing admin JSON API accepts them, and panel edits preserve them.
These composition fields are enforced by the companion resolver and loader.
The original `outbound-turn` route keeps its event, text, and state matching behavior.

Resolution processes higher-priority candidates first and preserves input order
for ties. Dependencies are emitted before their dependent skill and deduplicated.
Conflicts work in either direction. Each candidate and its dependency closure
are accepted together or rejected together. Rejection reasons include
`unavailable_dependency`, `conflict`, `skill_limit`, `byte_budget`, and
`dependency_limit`. A closure can visit at most 64 distinct skills.

The result is `ok`, `partial`, or `no_match`, with selected URIs and rejection
reasons. Large rejection lists can be truncated to preserve the reply budget.
Resolution reserves space for a complete subsequent load, including manifests
and exact bodies. Skill bodies are never clipped to fit.

## Revision and loading semantics

Search returns a catalog revision. Describe, resolve, and load require that
revision. A changed or missing revision returns an MCP invalid-parameters error,
or a rejected promise when invoked through code mode. Refresh discovery before
trying again. Already admitted requests retain their original snapshot.

Load returns `resources`, each containing `manifest`, `mimeType`, and exact
Markdown `text`. The text and digest are identical to the existing resource
interface. Hosts verify URI, UTF-8 byte size, and SHA-256 against the manifest
before adding the instructions to their prompt.

If any requested root or dependency cannot be loaded within the constraints,
load returns `unresolved` with no bodies. Hosts can inspect the reasons and make
an explicit smaller selection. The companion does not silently omit required
dependencies or partially load a requested set.

## Essential checks

Run `go test -race ./...` and `go vet ./...` from `services/skills-mcp`.
`skill_selection_test.go` covers both MCP endpoints, JavaScript composition,
legacy API compatibility, digest equality, state eligibility, dependency and
conflict rules, byte/count bounds, stale publication revisions, cancellation,
invalid relation graphs, and a 10,000-skill HTTP publication.

`skill_embeddings_test.go` also covers semantic retrieval through both MCP
endpoints and JavaScript, description reuse, query cache bounds, batch size,
state and event filtering, thresholds, malformed provider responses, timeout
fallback, atomic publication failure, and readers during index preparation.
These tests use a deterministic HTTP embedding fixture. Evaluate real-model
relevance and end-to-end latency against the configured endpoint separately.

```sh
go test -run '^$' -bench '^BenchmarkSemanticSkillSearch$' -benchmem -benchtime=1s -count=3 .
```

### Local scan measurements

On 2026-11-14, the benchmark on an Apple M3 Max produced the following ranges
across three one-second runs. Each run requested ten results from identical
normalized synthetic 384-dimensional vectors and used a cached query vector.
The timing includes eligibility filtering, cosine scoring, ranking, and JSON
reply-budget checks. Model inference and network time require separate measurement.

| Catalog size | Search time per operation | Temporary allocation per operation |
| --- | --- | --- |
| 10,000 | 3.89–3.94 ms | Approximately 17.5 KB |
| 100,000 | 39.00–39.85 ms | Approximately 18 KB |

`skill_activation_test.go` covers the 49/50/51 boundaries, configured thresholds,
`always`/`off` overrides, available-skill counting, activation changes through
HTTP publication, persistence, both MCP endpoints, JavaScript discovery, and
embedding-preparation gating.

## Active Call integration

The host advertises `ai.liuchuanming.golive/turn-preparation` version `"1"`
in its per-request client capabilities. A compatible `outbound-turn` response
includes `prepared` metadata with exact resource bodies and manifests from the
same immutable catalog snapshot. The host verifies each linked manifest, digest,
byte count and frontmatter before rendering. Existing servers omit this metadata
and the host uses the original resource retrieval and revision-check sequence.
The engine still rechecks its confirmed memory version after I/O.

Below the configured selection threshold, preparation uses the existing routing
rules. When companion selection is enabled, matched rules retain precedence.
An automatic request with no rule match uses a bounded semantic/lexical search
for one root skill, followed by dependency resolution and batch load. Legacy
text exclusions and state guards apply to that automatic search. An unresolved
set fails preparation so the original prompt fallback remains available.
Search receives 100 ms within the existing host deadline, with lexical fallback
when that search budget expires. Traces include retrieval mode, fallback reason
and embedding provider/revision when applicable.

The `execute_code` outer `state` object is the host's confirmed string-state
projection. Every nested `skills_*` call receives that projection, overriding
script-provided state. Omitted outer state means an empty projection. Business
call identity and memory retain their existing outer-argument binding.
