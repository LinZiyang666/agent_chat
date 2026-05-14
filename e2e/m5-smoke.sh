#!/usr/bin/env bash
# M5 smoke: verify the state-view API *surface* end-to-end.
#
# Mock-driven: we don't bring an account online (that needs real
# Discord), so the snapshot is empty but the endpoint shape is
# verified. Real-Discord verification of state updates is the
# operator's job — same pattern as M3/M4.

set -euo pipefail

cd "$(dirname "$0")/.."

DATA_ROOT="$(mktemp -d -t agentchat-m5-XXXXXX)"
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

echo "==> GET /v1/state returns empty-ish snapshot"
./bin/agentchat state --json | python3 -c '
import json,sys
s = json.load(sys.stdin)
assert s.get("totals", {}).get("unread") == 0, f"expected unread=0, got {s}"
assert s.get("account_id"), "snapshot must include account_id"
assert isinstance(s.get("version"), int), "version must be int"
'

echo "==> version increments between two state calls"
v1=$(./bin/agentchat state --json | python3 -c 'import json,sys; print(json.load(sys.stdin)["version"])')
v2=$(./bin/agentchat state --json | python3 -c 'import json,sys; print(json.load(sys.stdin)["version"])')
[[ "$v2" -gt "$v1" ]] || { echo "version did not advance: v1=$v1 v2=$v2"; exit 1; }

echo "==> watch state opens, emits initial frame, idles silently"
# Use 'timeout' to bound the watcher; SIGTERM lets the Go runtime
# shut down cleanly. Keep stdout (NDJSON) separate from stderr so a
# CLI error can't masquerade as a frame (M5-P3-007 fix).
LINE_OUT="$DATA_ROOT/watch.out"
LINE_ERR="$DATA_ROOT/watch.err"
timeout --signal=TERM 1.5 ./bin/agentchat watch state >"$LINE_OUT" 2>"$LINE_ERR" || true

# Stderr must be empty — a non-empty stderr means the CLI errored
# before/during the stream, which is what M5-P3-007 flagged could
# previously slip through.
if [[ -s "$LINE_ERR" ]]; then
    echo "watch wrote to stderr (unexpected):"
    cat "$LINE_ERR"
    exit 1
fi

LINES=$(wc -l < "$LINE_OUT" | tr -d ' ')
if [[ "$LINES" -lt 1 ]]; then
    echo "watch must emit at least the initial frame; got $LINES lines:"
    cat "$LINE_OUT"
    exit 1
fi
if [[ "$LINES" -gt 1 ]]; then
    echo "watch on an idle daemon must emit exactly 1 frame in 1.5s; got $LINES:"
    cat "$LINE_OUT"
    exit 1
fi

# Parse the single line as JSON and require the canonical Snapshot
# shape — assert account_id, integer version, and a totals block.
python3 - "$LINE_OUT" <<'PY'
import json, sys
with open(sys.argv[1]) as f:
    line = f.readline().strip()
if not line:
    sys.exit("watch produced an empty first line")
try:
    snap = json.loads(line)
except json.JSONDecodeError as e:
    sys.exit(f"watch first line is not valid JSON ({e}): {line!r}")
if not snap.get("account_id"):
    sys.exit(f"watch frame missing account_id: {snap}")
if not isinstance(snap.get("version"), int):
    sys.exit(f"watch frame version must be int, got {snap.get('version')!r}")
if not isinstance(snap.get("totals"), dict):
    sys.exit(f"watch frame totals must be an object: {snap.get('totals')!r}")
PY

echo "==> new command help text renders"
./bin/agentchat state --help >/dev/null
./bin/agentchat watch --help >/dev/null
./bin/agentchat watch state --help >/dev/null

echo "OK: M5 mock-driven smoke test passed."
echo "    Real Discord verification is the operator's job:"
echo "      1. Online an admin bot; create a room; invite a subscribed user."
echo "      2. Open: agentchat watch state (as the user)."
echo "      3. From admin: agentchat send <room> 'hi' (with --requires-ack, --priority urgent)."
echo "      4. Verify a new NDJSON frame lands on the user's stream within ~200ms,"
echo "         with totals.unread / mentions / pending_acks / priority all updating."
