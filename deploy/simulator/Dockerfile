FROM golang:1.26.2-bookworm@sha256:47ce5636e9936b2c5cbf708925578ef386b4f8872aec74a67bd13a627d242b19 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG SOURCE_COMMIT=dev
RUN mkdir -p /empty
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X main.commit=${SOURCE_COMMIT}" -o /golive-simulator ./cmd/golive-simulator
FROM gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab
COPY --from=build --chown=65532:65532 /empty /data
COPY --from=build --chown=65532:65532 /empty /gateway
COPY --from=build /golive-simulator /golive-simulator
COPY configs/simulator /configs/simulator
ENTRYPOINT ["/golive-simulator"]
CMD ["-profile", "/configs/simulator/mock.json", "-addr", ":18865", "-data", "/data"]
