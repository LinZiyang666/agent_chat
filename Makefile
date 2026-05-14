# Agent Chat — build & dev tasks
# All paths assume `make` is invoked from the repository root.

BIN_DIR := bin
LDFLAGS := -s -w

# Code-bearing packages — those with real implementation as opposed to
# doc.go placeholders. The `cover` target uses this list rather than
# `./...` so:
#   1. project-total coverage is meaningful (not diluted by empty
#      placeholders) — workflow §1.6.
#   2. the target is reproducible across Go toolchains that do not
#      ship the `covdata` tool (fix for M2-P3-009).
COVER_PKGS := \
  ./internal/account \
  ./internal/api \
  ./internal/audit \
  ./internal/auth \
  ./internal/cliutil \
  ./internal/config \
  ./internal/connector \
  ./internal/crypto \
  ./internal/errcode \
  ./internal/message \
  ./internal/state \
  ./internal/store/sqlite \
  ./pkg/client \
  ./cmd/agentchat/cmds \
  ./cmd/agentchatd/cmds

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
	go test -race -timeout=20m ./...

cover:
	go test -coverprofile=coverage.txt $(COVER_PKGS)
	go tool cover -func=coverage.txt | tail -1

fmt:
	go fmt ./...

vet:
	go vet ./...

tidy:
	go mod tidy

smoke: build
	./e2e/m1-smoke.sh
	./e2e/m2-smoke.sh
	./e2e/m3-smoke.sh
	./e2e/m4-smoke.sh
	./e2e/m5-smoke.sh

clean:
	rm -rf $(BIN_DIR) coverage.txt coverage.html
