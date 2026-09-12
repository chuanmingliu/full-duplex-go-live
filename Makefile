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

.PHONY: all build test race vet fmt run run-mock demo dist clean tidy

all: vet test build

build:
	@mkdir -p $(BIN)
	go build -o $(BIN)/golive ./cmd/golive
	go build -o $(BIN)/golivectl ./cmd/golivectl

test:
	go test ./...

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

## dist: cross-compile every shipped platform into bin/, so ./start.sh works
## without Go. Run this before packaging the project for someone else.
dist:
	@mkdir -p $(BIN)
	@for p in $(PLATFORMS); do \
		os=$${p%/*}; arch=$${p#*/}; \
		echo "  building $$os/$$arch"; \
		GOOS=$$os GOARCH=$$arch go build -trimpath -ldflags "-s -w" \
			-o $(BIN)/golive-$$os-$$arch ./cmd/golive; \
		GOOS=$$os GOARCH=$$arch go build -trimpath -ldflags "-s -w" \
			-o $(BIN)/golivectl-$$os-$$arch ./cmd/golivectl; \
	done
	@ls -lh $(BIN)

clean:
	rm -rf $(BIN) *.wav golive.log
