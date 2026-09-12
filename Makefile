# The module proxy is not always reachable from build environments behind a
# restricted egress policy; fetching straight from the source hosts is.
GOFLAGS ?=
GOPROXY ?= direct
GOSUMDB ?= off
export GOPROXY
export GOSUMDB

BIN := bin

.PHONY: all build test race vet fmt run run-mock demo clean tidy

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

clean:
	rm -rf $(BIN) *.wav
