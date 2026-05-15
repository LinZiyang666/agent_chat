#!/usr/bin/env bash
# M7 smoke: verify the attachment surface end-to-end without
# requiring a real Discord bot.
#
# Mock-driven (matches M3-M6 convention). The full happy path
# (real Discord upload + download) is operator-verified; this
# smoke checks the CLI surface + API validation only.

set -euo pipefail

cd "$(dirname "$0")/.."

DATA_ROOT="$(mktemp -d -t agentchat-m7-XXXXXX)"
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

echo "==> attachments directory exists after boot"
[[ -d "$DATA_ROOT/attachments" ]] || {
    echo "expected $DATA_ROOT/attachments to be created by downloader init"
    exit 1
}

echo "==> CLI help mentions --attach"
./bin/agentchat send --help | grep -q -- '--attach' \
    || { echo "send --help missing --attach"; exit 1; }

echo "==> unknown room id returns NOT_FOUND (authz runs before path stat — M7-P3-001 fix)"
# M7-P3-001 fix: attachment stat / size guard moved AFTER room
# authorization. A nonexistent room must therefore fail with
# NOT_FOUND, NOT with ATTACHMENT_TOO_LARGE or INVALID_ARGUMENT.
# The path argument here is deliberately something the smoke does
# not create — if authz ever regresses back to "stat first", this
# request would leak its non-existence (400) or trigger a 413 with
# a synthetic huge file. NOT_FOUND is the only safe answer.
RESP_BODY="$DATA_ROOT/resp.json"
HTTP_CODE=$(curl -sS --unix-socket "$DATA_ROOT/agentchatd.sock" \
    -H "Authorization: Bearer $TOKEN" \
    -H 'Content-Type: application/json' \
    -X POST "http://agentchatd/v1/rooms/no-such-room/messages" \
    -d '{"content":"hi","attachments":[{"path":"/does/not/exist.bin"}]}' \
    -o "$RESP_BODY" -w '%{http_code}')
if [[ "$HTTP_CODE" != "404" ]]; then
    echo "expected HTTP 404 for unknown room (authz first), got $HTTP_CODE"
    cat "$RESP_BODY"
    exit 1
fi
python3 - "$RESP_BODY" <<'PY'
import json, sys
with open(sys.argv[1]) as f:
    body = json.load(f)
err = body.get("error", {})
assert err.get("code") == "NOT_FOUND", f"expected NOT_FOUND, got {err}"
PY

echo "==> Discord 10MB per-file guard + per-file stat verified by Go test suite (M7 + M8-S-P2-006)"
# The actual ATTACHMENT_TOO_LARGE / per-file branches require an
# authorized room which in turn requires bringing a bot online.
# That isn't available in the mock smoke. Coverage:
#   - internal/api/m7_test.go::TestSendMessageAttachmentTooLargeReturns413
#   - internal/api/m7_test.go::TestSendMessageSingleOversizeAttachmentFails
#   - internal/api/m7_test.go::TestSendMessageMixedOneOversizeAttachmentFails
#   - internal/api/m7_phase3_audit_test.go::
#         TestPhase3AttachmentAuthzRunsBeforeSizePreflight
#         TestPhase3AttachmentLimitIsPerFileNotAggregate
# These run as part of `go test ./internal/api`.

echo "OK: M7 mock-driven smoke test passed."
echo "    Real Discord verification is the operator's job:"
echo "      1. Online a bot; create a room; invite a subscribed viewer."
echo "      2. agentchat send <room> --attach /tmp/screenshot.png 'look'"
echo "         → bot uploads the file; Discord client shows the image inline"
echo "      3. As the viewer, after the gateway echo arrives:"
echo "         agentchat history <room>"
echo "         → row has [ATTACHMENT] entry; local_path points under <data-root>/attachments/"
echo "      4. xdg-open <local_path> opens the cached copy."
