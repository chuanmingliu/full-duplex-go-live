# syntax=docker/dockerfile:1
FROM golang:1.24-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
ARG COMMIT=unknown
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION} -X main.commit=${COMMIT}" \
	-o /out/golive ./cmd/golive

FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates wget \
	&& rm -rf /var/lib/apt/lists/* \
	&& useradd --system --home /app --uid 65532 --gid nogroup golive
WORKDIR /app
COPY --from=build /out/golive /usr/local/bin/golive
COPY configs /app/configs
USER golive
EXPOSE 8080
HEALTHCHECK --interval=15s --timeout=3s --start-period=5s \
	CMD wget -q -O- http://127.0.0.1:8080/livez >/dev/null || exit 1
ENTRYPOINT ["golive"]
CMD ["-profile", "configs/production.json"]
