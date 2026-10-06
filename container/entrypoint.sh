#!/bin/bash
set -euo pipefail

# One-off maintenance commands do not start the poster or a polling loop.
if [[ ${1:-} != serve ]]; then
    exec "$@"
fi
shift

pids=()
cleanup() {
    trap '' TERM INT
    if ((${#pids[@]})); then
        kill -TERM "${pids[@]}" 2>/dev/null || true
        # Bound shutdown even when one child stops responding.
        (trap - TERM INT; sleep 20; kill -KILL "${pids[@]}" 2>/dev/null || true) &
        local watchdog=$!
        wait "${pids[@]}" 2>/dev/null || true
        kill "$watchdog" 2>/dev/null || true
        wait "$watchdog" 2>/dev/null || true
    fi
}
trap cleanup EXIT
trap 'exit 0' TERM INT

# Hold the poster lock for the entire container lifetime, including shutdown.
exec 9>"$DATA_DIR/poster.lock"
if ! flock -n 9; then
    echo 'another poster holds the poster lock' >&2
    exit 1
fi

# Filter credentials by application; the shared UID still permits cross-process access.
# Prefix filtering also covers future settings.
(
    unset "${!GRAPH_@}" ALERT_RECIPIENTS HEARTBEAT_URL
    exec node /app/poster/server.mjs
) 9>&- &
pids+=("$!")
(
    unset "${!X_@}" ELEVENLABS_API_KEY
    # Bound startup by the healthcheck start period.
    # Authenticated requests queue behind the initial session check.
    attempt=0
    until (: <>/dev/tcp/127.0.0.1/8081) 2>/dev/null; do
        if ((++attempt >= 900)); then
            echo 'poster did not start' >&2
            exit 1
        fi
        sleep 0.1
    done
    exec /app/orchestrator "$@"
) 9>&- &
orchestrator_pid=$!
pids+=("$orchestrator_pid")

# Any child exiting ends the container; Docker owns the restart policy.
status=0
wait -n -p exited "${pids[@]}" || status=$?
if [[ $status == 0 ]] && ! [[ $# -gt 0 && $exited == "$orchestrator_pid" ]]; then
    echo 'a required process stopped unexpectedly' >&2
    status=1
fi
exit "$status"
