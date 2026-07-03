#!/usr/bin/env sh
# loadgen.sh — drive HTTP load at the cpu-autoscale demo service so its per-task
# CPU rises above swarm.autoscaler.target and swarm-hpa scales it out.
#
# The registry.k8s.io/hpa-example app burns CPU per request, so a handful of
# concurrent workers is enough to cross a 50% CPU target. While this runs, watch
# the daemon (make run ARGS="--log-level=debug") for lines like:
#   level=INFO msg="scaling decision" service=demo_web current=2 desired=4 ...
#   level=INFO msg="dry-run: would scale" service=demo_web ...
#
# NOTE: the Docker stats provider is local-node only; on a multi-node swarm run
# the daemon where the replicas are (or use the Prometheus example instead).
set -eu

usage() {
    cat <<'EOF'
Usage: loadgen.sh [TARGET_URL]

Generate concurrent HTTP load for the cpu-autoscale demo.

Configuration (env vars; defaults in brackets):
  TARGET_URL    URL to hammer            [http://localhost:8080/]
  CONCURRENCY   parallel request workers [20]
  DURATION      seconds to run           [120]

Examples:
  loadgen.sh
  CONCURRENCY=50 DURATION=300 loadgen.sh
  loadgen.sh http://localhost:8080/
EOF
}

case "${1:-}" in
    -h|--help) usage; exit 0 ;;
esac

TARGET_URL="${1:-${TARGET_URL:-http://localhost:8080/}}"
CONCURRENCY="${CONCURRENCY:-20}"
DURATION="${DURATION:-120}"

# Pick an HTTP client: curl preferred, wget as fallback. (Word-splitting of
# $FETCH below is intentional — it carries flags.)
if command -v curl >/dev/null 2>&1; then
    FETCH="curl -fsS -o /dev/null --max-time 10"
elif command -v wget >/dev/null 2>&1; then
    FETCH="wget -q -O /dev/null -T 10"
else
    echo "loadgen: error — need curl or wget on PATH" >&2
    exit 1
fi

echo "loadgen: preflight — checking $TARGET_URL is reachable ..."
# shellcheck disable=SC2086
if ! $FETCH "$TARGET_URL"; then
    echo "loadgen: error — $TARGET_URL is not reachable." >&2
    echo "  Deploy the demo first:  docker stack deploy -c examples/cpu-autoscale/stack.yml demo" >&2
    echo "  Or point at another URL: loadgen.sh http://host:port/" >&2
    exit 1
fi

echo "loadgen: starting"
echo "  target      = $TARGET_URL"
echo "  concurrency = $CONCURRENCY"
echo "  duration    = ${DURATION}s"
echo "  client      = ${FETCH%% *}"

work_dir="$(mktemp -d)"
pids=""

count_requests() {
    # Sum the per-worker tally files (one byte appended per request).
    _total=0
    for _f in "$work_dir"/w* ; do
        [ -f "$_f" ] || continue
        _c=$(wc -c < "$_f" 2>/dev/null || echo 0)
        _total=$((_total + _c))
    done
    echo "$_total"
}

cleanup() {
    trap - INT TERM
    echo
    echo "loadgen: stopping workers ..."
    for _pid in $pids; do kill "$_pid" 2>/dev/null || :; done
    wait 2>/dev/null || :
    _total=$(count_requests)
    rm -rf "$work_dir"
    echo "loadgen: done — ~${_total} requests across ${CONCURRENCY} workers (up to ${DURATION}s)"
    echo "loadgen: check the daemon logs for scaling decisions."
}
trap 'cleanup; exit 0' INT TERM

now=$(date +%s)
deadline=$((now + DURATION))

# Spawn workers. Each hammers the URL until the deadline and tallies its requests
# in its own file (no cross-process contention). Request errors are tolerated.
_i=1
while [ "$_i" -le "$CONCURRENCY" ]; do
    (
        wf="$work_dir/w$_i"
        while [ "$(date +%s)" -lt "$deadline" ]; do
            # shellcheck disable=SC2086
            $FETCH "$TARGET_URL" >/dev/null 2>&1 || :
            printf '.' >> "$wf"
        done
    ) &
    pids="$pids $!"
    _i=$((_i + 1))
done

# Progress: report roughly every 10s until the deadline.
while :; do
    remaining=$((deadline - $(date +%s)))
    [ "$remaining" -le 0 ] && break
    if [ "$remaining" -lt 10 ]; then sleep "$remaining"; else sleep 10; fi
    elapsed=$((DURATION - (deadline - $(date +%s))))
    echo "loadgen: ${elapsed}s elapsed, ~$(count_requests) requests so far"
done

cleanup
