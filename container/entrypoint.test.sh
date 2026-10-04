#!/bin/bash
# Run only in a disposable container: replace the applications with startup probes.
set -euo pipefail
export HEADLESS=true
mkdir -p /tmp/bin
export PATH="/tmp/bin:$PATH"

cat >/tmp/bin/node <<'SH'
#!/bin/bash
if [[ ${POSTER_STARTUP:-} == never ]]; then exec sleep infinity; fi
exec /usr/local/bin/node -e 'setTimeout(() => require("node:net").createServer(socket => socket.end()).listen(8081, "127.0.0.1"), 1000 * (process.env.POSTER_DELAY || 1))'
SH
cat >/app/orchestrator <<'SH'
#!/bin/bash
if ! (: <>/dev/tcp/127.0.0.1/8081) 2>/dev/null; then
    echo 'orchestrator started before the poster listened' >&2
    exit 24
fi
exit 23
SH
chmod +x /tmp/bin/node /app/orchestrator

run() {
    local expected=$1 status=0
    shift
    # Fresh state per run: the shutdown watchdog's sleep can still hold the profile lock.
    DATA_DIR=$(mktemp -d) timeout 120 /app/entrypoint.sh "$@" >/tmp/startup.log 2>&1 || status=$?
    if [[ $status != "$expected" ]]; then
        cat /tmp/startup.log
        echo "expected exit $expected, got $status" >&2
        exit 1
    fi
}

run 23 serve
run 23 serve -once
echo 'PASS: both serve modes wait for the poster port and preserve the exit code'

POSTER_DELAY=15 run 23 serve
echo 'PASS: a slow browser launch does not stop the container'

POSTER_STARTUP=never run 1 serve
grep -q 'poster did not start' /tmp/startup.log
echo 'PASS: the poster startup wait is bounded'
