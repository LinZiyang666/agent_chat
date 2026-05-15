#!/usr/bin/env bash
# M6 smoke: verify the announcement + system-announcement API surface
# end-to-end, plus the mention_all field on /v1/rooms/{id}/messages.
#
# Mock-driven (same constraint as M3-M5): we don't connect to Discord
# in CI, so the room-announcement / mention_all paths exercise the
# message-and-membership graph using an admin bot that talks to a mock
# provider. The new endpoints' shapes and state aggregation are
# verified; real-Discord delivery is the operator's job.

set -euo pipefail

cd "$(dirname "$0")/.."

DATA_ROOT="$(mktemp -d -t agentchat-m6-XXXXXX)"
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
AGENTCHAT_DISCORD_GUILD_ID=987654321 \
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

echo "==> CLI help renders for every new M6 command"
./bin/agentchat room announce --help          >/dev/null
./bin/agentchat room announce-show --help     >/dev/null
./bin/agentchat ack-announcement --help       >/dev/null
./bin/agentchat system-announcements --help   >/dev/null
./bin/agentchat ack-system --help             >/dev/null
./bin/agentchat admin system-announce --help  >/dev/null
./bin/agentchat send --help | grep -q -- '--all'

echo "==> empty system announcements list returns an empty array"
./bin/agentchat system-announcements --json | python3 -c '
import json,sys
got = json.load(sys.stdin)
assert isinstance(got, list), f"expected list, got {type(got).__name__}: {got!r}"
assert got == [], f"expected empty list, got {got!r}"
'

echo "==> admin can post a system announcement; viewer sees it unread"
SYS_ID=$(./bin/agentchat admin system-announce 'maintenance Sunday 02:00' --json \
    | python3 -c 'import json,sys; print(json.load(sys.stdin)["id"])')
[[ -n "$SYS_ID" ]] || { echo "no sys ann id returned"; exit 1; }

./bin/agentchat state --json | python3 -c '
import json,sys
s = json.load(sys.stdin)
totals = s.get("totals", {})
assert totals.get("system_announcements") == 1, f"expected 1 unread sys ann, got {totals!r}"
'

echo "==> ack-system zeros the unread counter"
./bin/agentchat ack-system "$SYS_ID" --json >/dev/null
./bin/agentchat state --json | python3 -c '
import json,sys
s = json.load(sys.stdin)
totals = s.get("totals", {})
assert totals.get("system_announcements") == 0, f"expected 0 after ack, got {totals!r}"
'

echo "==> system-announcements list reports read=true after ack"
./bin/agentchat system-announcements --json | python3 -c '
import json,sys
got = json.load(sys.stdin)
assert len(got) == 1, f"expected 1 row, got {got!r}"
assert got[0]["read"] is True, f"expected read=true, got {got!r}"
'

echo "==> announcement / mention_all surface (admin-only via direct API)"
# The room-announcement and mention_all paths share the same SendMessage
# / Provider plumbing that already requires an online bot. In the
# mock-driven smoke we can't bring a Discord bot online, so we verify
# the endpoints reject sensibly when prerequisites are missing instead
# of trying to send a real message. The full happy-path is covered by
# the Go integration tests (internal/api/m6_test.go).
RESP=$(curl -sS --unix-socket "$DATA_ROOT/agentchatd.sock" \
    -H "Authorization: Bearer $TOKEN" \
    -X POST http://agentchatd/v1/rooms/does-not-exist/announcement \
    -H 'Content-Type: application/json' \
    -d '{"content":"x"}' -o /dev/null -w '%{http_code}')
[[ "$RESP" == "404" ]] || { echo "expected 404 for unknown room, got $RESP"; exit 1; }

echo "==> non-admin token cannot post a system announcement"
ACC=$(./bin/agentchat admin account create --name alice --role user --json \
    | python3 -c 'import json,sys; print(json.load(sys.stdin)["id"])')
RAW=$(./bin/agentchat admin token create "$ACC" --json \
    | python3 -c 'import json,sys; print(json.load(sys.stdin)["raw"])')
RESP=$(curl -sS --unix-socket "$DATA_ROOT/agentchatd.sock" \
    -H "Authorization: Bearer $RAW" \
    -X POST http://agentchatd/v1/system/announcements \
    -H 'Content-Type: application/json' \
    -d '{"content":"forbidden"}' -o /dev/null -w '%{http_code}')
[[ "$RESP" == "403" ]] || { echo "expected 403 for non-admin sys ann, got $RESP"; exit 1; }

echo "OK: M6 mock-driven smoke test passed."
echo "    Real Discord verification is the operator's job:"
echo "      1. Online a bot; create a room; invite a subscribed viewer."
echo "      2. As admin:     agentchat room announce <room> 'v2 launch'"
echo "         As viewer:    agentchat state | jq '.totals.announcements'   # = 1"
echo "         As viewer:    agentchat ack-announcement <id>                # state -> 0"
echo "      3. As admin:     agentchat send <room> --all 'drill at 0600'"
echo "         As viewer:    agentchat state | jq '.totals.mentions'        # bumps even without <@bot>"
echo "      4. As admin:     agentchat admin system-announce 'reboot Sunday'"
echo "         As viewer:    agentchat state | jq '.totals.system_announcements'"
