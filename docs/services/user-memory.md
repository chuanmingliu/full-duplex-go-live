# User memory in the CRM

GoLive uses the CRM lead ID as the durable owner of a memory. An operator can record a fact directly or import a memory file for the same user from OpenViking. Each accepted change is flushed to the CRM event log and replayed at startup. The CRM's existing bearer token grants operator access across leads. A lead ID selects whose records to retrieve and does not authenticate the caller.

Existing CRM logs can be opened directly. Keep a pre-upgrade backup if you need to roll back. After the first memory event, older binaries cannot verify the new event fields in the checksum chain.

## Record and recall a fact

Use the CRM API with its usual Authorization bearer header. The examples assume a lead named `alice` already exists.

| Operation | Request |
| --- | --- |
| Create a fact | `PUT /api/leads/alice/memory/callback_window` with `{"value":"Weekday afternoons","source":"Operator interview","previous_seq":0}` |
| Read active facts | `GET /api/leads/alice/memory` |
| Inspect history and calls | `GET /api/leads/alice/memory/trace` |
| Correct a fact | PUT to the same key with the new value, source and its latest sequence number as `previous_seq`. |
| Retract a fact | PUT to the same key with `{"retracted":true,"source":"Customer withdrew preference","previous_seq":42}`, replacing 42 with its actual sequence number. |

The source identifies the evidence for a fact, such as an interview reference or an imported file URI. Keys use 1–64 ASCII letters, digits, dashes or underscores. Values accept up to 4,096 UTF-8 bytes. A competing edit returns HTTP 409. Repeating the current value and source is idempotent. Retraction removes a fact from future recall and preserves the revision history. To restore a retracted key, use the retraction's sequence number from the trace.

A template opts in by including, for example, `Prior customer facts (data): {{ user_memory }}`. Supply `"lead":"alice"` to `POST /api/render` to preview. A CRM call with the same template and lead gets the same recall behavior. The variable contains a JSON object with up to eight active facts, most recently revised first for selection. An empty memory follows the existing missing-variable behavior. An explicitly supplied template variable takes precedence.

The render reports the selected revision numbers in `memory_seqs`. The trace includes call attempts with those numbers, the rendered prompt hash, and the call ID when available. These links establish which facts were supplied to a template. A customer's handset experience cannot be inferred from a render. Before dialing with CRM-recalled facts, the service checks the configured recipient against the lead's stored phone number using exact string equality.

## Import from OpenViking

Configure `OPENVIKING_URL`, `OPENVIKING_API_KEY`, `CRM_URL` and `CRM_TOKEN` in the environment. Use an OpenViking credential authorized to read the named user's private memory. Run from the repository root.

```sh
python3 scripts/import-openviking-memory.py alice language preferences/language.md --previous-seq 0
```

