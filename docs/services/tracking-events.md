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

Use the exact call ID returned by the dial operation. Customer IDs or telephone numbers cannot substitute for it. The current implementation supports `addV` only. For a deliberate test, use `source: "simulation"`; the service log records that source. The future tracking service remains responsible for verifying the real customer action and its call correlation.

## Protocol and delivery

This is an authenticated HTTP JSON callback. Internally it invokes the same engine handler as the `golive.business.event` WebSocket extension. MCP remains the protocol for skill discovery, guidance retrieval and tools. The tracking callback uses HTTP JSON.

A 202 response reports that the event was queued to the live session. The resulting `golive.memory` event carries `wechat.status=added` on the session/sideband stream. Repeated `addV` delivery is idempotent for that session, including retries with a different event ID. The event ID is echoed for correlation; it is not a durable deduplication ledger.

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
