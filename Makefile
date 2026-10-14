# Restricted networks can set GOPROXY=direct. Checksum verification stays on.
GOFLAGS ?=

BIN := bin
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
LDFLAGS := -s -w -X main.version=$(VERSION) -X main.commit=$(COMMIT)

# Platforms shipped in a dist bundle, so the project runs on a machine with no
# Go toolchain.
PLATFORMS := darwin/arm64 darwin/amd64 linux/amd64 linux/arm64

.PHONY: all build test race vet fmt run run-mock run-sim demo bench bench-sim ttsprobe dist bundle clean tidy

all: vet test build

build:
	@mkdir -p $(BIN)
	go build -ldflags "$(LDFLAGS)" -o $(BIN)/golive ./cmd/golive
	go build -ldflags "$(LDFLAGS)" -o $(BIN)/golivectl ./cmd/golivectl
	go build -ldflags "$(LDFLAGS)" -o $(BIN)/golivebench ./cmd/golivebench
	go build -ldflags "$(LDFLAGS)" -o $(BIN)/ttsprobe ./cmd/ttsprobe
	go build -ldflags "$(LDFLAGS)" -o $(BIN)/echoprobe ./cmd/echoprobe

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

## run-prod: production profile (auth required, no demo page, json logs)
run-prod: build
	$(BIN)/golive -profile configs/production.json -env .env.local

## run-mock: start with mock providers; no credentials needed
run-mock: build
	$(BIN)/golive -profile configs/mock.json -env /dev/null

## run-sim: mock providers with realistic stage latencies injected, for
## measuring the engine's own latency work. See configs/sim.env.
run-sim: build
	$(BIN)/golive -profile configs/sim.json -env configs/sim.env

## bench-sim: measure a server started with run-sim. Four utterance lengths,
## because a turn's length decides which path through the engine it takes.
bench-sim: build
	$(BIN)/golivebench -a golive-sim=ws://127.0.0.1:8080/v1/live/sessions \
		-synth-ms 700,1500,2400,3300 -runs 12 -warmup 1 -v -md bench-sim.md -json bench-sim.json

## demo: drive a full turn, then interrupt it, and save both sides
demo: build
	$(BIN)/golivectl -speak-ms 2200 -barge-in-at 1400 -record session.wav

## bench: A/B this service against an OpenAI Realtime stack. See BENCHMARK.md.
## Override CASCADE= to point at yours; CLIPS= to use real recordings.
CASCADE ?= ws://127.0.0.1:8765/v1/realtime
CLIPS   ?=
bench: build
	$(BIN)/golivebench -a golive=ws://127.0.0.1:8080/v1/live/sessions -b cascade=$(CASCADE) \
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
		for c in golive golivectl golivebench ttsprobe echoprobe; do \
			GOOS=$$os GOARCH=$$arch go build -trimpath -ldflags "$(LDFLAGS)" \
				-o $(BIN)/$$c-$$os-$$arch ./cmd/$$c; \
		done; \
	done
	@ls -lh $(BIN)

## bundle: everything a phone-line box with no Go needs, in one archive: linux
## binaries, the telephony profile, the scripts. Unpack it there and run
## scripts/phone-brain.sh golive. See TELEPHONY.md.
BUNDLE := dist/golive-telephony
bundle:
	@rm -rf $(BUNDLE) && mkdir -p $(BUNDLE)/bin $(BUNDLE)/configs $(BUNDLE)/scripts
	@for c in golive golivebench golivectl llmprobe ttsprobe echoprobe journal crm edge; do \
		CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags "$(LDFLAGS)" \
			-o $(BUNDLE)/bin/$$c-linux-amd64 ./cmd/$$c || exit 1; \
	done
	@CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags "$(LDFLAGS)" \
		-o $(BUNDLE)/bin/golive-linux-arm64 ./cmd/golive
	@cp configs/*.json configs/sim.env $(BUNDLE)/configs/
	@cp scripts/*.sh $(BUNDLE)/scripts/
	@cp start.sh TELEPHONY.md .env.example $(BUNDLE)/
	@cp -r journal $(BUNDLE)/journal
	@mkdir -p $(BUNDLE)/web && cp web/index.html $(BUNDLE)/web/
	@echo "$(COMMIT) $$(date -u +%FT%TZ) $$(git rev-parse --abbrev-ref HEAD 2>/dev/null)" > $(BUNDLE)/VERSION
	@tar -C dist -czf dist/golive-telephony.tar.gz golive-telephony
	@echo "dist/golive-telephony.tar.gz  $$(cat $(BUNDLE)/VERSION)"

clean:
	rm -rf $(BIN) dist *.wav golive.log bench.md bench.json bench-sim.md bench-sim.json