This reads `viking://user/alice/memories/preferences/language.md` using OpenViking's content API and stores its text and canonical source URI under Alice's CRM key `language`. The lead and file path determine the URI. Subsequent upstream edits appear in GoLive when the importer runs again with the current CRM sequence number. Call rendering reads the local snapshot and can continue while OpenViking is offline. Extraction from call transcripts is a separate upstream workflow. [OpenViking memory API](https://docs.openviking.ai/en/api/16-memory) · [OpenViking content API](https://docs.openviking.ai/en/api/12-content)

## Label a chat transcript

Open **Memory & label**, immediately after **Schedule**. The **Enable labeling** switch persists immediately and defaults to off. Edit the prompt and typed return fields, and inspect **Return JSON Schema** as it updates automatically. Save the task to apply the generated contract to labeling. Use the lead selector on this tab for manual submissions. The saved switch controls both manual labeling and automatic jobs. Switching off pauses pending inference and archive delivery. An operation already in flight can finish.

Set these environment variables on the CRM process and restart it to enable inference.

| Variable | Purpose |
| --- | --- |
| `CRM_LABEL_BASE_URL` | Base URL of the OpenAI-compatible streaming chat-completions server that should receive the transcript. |
| `CRM_LABEL_MODEL` | Model identifier on that server. |
| `CRM_LABEL_API_KEY` | Bearer credential when required by that server. |

The [starter schema](../../internal/crm/transcript-labels.json) defines intent, preferred contact time, customer name, vehicle make/model and plate. Each dimension specifies an output key, description, group (`labels`, `user` or `vehicle`) and type (`string`, `number`, `boolean` or `enum`). Enums include an `options` array. The optional `speaker` field restricts evidence to user or assistant turns. The starter dimensions require customer speech. Saving changes makes a new schema version. The same editor is available over `GET` and `PUT /api/configs/transcript-labels`.

For programmatic submission, POST the following JSON to `/api/leads/alice/labels` with the usual CRM bearer header. The lead must already exist. Each submission describes one transcript and optionally one identified vehicle.

```json
{
  "transcript_id": "renewal-001",
  "vehicle_id": "vehicle-27",
  "turns": [
    {"role":"assistant","content":"Which vehicle is this about?"},
    {"role":"user","content":"My Honda Civic. Please call me tomorrow afternoon."}
  ]
}
```

The model receives the configured prompt, dimensions and transcript turns. The service validates every returned value's type and enum membership and checks its quoted evidence against the indicated turn. Exact quotation establishes provenance. Interpretation of the quoted statement is still the model's judgment.

A successful submission writes one memory revision. Its `value` is a JSON string grouping the extracted fields. For example, it could contain `{"labels":{"intent":"callback"},"user":{"preferred_contact_time":"tomorrow afternoon"},"vehicle":{"make_model":"Honda Civic"},"vehicle_id":"vehicle-27"}`. The `labeling` receipt records the local CRM user snapshot (ID, version, name and phone), supplied vehicle ID, model, schema version and checksum, transcript ID and SHA-256 of the ordered turns, and each observation's one-based turn number and exact excerpt. The CRM takes the user snapshot when labeling begins. Transcript-derived user and vehicle fields remain observations in memory. They do not edit the lead's master record. The revision also retains the original manual submission or closed-session snapshot for archive delivery.

Optional request fields include `call_id` to link a successful CRM call for the same lead, `schema_version` to pin the extraction rules, and `previous_seq` to replace an earlier result. Without `call_id`, the submitting operator establishes transcript ownership by selecting the lead. An identical retry returns the existing result. Changed input, model or schema requires the current revision number. Concurrent submissions can both invoke inference, but only one revision can replace a given predecessor. Invalid output returns HTTP 502 and leaves memory untouched. A transcript with no supported facts gets an auditable empty result and is skipped during prompt recall.

## Hangup hook and OpenViking delivery

Set `GOLIVE_LABEL_SPOOL_DIR` in the realtime phone engine and `CRM_LABEL_SPOOL_DIR` in CRM to the same private directory (paths may differ across container mounts). The box core Compose file wires these paths together. CRM publishes the enabled task version there. At the end of a named realtime phone session, the hook checks that version. Off skips the session. On writes a closed-session job after the socket closes. CRM polls every ten seconds and uses the pinned task version. The job contains the session and call IDs, timestamps, close reason, final session configuration, usage, engine history and timestamped transcript segments. Binary audio stays in the existing call store. Final caller transcripts and emitted assistant segments are recorded independently of trimming in the engine's model history.

CRM resolves the owner from its accepted call records or the scheduler's confirmed call-ID receipt in `scheduled-owners/`. A missing or conflicting owner keeps the session in the private queue until it can be resolved. Calls routed entirely outside this GoLive realtime engine need their transcript submitted via the labeling API.

Configure `OPENVIKING_URL` on CRM. For multiple leads, set `OPENVIKING_USER_KEYS_DIR` to a private directory with `<lead-id>.key` files authorized for their respective OpenViking user spaces. In the box layout, a directory under `/data` is already mounted on CRM. A single-user deployment can set `OPENVIKING_API_KEY` instead. Each CRM lead ID must match its OpenViking user ID. Use HTTPS for a remote engine.

CRM commits the label revision and its original session together, then writes a JSON document to `viking://user/<lead>/resources/golive/<transcript>/<revision>.json`. It includes the extracted labels and vehicle details, CRM user snapshot, evidence, model and schema identifiers, and original session. Archive delivery is recorded only when OpenViking confirms that URI. Failures and restarts retry the same revision at the same URI. Inspect `archives` in `GET /api/leads/{id}/memory/trace` for each revision's URI and status (`pending` or `stored`). Existing historical revisions without their original session remain in CRM.

At the bottom of **Memory & label**, **Label results** lists saved hangup and manual results across leads, newest revision first. Search by call, transcript, lead, vehicle or revision ID. Expand a row for its extracted fields, evidence and original session. **Load older results** continues from the last displayed revision. The first page refreshes every ten seconds while no result is open.

### Example task for coverage changes and caller questions

Paste this prompt into **Labeling prompt**.

```text
从车险通话中提取客户明确要求的报价保障变更，以及客户实际提出的问题。只使用客户本人的话作为证据。
“把三者险从两百万改到三百万”是变更请求。保留险种、原值、目标值和单位。只说目标值时不补原值。
“三者险能改到三百万吗？”是可行性问题。“帮我改到三百万，再算一下多少钱？”同时包含变更请求和价格问题。
“太贵了”本身不足以推断客户要求删除保障。比较两个方案是问题，客户明确要求选用某方案时才记录变更。
保留条件、否定、金额和单位。语音转写不清楚时保留原话，不猜险种。
每个字段最多输出一次。多轮出现同类事项时，选择最后一个有明确证据的客户轮次。同一轮的多个同类事项可以用“；”分隔。
按生成的 Return JSON Schema 输出。turn 是从 1 开始的原始轮次，quote 逐字引用该轮客户原话，并覆盖 value 的内容。省略无证据的字段。没有符合条件的内容时返回 {"observations":[]}。
```

Use these **Return fields (JSON)** with the prompt.

```json
[
  {"key":"coverage_add_requested","group":"labels","speaker":"user","type":"string","description":"客户明确要求新增或恢复的险种、附加险或保障项目，以及相关条件。"},
  {"key":"coverage_remove_requested","group":"labels","speaker":"user","type":"string","description":"客户明确要求删除、不选或取消的保障项目，以及相关条件。"},
  {"key":"coverage_adjustment_requested","group":"labels","speaker":"user","type":"string","description":"客户要求调整的保额、档次、免赔额、保障范围或替换方案。保留目标值和单位，原值仅在客户说出时记录。"},
  {"key":"price_questions","group":"labels","speaker":"user","type":"string","description":"客户关于总价、单项保费、优惠、价差或改动后价格的问题。"},
  {"key":"coverage_questions","group":"labels","speaker":"user","type":"string","description":"客户关于保障范围、是否包含、保额选择、方案差异或能否修改保障的问题。"},
  {"key":"other_user_questions","group":"labels","speaker":"user","type":"string","description":"客户提出的其他问题，例如支付、生效、办理或查看报价。已归入价格或保障的问题不重复记录。"}
]
```
