#!/bin/bash
# Run only in a disposable container: replace the applications with startup probes.
set -euo pipefail
export HEADLESS=true
mkdir -p /tmp/bin
export PATH="/tmp/bin:$PATH"

cat >/tmp/bin/node <<'SH'
#!/bin/bash
echo 'poster started'
if [[ ${POSTER_STARTUP:-} == never ]]; then exec sleep 30; fi
exec /usr/local/bin/node -e 'setTimeout(() => require("node:net").createServer(socket => socket.end()).listen(8081, "127.0.0.1"), 1000)'
SH
cat >/app/orchestrator <<'SH'
#!/bin/bash
echo 'orchestrator started'
if [[ ${1:-} != -help ]]; then
    if ! (: <>/dev/tcp/127.0.0.1/8081) 2>/dev/null; then
        echo 'orchestrator started before the poster listened' >&2
        exit 24
    fi
fi
exit 23
SH
chmod +x /tmp/bin/node /app/orchestrator

run() {
    local expected=$1 status=0
    shift
    DATA_DIR=$(mktemp -d) timeout 20 /app/entrypoint.sh "$@" >/tmp/startup.log 2>&1 || status=$?
    if [[ $status != "$expected" ]]; then
        cat /tmp/startup.log
        echo "expected exit $expected, got $status" >&2
        exit 1
    fi
}

for mode in continuous once; do
    args=(serve)
    if [[ $mode == once ]]; then args+=(-once); fi
    run 23 "${args[@]}"
    grep -q 'orchestrator started' /tmp/startup.log
    echo "PASS: $mode waits for the poster port and preserves the exit code"

    POSTER_STARTUP=never run 1 "${args[@]}"
    grep -q 'poster did not start' /tmp/startup.log
    if grep -q 'orchestrator started' /tmp/startup.log; then
        echo 'orchestrator started without a listening poster' >&2
        exit 1
    fi
    echo "PASS: $mode bounds the poster startup wait"
done

run 23 /app/orchestrator -help
if grep -q 'poster started' /tmp/startup.log; then
    echo 'maintenance command started the poster' >&2
    exit 1
fi
echo 'PASS: maintenance bypasses poster startup'
