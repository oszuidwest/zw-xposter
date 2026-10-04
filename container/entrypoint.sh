#!/bin/bash
set -euo pipefail

# One-off maintenance commands do not start a browser or a polling loop.
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
trap 'cleanup' EXIT
trap 'exit 0' TERM INT

# Hold the profile lock for the entire container lifetime, including shutdown.
exec 9>"$DATA_DIR/poster.lock"
if ! flock -n 9; then
    echo 'another poster holds the profile lock' >&2
    exit 1
fi
rm -f "$DATA_DIR/profile/SingletonLock"

if [[ ${HEADLESS:-false} != true ]]; then
    rm -f /tmp/.X99-lock /tmp/.X11-unix/X99
    env -i "PATH=$PATH" Xvfb :99 -screen 0 "${SCREEN_SIZE:-1920x1080}x24" -nolisten tcp 9>&- &
    pids+=("$!")
    for ((attempt=0; attempt<50; attempt++)); do
        if [[ -S /tmp/.X11-unix/X99 ]]; then break; fi
        if ! kill -0 "${pids[0]}" 2>/dev/null; then break; fi
        sleep 0.1
    done
    if [[ ! -S /tmp/.X11-unix/X99 ]]; then
        echo 'Xvfb did not start' >&2
        exit 1
    fi
    export DISPLAY=:99
fi

# Filter credentials by application; the shared UID still permits cross-process access.
# Prefix filtering also covers future settings.
(
    unset "${!GRAPH_@}" ALERT_RECIPIENTS HEARTBEAT_URL
    exec node /app/poster/server.mjs
) 9>&- &
pids+=("$!")
(
    unset "${!X_@}"
    # Polls wait for the poster port; browser startup precedes listen,
    # and requests queue behind the initial session check.
    attempt=0
    until (: <>/dev/tcp/127.0.0.1/8081) 2>/dev/null; do
        if ((++attempt >= 100)); then
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
