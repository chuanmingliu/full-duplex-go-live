# golive CRM and call preparation

`codex/service-crm` maintains the lead service: versioned prompt templates,
rendered call instructions, business configuration and call preparation.
It also exposes the business tools used during a conversation.

## Functionality and interfaces

- Renders templates against lead data and retains identifiable rendered prompts.
- Stores versioned business configuration, including quotation plans and speech.
- Prepares quotation results and handles the conversation's quote tool requests.
- Submits authorized outbound requests to Aria after checking the phone session state.
- Serves a token-protected API/UI; `/healthz` is the unauthenticated health endpoint.

The observed box API listens on port 8791. Key routes include `/api/templates`,
`/api/configs`, `/api/render`, `/api/call`, `/api/tools/quote` and `/api/quote/prefetch`.
Posting to `/api/call` is a real call action when connected to the live originator.

## Owned source

| Path | Responsibility |
| --- | --- |
| [cmd/crm](cmd/crm) | Process flags, listeners and lifecycle |
| [internal/crm](internal/crm) | Templates, business config, tools, quotation preparation and persistence |
| [internal/crm/ui](internal/crm/ui) | CRM browser UI |

This is a service workstream inside the shared repository. Changes belong to CRM
and necessary shared-code integration with coordinated review. Other service
code remains present but is outside this branch's maintenance scope.

## Build and isolated check

Use the Go version in [go.mod](go.mod), from the repository root:

```sh
go test ./internal/crm
go build -o /tmp/golive-crm ./cmd/crm
CRM_DEV_DIR=$(mktemp -d)
/tmp/golive-crm -addr 127.0.0.1:18791 -dir "$CRM_DEV_DIR" \
  -originator http://127.0.0.1:1 -originator-env /dev/null \
  -golive http://127.0.0.1:1
```

This isolated instance has a fresh state directory and unreachable call
backends. It is not a production call launcher. From another terminal:

```sh
curl --fail http://127.0.0.1:18791/healthz
```

## Dependencies and durable state

Live calling requires the configured Aria originator, its private allowed-number
configuration and the golive session-state endpoint. Quotation requests use the
persisted `quote-api.base_url`; a nonempty `CRM_QUOTE_API_BASE` overrides that URL.
The separate quotation backend owns pricing execution.

The live state is under `~/golive-crm`, including `api.token`, templates,
configuration and event history. Preserve the whole directory and permissions.
Use `crm -dir <state-directory> -verify` to check its event-log checksums.
Do not commit lead data, rendered prompts or tokens.

CRM has an independent native lifecycle; restarting the phone does not upgrade
CRM. The [CRM Compose service](deploy/services/box/core.compose.yml) is staged.
Follow [migration requirements](deploy/services/README.md) before adoption and
verify call preparation and quotation behavior after any replacement.
