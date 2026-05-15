# Agent Chat — build & dev tasks
# All paths assume `make` is invoked from the repository root.

BIN_DIR := bin

# Version stamped into both binaries via -ldflags -X (M8-B-P1-001). A
# tagged release shows the tag, a dirty checkout shows `<sha>-dirty`,
# and a clean non-tagged checkout shows the short sha. Falls back to
# the literal "dev" if `git describe` fails (e.g. the source has been
# shipped without a .git directory).
VERSION := $(shell git describe --tags --dirty --always 2>/dev/null || echo dev)
LDFLAGS := -s -w \
  -X github.com/LinZiyang666/agentchat/cmd/agentchat/cmds.Version=$(VERSION) \
  -X github.com/LinZiyang666/agentchat/cmd/agentchatd/cmds.Version=$(VERSION)

# `-trimpath` strips developer $HOME paths from binary debug info (M8-
# B-P1-002) and makes builds reproducible across machines.
GOFLAGS := -trimpath

# Code-bearing packages — those with real implementation as opposed to
# doc.go placeholders. The `cover` target uses this list rather than
# `./...` so:
#   1. project-total coverage is meaningful (not diluted by empty
#      placeholders) — workflow §1.6.
#   2. the target is reproducible across Go toolchains that do not
#      ship the `covdata` tool (fix for M2-P3-009).
#
# M8-B-P1-004: added `internal/attachment` (M7's downloader has real
# tests and was previously missing from cover totals).
COVER_PKGS := \
  ./internal/account \
  ./internal/api \
  ./internal/attachment \
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
	go build $(GOFLAGS) -ldflags="$(LDFLAGS)" -o $(BIN_DIR)/agentchat ./cmd/agentchat

build-daemon:
	@mkdir -p $(BIN_DIR)
	go build $(GOFLAGS) -ldflags="$(LDFLAGS)" -o $(BIN_DIR)/agentchatd ./cmd/agentchatd

test:
	go test ./...

# M8-B-P1-003: race timeout was 20m which is borderline on shared CI
# given internal/api alone took ~17 min pre-M8. M8-Q-P1-008 (bcrypt
# cost var) drops that to seconds, so 20m is now generous; we keep
# 45m so a future hot test or slow VM still survives.
test-race:
	go test -race -timeout=45m ./...

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
	./e2e/m6-smoke.sh
	./e2e/m7-smoke.sh

clean:
	rm -rf $(BIN_DIR) coverage.txt coverage.html
