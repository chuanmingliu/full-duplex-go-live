# The module proxy is not always reachable from build environments behind a
# restricted egress policy; fetching straight from the source hosts is.
GOFLAGS ?=
GOPROXY ?= direct
GOSUMDB ?= off
export GOPROXY
export GOSUMDB

BIN := bin

# Platforms shipped in a dist bundle, so the project runs on a machine with no
# Go toolchain.
PLATFORMS := darwin/arm64 darwin/amd64 linux/amd64

.PHONY: all build test race vet fmt run run-mock demo bench ttsprobe dist clean tidy

all: vet test build

build:
	@mkdir -p $(BIN)
	go build -o $(BIN)/golive ./cmd/golive
	go build -o $(BIN)/golivectl ./cmd/golivectl
	go build -o $(BIN)/golivebench ./cmd/golivebench
	go build -o $(BIN)/ttsprobe ./cmd/ttsprobe

## test: the Go suite, plus the demo page's transcript bookkeeping. The page is
## where a session is read, so a defect there is indistinguishable from one in
## the engine — it needs a test even though it is not Go.
test:
	go test ./...
	@command -v node >/dev/null 2>&1 && node web/transcript_test.mjs \
		|| echo "skipping web/transcript_test.mjs: node is not installed"

race:
	go test -race ./...

vet:
	go vet ./...

fmt:
	gofmt -l -w .

tidy:
	go mod tidy

## run: start with the real provider stack (needs .env.local)
run: build
	$(BIN)/golive -profile configs/tencent-deepseek-minimax.json -env .env.local

## run-mock: start with mock providers; no credentials needed
run-mock: build
	$(BIN)/golive -profile configs/mock.json -env /dev/null

## demo: drive a full turn, then interrupt it, and save both sides
demo: build
	$(BIN)/golivectl -speak-ms 2200 -barge-in-at 1400 -record session.wav

## bench: A/B this service against an OpenAI Realtime stack. See BENCHMARK.md.
## Override CASCADE= to point at yours; CLIPS= to use real recordings.
CASCADE ?= ws://127.0.0.1:8765/v1/realtime
CLIPS   ?=
bench: build
	$(BIN)/golivebench -a golive=ws://127.0.0.1:8080/v1/live -b cascade=$(CASCADE) \
		$(if $(CLIPS),-clips "$(CLIPS)",) -runs 12 -warmup 2 -v -md bench.md -json bench.json

## ttsprobe: measure time-to-first-audio from the synthesis provider alone,
## across both MiniMax endpoints. See BENCHMARK.md.
ttsprobe: build
	$(BIN)/ttsprobe -compare-endpoints -runs 12 -json ttsprobe.json

## dist: cross-compile every shipped platform into bin/, so ./start.sh works
## without Go. Run this before packaging the project for someone else.
dist:
	@mkdir -p $(BIN)
	@for p in $(PLATFORMS); do \
		os=$${p%/*}; arch=$${p#*/}; \
		echo "  building $$os/$$arch"; \
		for c in golive golivectl golivebench ttsprobe; do \
			GOOS=$$os GOARCH=$$arch go build -trimpath -ldflags "-s -w" \
				-o $(BIN)/$$c-$$os-$$arch ./cmd/$$c; \
		done; \
	done
	@ls -lh $(BIN)

clean:
	rm -rf $(BIN) *.wav golive.log bench.md bench.json
