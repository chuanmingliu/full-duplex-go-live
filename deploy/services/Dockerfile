# syntax=docker/dockerfile:1
# Build from the repository root, preferably from a clean, verified checkout.
# The runtime contains only owned service binaries and public web assets.
FROM golang:1.24-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
ARG SOURCE_COMMIT
RUN set -eu; test -n "$SOURCE_COMMIT"; mkdir -p /out; \
    for service in golive crm journal desk quotesvc edge; do \
      CGO_ENABLED=0 go build -trimpath \
        -ldflags "-s -w -X main.version=$SOURCE_COMMIT -X main.commit=$SOURCE_COMMIT" \
        -o "/out/$service" "./cmd/$service"; \
    done

FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates tzdata wget \
    && rm -rf /var/lib/apt/lists/* \
    && useradd --system --home /app --uid 65532 --gid nogroup golive
WORKDIR /app
COPY --from=build /out/ /usr/local/bin/
COPY web /app/web
COPY configs/mock.json /app/configs/mock.json
ARG SOURCE_COMMIT
LABEL org.opencontainers.image.source="https://github.com/chuanmingliu/full-duplex-go-live" \
      org.opencontainers.image.revision="$SOURCE_COMMIT"
USER golive
ENTRYPOINT ["golive"]
CMD ["-profile", "/app/configs/mock.json", "-env", "/dev/null"]
