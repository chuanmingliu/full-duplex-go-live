# Tracking events for an active call

The business tracking service resolves the customer identity to the active telephone call ID. It sends `addV` only after confirming successful WeChat addition. GoLive accepts the callback on the box and updates the matching Active Call session.

The endpoint is `POST /v1/calls/{call_id}/events` on the GoLive engine. Authenticate with the dedicated `GOLIVE_BUSINESS_EVENT_TOKEN` in the Authorization bearer header. The credential is environment-only and independent of the phone client's session credential. An empty credential disables the endpoint.

```json
{
  "name": "addV",
  "event_id": "tracking-event-uuid",
  "source": "tracking"
}
```

Use the exact call ID returned by the dial operation. Customer IDs or telephone numbers cannot substitute for it. Send `addV` after WeChat addition and `product_page.opened` after the tracking backend confirms the product page opened. For a deliberate test, use `source: "simulation"`. The service log records that source. The tracking service is responsible for verifying the customer action and correlating it to a call.

## Protocol and delivery

This is an authenticated HTTP JSON callback. The `golive.business.event` WebSocket extension also supports the existing `addV` delivery. Product-page events require the dedicated HTTP callback credential. MCP remains the protocol for skill discovery, guidance retrieval and tools.

A 202 response reports that the event was queued to the live session. The resulting `golive.memory` event carries `wechat.status=added` on the session/sideband stream. Repeated `addV` delivery is idempotent for that session, including retries with a different event ID. The event ID is echoed for correlation; it is not a durable deduplication ledger.

## Caller-message guidance

In **Leads → Configs → User prompt**, enable the template and save a version. A new call loads that version at startup. For each caller turn the LLM request keeps the configured system instructions and existing chat history, then adds rendered guidance to the current `user` message. The original caller text remains in the message and in stored history. Skill selection uses the original caller text. Other host guidance and tool messages retain their roles and ordering.

The template supports conditional Jinja syntax and exposes `caller_text`, `events.wechat_added` and `events.product_page_opened`. The event values are booleans for the current call. They reflect backend-confirmed actions at the start of the request. An event received while the assistant is already speaking becomes available to a subsequent caller turn.

```jinja
{% if events.product_page_opened %}
用户已打开产品页面。若询问调整保障，引导用户在当前页面选择对应保障并核对保费。
{% elif events.wechat_added %}
用户已添加车险管家。根据当前问题说明如何找到产品页面。
{% endif %}
```

With this example and no confirmed event, GoLive sends the caller's words with no added guidance. Disabled or unavailable templates and render failures also preserve the original request. The product-page event updates state for future prompts without initiating speech. A real tracking producer must emit the callback for the flag to change during a live call.

## Configurable callback speech

The Skills & tools panel has a **Callback responses** section. Enable **Speak a confirmation after addV**, enter one or more lines, then publish. The catalog stores the binding in `event_responses.addV`.

```json
{
  "event_responses": {
    "addV": {
      "enabled": true,
      "templates": [
        "微信已经添加成功了，我们继续刚才的话题。"
      ]
    }
  }
}
```

Each binding accepts up to 12 distinct single-line templates, each at most 300 Unicode characters. Text is spoken verbatim. An enabled binding requires at least one template. The editor publishes atomically using its catalog revision, rejecting concurrent stale edits. Existing catalogs without this field retain their prior behavior.

After applying the trusted state update, Active Call fetches `golive://event-responses/addV` using a standard MCP `resources/read` request. This single request shares the configured skills timeout, 150 ms by default, and runs off the audio loop. A published edit takes effect on the next callback without restarting either service. A callback already being handled retains the fetched revision.

One template is chosen randomly. The response waits for the caller, ASR finalization, interruption/echo decision, active model/audio output, playback receipts, tool acknowledgments, quotation tasks and pending result deliveries. A currently recognized WeChat guidance turn is canceled when completion makes it stale. Quotation calculations and unrelated answers retain their normal scheduling. Silence reminders yield to the queued callback response.

Speech follows the normal player and canonical transcript path, including guarded interruptions and output accounting. The binding is consumed when speech starts, so duplicate events and interruptions cannot cause repeated announcements. A disabled, missing, invalid or unavailable resource preserves the state update and the previous conversational fallback. Call closure cancels lookup and discards pending speech.

`business: response prepared` records lookup status, revision and elapsed time. `business: response started` records the selected template index, turn and time waiting since the callback. This marks speech scheduling. The corresponding player output and transcript establish when audio actually starts. Keep quotation calculation timing separate.

The tracking ingress remains HTTP JSON. MCP transports the maintained response configuration. The actual tracking producer must still be connected when available.

| HTTP status | Meaning |
| --- | --- |
| 202 | Queued to the matching initialized session |
| 400 | Unsupported event, malformed body or invalid source |
| 401 / 429 | Invalid credential or authentication throttling |
| 404 | No matching active call |
| 409 | Ambiguous, closing or uninitialized call |
| 503 | Callback credential is unconfigured |

Callbacks target live sessions only. A new call must receive its own authoritative initial state or callback. An event arriving during WeChat guidance cancels that stale guidance and replans. An event arriving during quote calculation preserves the quote when its pricing inputs remain the same.

## Test coverage

Server tests cover the Realtime/Active Call session, separate callback authentication, disabled defaults, invalid payloads, unknown calls, uninitialized sessions, ambiguous call IDs, duplicate delivery and isolation from another concurrent call. Quotation and interruption behavior is covered by the [real-model simulation report](../telephony/2026-11-09-skills-fixes.md).

## Deployment and rollback

The original saved prompt, greeting, voice and recipient remain the call inputs. The callback credential is added to the private engine environment alongside the optional skills endpoint configuration. Preserve the previous environment and engine binary before activating it. Restoring those files disables the callback and returns to the previous engine implementation. The independent MCP catalog rollback also needs its saved schema-compatible catalog snapshot.
