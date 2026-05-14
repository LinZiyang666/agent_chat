#!/usr/bin/env bash
# M2 end-to-end smoke test.
#
# Builds both binaries, starts agentchatd in a temp dir, reads the
# bootstrap admin token from its stdout, then exercises the full CLI
# surface (whoami, admin account *, admin token *, admin audit list)
# against the real daemon over a Unix socket.
#
# On any failure (set -e) the daemon is killed and a non-zero exit
# propagates. Designed to be re-runnable.

set -euo pipefail

cd "$(dirname "$0")/.."

DATA_ROOT="$(mktemp -d -t agentchat-m2-XXXXXX)"
trap 'cleanup' EXIT

DAEMON_PID=""
DAEMON_OUT="$DATA_ROOT/daemon.out"
DAEMON_ERR="$DATA_ROOT/daemon.err"

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
./bin/agentchatd serve --data-root "$DATA_ROOT" >"$DAEMON_OUT" 2>"$DAEMON_ERR" &
DAEMON_PID=$!

# Wait until the socket appears, up to ~3s.
for _ in 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15; do
    if [[ -S "$DATA_ROOT/agentchatd.sock" ]]; then break; fi
    sleep 0.2
done
if [[ ! -S "$DATA_ROOT/agentchatd.sock" ]]; then
    echo "daemon did not create socket"
    echo "--- stderr ---"; cat "$DAEMON_ERR"
    echo "--- stdout ---"; cat "$DAEMON_OUT"
    exit 1
fi

# The bootstrap banner is written on stdout: "AGENTCHAT_TOKEN=<raw>"
TOKEN="$(grep -oE 'AGENTCHAT_TOKEN=\S+' "$DAEMON_OUT" | head -n1 | cut -d= -f2)"
if [[ -z "$TOKEN" ]]; then
    echo "could not extract token from daemon stdout"
    cat "$DAEMON_OUT"
    exit 1
fi
export AGENTCHAT_TOKEN="$TOKEN"
export AGENTCHAT_HOME="$DATA_ROOT"

echo "==> agentchat whoami"
out="$(./bin/agentchat whoami --json)"
echo "$out" | grep -q '"name": *"root"' || { echo "whoami output unexpected: $out"; exit 1; }

echo "==> agentchat admin account create alice"
./bin/agentchat admin account create --name alice --role user --json >/dev/null

echo "==> agentchat admin account list contains alice"
./bin/agentchat admin account list --json | grep -q 'alice'

echo "==> agentchat admin account set-role alice -> admin"
ALICE_ID="$(./bin/agentchat admin account list --json | sed -n 's/.*"id": *"\([^"]*\)".*"name": *"alice".*/\1/p' | head -n1)"
# fallback: a more robust line-by-line extraction if the regex above misses
if [[ -z "$ALICE_ID" ]]; then
    ALICE_ID="$(./bin/agentchat admin account list --json | python3 -c 'import json,sys; [print(a["id"]) for a in json.load(sys.stdin) if a["name"]=="alice"]')"
fi
./bin/agentchat admin account set-role "$ALICE_ID" admin --json >/dev/null

echo "==> agentchat admin token create for alice"
TOKEN_OUT="$(./bin/agentchat admin token create "$ALICE_ID" --json)"
RAW_TOKEN="$(printf '%s' "$TOKEN_OUT" | python3 -c 'import json,sys; print(json.load(sys.stdin)["raw"])')"
TOKEN_ID="$(printf '%s' "$TOKEN_OUT" | python3 -c 'import json,sys; print(json.load(sys.stdin)["token"]["id"])')"
[[ -n "$RAW_TOKEN" ]] || { echo "no raw token returned"; exit 1; }

echo "==> alice can whoami with her own token"
AGENTCHAT_TOKEN="$RAW_TOKEN" ./bin/agentchat whoami --json | grep -q '"name": *"alice"'

echo "==> revoke alice's token"
./bin/agentchat admin token revoke "$TOKEN_ID" --json >/dev/null

echo "==> revoked token now fails with AUTH_REVOKED"
# pipefail is on, so we must capture the (non-zero) exit explicitly
# before grepping, otherwise the pipeline status reflects the failed
# whoami rather than the success of grep.
revoked_output="$(AGENTCHAT_TOKEN="$RAW_TOKEN" ./bin/agentchat whoami 2>&1 || true)"
if ! grep -q 'AUTH_REVOKED' <<<"$revoked_output"; then
    echo "expected AUTH_REVOKED on revoked token, got:"
    echo "$revoked_output"
    exit 1
fi

echo "==> audit log contains account.create"
./bin/agentchat admin audit list --json | grep -q 'account.create'

echo "==> agentchat admin account delete alice"
./bin/agentchat admin account delete "$ALICE_ID" --json >/dev/null

echo "==> healthz still up"
# bypass auth: hit healthz directly through curl over the unix socket
curl -s --unix-socket "$DATA_ROOT/agentchatd.sock" http://daemon/v1/healthz | grep -q '"status":"ok"'

echo "==> second daemon on same data root is refused (B1)"
# Capture stderr; pipefail is on so we wrap with || true.
second_err="$(./bin/agentchatd serve --data-root "$DATA_ROOT" 2>&1 || true)"
if ! grep -q 'CONFLICT' <<<"$second_err"; then
    echo "expected CONFLICT from second daemon, got:"
    echo "$second_err"
    exit 1
fi

echo "==> --name is required (admin account create) (M6)"
missing_name_err="$(./bin/agentchat admin account create --role user 2>&1 || true)"
if ! grep -qi 'required flag' <<<"$missing_name_err"; then
    echo "expected required-flag error, got:"
    echo "$missing_name_err"
    exit 1
fi

echo "==> cli.toml token source works (M7)"
mkdir -p "$DATA_ROOT/cli-tok-test"
RAW_ROOT_TOKEN="$AGENTCHAT_TOKEN"
unset AGENTCHAT_TOKEN
printf 'token = "%s"\n' "$RAW_ROOT_TOKEN" > "$DATA_ROOT/cli.toml"
./bin/agentchat whoami --json | grep -q '"name": *"root"' || {
    echo "expected cli.toml to provide token"
    exit 1
}
rm -f "$DATA_ROOT/cli.toml"
export AGENTCHAT_TOKEN="$RAW_ROOT_TOKEN"

echo "OK: M2 smoke test passed"
