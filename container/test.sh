#!/bin/bash
# Exercise the real processes with disposable state, fake credentials and no network.
set -euo pipefail
image=${1:?usage: bash container/test.sh IMAGE}
container="xposter-test-$$-$RANDOM"
root=$(cd "$(dirname "$0")/.." && pwd)
cleanup() { docker rm -f "$container" >/dev/null 2>&1 || true; }
trap 'cleanup' EXIT
# Disposable state and fake credentials for the full serve command.
offline=(--network none --tmpfs '/data:uid=1000,gid=1000,mode=0700'
    -e X_USERNAME=offline-test -e X_AUTH_TOKEN=offline-test)
serve_fresh='echo '\''{"items":{}}'\'' >/data/state.json && exec /app/entrypoint.sh serve "$@"'

# Isolate startup ordering and timeouts from session behavior.
docker run --rm --network none --user 0 \
    -v "$root/container/entrypoint.test.sh:/tmp/entrypoint.test.sh:ro" \
    "$image" bash /tmp/entrypoint.test.sh

start() {
    docker run -d --name "$container" "${offline[@]}" -e GRAPH_CLIENT_SECRET=offline-test \
        -e ELEVENLABS_API_KEY=offline-test \
        "$image" bash -c "$serve_fresh" _ >/dev/null
    for ((attempt=0; attempt<100; attempt++)); do
        if docker exec "$container" node -e '
            Promise.all(["http://127.0.0.1:8080/status", "http://127.0.0.1:8081/health"].map(async url => {
                const r = await fetch(url, {signal: AbortSignal.timeout(500)});
                if (!r.ok) throw Error("not started");
            })).catch(() => process.exit(1));
        ' >/dev/null 2>&1; then return; fi
        if [[ $(docker inspect -f '{{.State.Running}}' "$container") != true ]]; then break; fi
        sleep 0.2
    done
    docker logs "$container"
    echo 'container did not start both services' >&2
    exit 1
}

# The smoke test is not part of the image, so it is mounted read-only.
docker run --rm --network none \
    -v "$root/poster/container-smoke.mjs:/app/poster/container-smoke.mjs:ro" \
    "$image" node container-smoke.mjs

# Exercise the real HTTP server and transport against deterministic X responses.
docker run --rm --network none \
    -v "$root/poster/server.test.mjs:/app/poster/server.test.mjs:ro" \
    -v "$root/poster/http-fixtures.mjs:/app/poster/http-fixtures.mjs:ro" \
    -v "$root/testdata/contract:/app/testdata/contract:ro" \
    "$image" node --test server.test.mjs

# No automatic initialization or masking of CLI exit codes.
if docker run --rm --network none "$image" /app/orchestrator; then
    echo 'missing state should stop the orchestrator' >&2
    exit 1
fi
docker run --rm --network none "$image" /app/orchestrator -help

# The combined healthcheck must require both endpoints, not merely live processes.
docker run --rm --network none -i "$image" node --input-type=module <<'JS'
import assert from 'node:assert/strict';
import http from 'node:http';
import { spawn } from 'node:child_process';
let codes = [200, 200];
const servers = [8080, 8081].map((port, index) => {
    const server = http.createServer((req, res) => res.writeHead(codes[index]).end());
    server.listen(port, '127.0.0.1');
    return server;
});
await Promise.all(servers.map(server => new Promise(resolve => server.on('listening', resolve))));
for (const statuses of [[200, 200], [200, 503], [503, 200]]) {
    codes = statuses;
    const code = await new Promise(resolve => spawn('node', ['/app/healthcheck.mjs']).on('exit', resolve));
    assert.equal(code, statuses.every(status => status === 200) ? 0 : 1);
}
for (const server of servers) server.close();
JS

for target in orchestrator node stop; do
    start
    if docker exec "$container" flock -n /data/poster.lock true; then
        echo 'the poster lock was not held' >&2
        exit 1
    fi
    if docker exec "$container" node /app/healthcheck.mjs; then
        echo 'an offline session must not be healthy' >&2
        exit 1
    fi

    # Finish assertions before fault injection: fast shutdown can kill docker exec
    # itself with 137, which must not mask a failed assertion.
    target_pid=$(docker exec -i "$container" node --input-type=module - "$target" <<'JS'
import assert from 'node:assert/strict';
import fs from 'node:fs';
const target = process.argv[2];
const pids = {};
for (const pid of fs.readdirSync('/proc').filter(name => /^\d+$/.test(name))) {
    let args;
    try { args = fs.readFileSync(`/proc/${pid}/cmdline`, 'utf8').split('\0'); } catch { continue; }
    const role = args[0] === '/app/orchestrator' ? 'orchestrator'
        : args[1] === '/app/poster/server.mjs' ? 'node' : null;
    if (!role) continue;
    assert.match(fs.readFileSync(`/proc/${pid}/status`, 'utf8'), /Uid:\s+1000\s+1000/);
    const env = fs.readFileSync(`/proc/${pid}/environ`, 'utf8').split('\0');
    assert.ok(!env.some(value => value.startsWith(role === 'orchestrator' ? 'X_AUTH_TOKEN=' : 'GRAPH_CLIENT_SECRET=')));
    assert.equal(env.some(value => value.startsWith('ELEVENLABS_API_KEY=')), role === 'node');
    pids[role] = Number(pid);
}
assert.deepEqual(Object.keys(pids).sort(), ['node', 'orchestrator']);
if (target !== 'stop') console.log(pids[target]);
JS
    )
    if [[ $target == stop ]]; then
        docker stop --time 25 "$container" >/dev/null
    else
        injection_status=0
        docker exec "$container" bash -c 'kill -KILL "$1"' _ "$target_pid" || injection_status=$?
        # The container may exit before the kill helper is reaped.
        if [[ $injection_status != 0 && $injection_status != 137 ]]; then exit "$injection_status"; fi
    fi
    code=$(timeout 24 docker wait "$container")
    if [[ $target == stop ]]; then [[ $code == 0 ]]; else [[ $code != 0 ]]; fi
    echo "PASS: $target shutdown (exit $code)"
    cleanup
done

# One-shot mode must propagate the offline feed failure after poster startup.
# Match the cause too: startup failures in entrypoint.sh also exit 1.
status=0
output=$(docker run --rm "${offline[@]}" -e DRY_RUN=true "$image" bash -c "$serve_fresh" _ -once 2>&1) || status=$?
if [[ $status != 1 || $output != *'fetch feed'* ]]; then
    printf '%s\n' "$output" >&2
    echo "one-shot exited $status without the expected feed failure" >&2
    exit 1
fi
echo 'PASS: one-shot shutdown preserves the poll failure'
