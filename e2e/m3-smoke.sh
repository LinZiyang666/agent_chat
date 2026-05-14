#!/usr/bin/env bash
# M3 smoke: verify the Discord-adapter API *surface* end-to-end.
#
# The actual Discord handshake is NOT exercised here because that
# requires a real bot token and outbound network access; operators
# verify it manually before this milestone is closed.
#
# What this script checks:
#   - set-discord persists an encrypted bot token (M3-P1)
#   - status reports has_bot_token=true after set-discord
#   - status reports provider_status=offline when not online
#   - offline when never online returns CONFLICT
#   - debug send when not online returns CONFLICT
#   - debug events when not online returns CONFLICT
#   - --help works for every new subcommand

set -euo pipefail

cd "$(dirname "$0")/.."

DATA_ROOT="$(mktemp -d -t agentchat-m3-XXXXXX)"
trap cleanup EXIT

DAEMON_PID=""
DAEMON_OUT="$DATA_ROOT/daemon.out"

cleanup() {
    if [[ -n "$DAEMON_PID" ]] && kill -0 "$DAEMON_PID" 2>/dev/null; then
        kill -TERM "$DAEMON_PID" 2>/dev/null || true
        wait "$DAEMON_PID" 2>/dev/null || true
    fi
    rm -rf "$DATA_ROOT"
}

echo "==> make build"
make build >/dev/null

echo "==> starting agentchatd in $DATA_ROOT"
./bin/agentchatd serve --data-root "$DATA_ROOT" >"$DAEMON_OUT" 2>&1 &
DAEMON_PID=$!

for _ in 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15; do
    [[ -S "$DATA_ROOT/agentchatd.sock" ]] && break
    sleep 0.2
done
if [[ ! -S "$DATA_ROOT/agentchatd.sock" ]]; then
    cat "$DAEMON_OUT"
    exit 1
fi

TOKEN="$(grep -oE 'AGENTCHAT_TOKEN=\S+' "$DAEMON_OUT" | head -n1 | cut -d= -f2)"
[[ -n "$TOKEN" ]] || { echo "no token in daemon output"; cat "$DAEMON_OUT"; exit 1; }
export AGENTCHAT_TOKEN="$TOKEN"
export AGENTCHAT_HOME="$DATA_ROOT"

echo "==> create account agent1"
ACC="$(./bin/agentchat admin account create --name agent1 --role user --json \
       | python3 -c 'import json,sys; print(json.load(sys.stdin)["id"])')"
[[ -n "$ACC" ]] || { echo "no account id returned"; exit 1; }

echo "==> set-discord persists the (encrypted) bot token"
./bin/agentchat admin account set-discord "$ACC" --bot-token "fake-test-token" --json >/dev/null

echo "==> status: has_bot_token=true after set-discord"
./bin/agentchat admin account status "$ACC" --json | grep -q '"has_bot_token": *true'

echo "==> status: provider_status=offline before online"
./bin/agentchat admin account status "$ACC" --json | grep -q '"provider_status": *"offline"'

echo "==> offline when not online -> CONFLICT"
out="$(./bin/agentchat admin account offline "$ACC" 2>&1 || true)"
grep -q 'CONFLICT' <<<"$out" || { echo "expected CONFLICT, got:"; echo "$out"; exit 1; }

echo "==> debug send when not online -> CONFLICT"
out="$(./bin/agentchat debug send --account "$ACC" --channel "0" --text "x" 2>&1 || true)"
grep -q 'CONFLICT' <<<"$out" || { echo "expected CONFLICT, got:"; echo "$out"; exit 1; }

echo "==> debug events when not online -> CONFLICT"
out="$(./bin/agentchat debug events --account "$ACC" 2>&1 || true)"
grep -q 'CONFLICT' <<<"$out" || { echo "expected CONFLICT, got:"; echo "$out"; exit 1; }

echo "==> new command help text renders"
./bin/agentchat admin account set-discord --help >/dev/null
./bin/agentchat admin account online --help >/dev/null
./bin/agentchat admin account offline --help >/dev/null
./bin/agentchat admin account status --help >/dev/null
./bin/agentchat debug --help >/dev/null
./bin/agentchat debug send --help >/dev/null
./bin/agentchat debug events --help >/dev/null

echo "OK: M3 mock-driven smoke test passed."
echo "    Real Discord verification is the operator's job:"
echo "      1. Create a Discord application + bot in the Developer Portal."
echo "      2. Enable privileged intents (MessageContent, GuildMembers)."
echo "      3. Add the bot to your guild via the OAuth invite URL."
echo "      4. Re-run: set-discord with the real token; online; debug send; debug events."
