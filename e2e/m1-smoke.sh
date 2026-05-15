#!/usr/bin/env bash
# M1 smoke test: build both binaries and verify --help, --version, and the
# `version` subcommand all behave as expected. Run from anywhere.
#
# M8-B-P1-001: the version string is now stamped from `git describe`
# rather than the literal "dev". The smoke assertion verifies the
# binary name prefix (`agentchat ` / `agentchatd `) and a non-empty
# tail — it accepts "dev" (no .git), a sha, a tag, or `<sha>-dirty`.

set -euo pipefail
trap 'echo "m1-smoke: aborted" >&2' INT TERM HUP

cd "$(dirname "$0")/.."

echo "==> make build"
make build >/dev/null

echo "==> ./bin/agentchat --help"
./bin/agentchat --help >/dev/null

echo "==> ./bin/agentchat version"
out=$(./bin/agentchat version)
if [[ "$out" != agentchat\ ?* ]]; then
    echo "unexpected output from 'agentchat version': $out"
    exit 1
fi

echo "==> ./bin/agentchat --version (cobra builtin)"
./bin/agentchat --version >/dev/null

echo "==> ./bin/agentchatd --help"
./bin/agentchatd --help >/dev/null

echo "==> ./bin/agentchatd version"
out=$(./bin/agentchatd version)
if [[ "$out" != agentchatd\ ?* ]]; then
    echo "unexpected output from 'agentchatd version': $out"
    exit 1
fi

echo "==> ./bin/agentchatd --version (cobra builtin)"
./bin/agentchatd --version >/dev/null

echo "OK: M1 smoke test passed"
