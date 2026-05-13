#!/usr/bin/env bash
# M1 smoke test: build both binaries and verify --help, --version, and the
# `version` subcommand all behave as expected. Run from anywhere.

set -euo pipefail

cd "$(dirname "$0")/.."

echo "==> make build"
make build >/dev/null

echo "==> ./bin/agentchat --help"
./bin/agentchat --help >/dev/null

echo "==> ./bin/agentchat version"
out=$(./bin/agentchat version)
if [[ "$out" != "agentchat dev" ]]; then
    echo "unexpected output from 'agentchat version': $out"
    exit 1
fi

echo "==> ./bin/agentchat --version (cobra builtin)"
./bin/agentchat --version >/dev/null

echo "==> ./bin/agentchatd --help"
./bin/agentchatd --help >/dev/null

echo "==> ./bin/agentchatd version"
out=$(./bin/agentchatd version)
if [[ "$out" != "agentchatd dev" ]]; then
    echo "unexpected output from 'agentchatd version': $out"
    exit 1
fi

echo "==> ./bin/agentchatd --version (cobra builtin)"
./bin/agentchatd --version >/dev/null

echo "OK: M1 smoke test passed"
