# Agent Chat — build & dev tasks
# All paths assume `make` is invoked from the repository root.

BIN_DIR := bin
LDFLAGS := -s -w

.PHONY: all build build-cli build-daemon test test-race cover fmt vet tidy clean smoke

all: build

build: build-cli build-daemon

build-cli:
	@mkdir -p $(BIN_DIR)
	go build -ldflags="$(LDFLAGS)" -o $(BIN_DIR)/agentchat ./cmd/agentchat

build-daemon:
	@mkdir -p $(BIN_DIR)
	go build -ldflags="$(LDFLAGS)" -o $(BIN_DIR)/agentchatd ./cmd/agentchatd

test:
	go test ./...

test-race:
	go test -race ./...

cover:
	go test -coverprofile=coverage.txt ./...
	go tool cover -func=coverage.txt | tail -1

fmt:
	go fmt ./...

vet:
	go vet ./...

tidy:
	go mod tidy

smoke: build
	./e2e/m1-smoke.sh

clean:
	rm -rf $(BIN_DIR) coverage.txt coverage.html
