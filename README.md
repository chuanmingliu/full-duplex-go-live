# golive local quotation service

`codex/service-quotation` maintains the local vehicle quotation backend used
by the test deployment. It implements the business-facing inquiry interface,
vehicle/rating data and asynchronous pricing execution.

## Functionality

- Accepts a vehicle quotation inquiry and computes its pricing result.
- Applies configured computation delay, jitter and worker concurrency.
- Serves results through the matching business query interface.
- Retains vehicle/rating data, access token and quotation history.

This service performs pricing. CRM separately owns prompt templates, quotation
prefetch and how a result is presented during a conversation.

## Owned source and API

| Path | Responsibility |
| --- | --- |
| [cmd/quotesvc](cmd/quotesvc) | CLI, worker/delay settings, listeners and lifecycle |
| [internal/quotesvc](internal/quotesvc) | Vehicle data, pricing, persistence and HTTP handlers |

The observed box listener is `127.0.0.1:8793`. CRM uses the `/intranet` base URL.
Routes include:

- `GET /healthz`
- `POST /intranet/vehicle/innerProcess/vehicleApplyInquiryPriceAsync`
- `POST /intranet/vehicle/simpleProcess/queryLastQuoteDetailByPhone`
- `GET /api/quotes`

The service retains its current authentication contract; use the private token
and existing CRM configuration for business requests.

## Build and isolated check

Use the Go version in [go.mod](go.mod), from the repository root:

```sh
go test ./internal/quotesvc
go build -o /tmp/golive-quotesvc ./cmd/quotesvc
QUOTE_DEV_DIR=$(mktemp -d)
/tmp/golive-quotesvc -addr 127.0.0.1:18793 -dir "$QUOTE_DEV_DIR" \
  -compute-ms 2500 -jitter-ms 2000 -workers 2
```

Check from another terminal:

```sh
curl --fail http://127.0.0.1:18793/healthz
```

Use synthetic vehicle data for tests. The `-add-vehicle` flag writes a vehicle
to the selected state directory; do not point development commands at live data.

## Maintenance boundary

The live state is `~/golive-quotesvc`. Back it up before changes and preserve
pricing inputs, token and history. The [staged `quotesvc` Compose service](deploy/services/box/core.compose.yml)
is selected by the `quotes` profile; it has not replaced the native process.
See [the migration contract](deploy/services/README.md) before adoption.

This branch changes quotation functionality and necessary shared-code
integration with coordinated review. It keeps the shared repository source
present; it does not own the phone engine or CRM's presentation behavior.
