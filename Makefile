# Restricted networks can set GOPROXY=direct. Checksum verification stays on.
GOFLAGS ?=

BIN := bin
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
LDFLAGS := -s -w -X main.version=$(VERSION) -X main.commit=$(COMMIT)

# Platforms shipped in a dist bundle, so the project runs on a machine with no
# Go toolchain.
PLATFORMS := darwin/arm64 darwin/amd64 linux/amd64 linux/arm64

.PHONY: all build test race vet fmt run run-mock run-sim run-fdb-sim demo bench bench-sim bench-fdb turnbench-sim ttsprobe dist bundle sim-bundle clean tidy

all: vet test build

build:
	@mkdir -p $(BIN)
	go build -ldflags "$(LDFLAGS)" -o $(BIN)/golive ./cmd/golive
	go build -ldflags "$(LDFLAGS)" -o $(BIN)/golivectl ./cmd/golivectl
	go build -ldflags "$(LDFLAGS)" -o $(BIN)/golivebench ./cmd/golivebench
	go build -ldflags "$(LDFLAGS)" -o $(BIN)/goliveturn ./cmd/goliveturn
	go build -ldflags "$(LDFLAGS)" -o $(BIN)/golivefdb ./cmd/golivefdb
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

## run-fdb-sim: the telephony profile's turn-taking on the mock providers, for
## the full-duplex benchmark. See FULLDUPLEX-BENCH.md.
run-fdb-sim: build
	$(BIN)/golive -profile configs/fdb-sim.json -env configs/fdb-sim.env

## turnbench-sim: a smoke test of the TurnBench adapter on the mocks — a
## toy two-channel conversation, golive detector-only on each channel, the
## predictions compared with what the toy was built to contain.
turnbench-sim: build
	@python3 scripts/turnbench/synth.py /tmp/golive-turnbench-toy
	@GOLIVE_MOCK_PHRASES='so what I was thinking is that we should do it next week|yeah' \
	 $(BIN)/golive -profile configs/turnbench-sim.json -env configs/turnbench-sim.env > /tmp/golive-turnbench-toy/golive.log 2>&1 & echo $$! > /tmp/golive-turnbench-toy/pid; \
	 sleep 2; \
	 $(BIN)/goliveturn -data /tmp/golive-turnbench-toy -url ws://127.0.0.1:8091/v1/live -out /tmp/golive-turnbench-toy/predictions.json -v; \
	 kill `cat /tmp/golive-turnbench-toy/pid`; \
	 echo "expected:"; cat /tmp/golive-turnbench-toy/toy/gold.json

## bench-fdb: the full-duplex benchmark against a server started with
## run-fdb-sim: murmurs, hesitations, interruptions, asides, pauses, a tool
## task, scored as τ-voice and Full-Duplex-Bench score them.
bench-fdb: build
	$(BIN)/golivefdb -url ws://127.0.0.1:8089/v1/live -sessions 2 -episodes 6 -parallel 4 \
		-md bench-fdb.md -json bench-fdb.json

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

## sim-bundle: the simulated customer, built from its own branch (SIM_REF)
## into dist/golive-sim.tar.gz. See "The simulated customer" in TELEPHONY.md.
sim-bundle:
	@scripts/sim-bundle.sh $(SIM_REF)

## bundle: everything a phone-line box with no Go needs, in one archive: linux
## binaries, the telephony profile, the scripts. Unpack it there and run
## scripts/phone-brain.sh golive. See TELEPHONY.md.
BUNDLE := dist/golive-telephony
bundle:
	@rm -rf $(BUNDLE) && mkdir -p $(BUNDLE)/bin $(BUNDLE)/configs $(BUNDLE)/scripts
	@for c in golive golivebench golivefdb goliveturn golivectl llmprobe ttsprobe echoprobe journal crm edge desk quotesvc; do \
		CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags "$(LDFLAGS)" \
			-o $(BUNDLE)/bin/$$c-linux-amd64 ./cmd/$$c || exit 1; \
	done
	@CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags "$(LDFLAGS)" \
		-o $(BUNDLE)/bin/golive-linux-arm64 ./cmd/golive
	@cp configs/*.json configs/sim.env $(BUNDLE)/configs/
	@cp scripts/*.sh $(BUNDLE)/scripts/ && mkdir -p $(BUNDLE)/scripts/turnbench && cp scripts/turnbench/*.py $(BUNDLE)/scripts/turnbench/
	@cp start.sh TELEPHONY.md .env.example $(BUNDLE)/
	@cp -r journal $(BUNDLE)/journal
	@mkdir -p $(BUNDLE)/web && cp web/index.html $(BUNDLE)/web/
	@echo "$(COMMIT) $$(date -u +%FT%TZ) $$(git rev-parse --abbrev-ref HEAD 2>/dev/null)" > $(BUNDLE)/VERSION
	@tar -C dist -czf dist/golive-telephony.tar.gz golive-telephony
	@echo "dist/golive-telephony.tar.gz  $$(cat $(BUNDLE)/VERSION)"

clean:
	rm -rf $(BIN) dist *.wav golive.log bench.md bench.json bench-sim.md bench-sim.json

## eval: the standing evaluation on the mocks, L2 (golivefdb) and L3
## (golivebench), recorded as journal/benchmarks/<date>-<build>-<layer>.json
## so a commit carries its own scorecard. Starts golive on the fdb mocks,
## runs both, stops it. The journal's Benchmarks tab reads the directory.
eval: build
	@$(BIN)/golive -profile configs/fdb-sim.json -env configs/fdb-sim.env > eval-golive.log 2>&1 & echo $$! > .eval.pid; \
	for i in 1 2 3 4 5 6 7 8 9 10; do curl -sf http://127.0.0.1:8089/healthz > /dev/null && break; sleep 0.5; done; \
	$(BIN)/golivefdb -url ws://127.0.0.1:8089/v1/live -sessions 1 -episodes 3 -parallel 6 \
		-md bench-fdb.md -json bench-fdb.json -record journal/benchmarks -source mocks; rc=$$?; \
	$(BIN)/golivebench -a golive-mocks=ws://127.0.0.1:8089/v1/live/sessions \
		-synth-ms 700,1500,2400,3300 -runs 6 -warmup 1 -md bench-sim.md -json bench-sim.json -record journal/benchmarks -source mocks; rc2=$$?; \
	kill $$(cat .eval.pid) 2>/dev/null; rm -f .eval.pid; \
	ls -t journal/benchmarks | head -2; exit $$(( rc + rc2 ))
