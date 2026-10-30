#!/bin/sh
# Exercise deploy/docker/compose.yaml with a prebuilt image, synthetic config and
# networking disabled. Never contacts a Kallisti server, feed or registry.
set -eu
cd "$(dirname "$0")/.."
image=${KALLISTI_RSS_IMAGE:-kallisti-rss:local}
docker image inspect "$image" >/dev/null 2>&1 || {
    printf 'image %s not found; build it first (make docker)\n' "$image" >&2
    exit 1
}

mkdir -p .verification
work=$(mktemp -d "$PWD/.verification/compose.XXXXXX")
project="kallisti-rss-smoke-$$"
compose() {
    KALLISTI_RSS_IMAGE="$image" docker compose --progress quiet --env-file /dev/null -p "$project" \
        --project-directory "$work" -f deploy/docker/compose.yaml -f "$work/override.yaml" "$@"
}
cleanup() {
    compose down --remove-orphans >/dev/null 2>&1 || true
    rm -rf "$work"
}
trap cleanup EXIT HUP INT TERM
fail() {
    printf 'compose runtime check failed: %s\n' "$1" >&2
    compose logs --no-color feedbot >&2 2>/dev/null || true
    exit 1
}

# Synthetic, non-secret inputs. World-readable so UID 65532 can read them
# without host chown; production files should be owned by 65532 and mode 600.
cp deploy/docker/config.example.yaml "$work/config.yaml"
mkdir -p "$work/secrets" "$work/data"
printf 'synthetic-not-a-password\n' > "$work/secrets/password"
chmod 644 "$work/config.yaml" "$work/secrets/password"
chmod 777 "$work/data"
cat > "$work/override.yaml" <<'EOF'
services:
  feedbot:
    pull_policy: never
    network_mode: none
EOF

out=$(compose run --rm --no-deps -T feedbot -check-config 2>&1) || fail "-check-config: $out"
[ "$out" = "configuration valid" ] || fail "-check-config output: $out"

out=$(compose run --rm --no-deps -T feedbot -status 2>&1) || fail "-status: $out"
[ "$out" = "pending=0 blocked=0 delivered=0" ] || fail "-status output: $out"
[ -f "$work/data/feedbot.db" ] || fail "database not created under bind-mounted /data"
[ "$(stat -c %u "$work/data/feedbot.db")" = 65532 ] || fail "database not owned by UID 65532"

compose run --rm --no-deps -T feedbot -subscriptions > "$work/subs.json" 2>&1 ||
    fail "-subscriptions: $(cat "$work/subs.json")"

compose up -d --no-build feedbot >/dev/null 2>&1 || fail "service did not start"
i=0
until compose logs --no-color feedbot 2>/dev/null | grep -q 'connection unavailable'; do
    i=$((i + 1))
    [ "$i" -le 30 ] || fail "service never reached the reconnect wait"
    sleep 1
done

# The running service holds the exclusive lock; offline commands must refuse.
if out=$(compose run --rm --no-deps -T feedbot -status 2>&1); then
    fail "-status succeeded while service held the database lock"
fi
case "$out" in *"database already in use"*) ;; *) fail "unexpected lock error: $out" ;; esac

start=$(date +%s)
compose stop -t 30 feedbot >/dev/null 2>&1 || fail "stop failed"
elapsed=$(($(date +%s) - start))
cid=$(compose ps -a -q feedbot)
code=$(docker inspect -f '{{.State.ExitCode}}' "$cid")
[ "$code" = 0 ] || fail "service exited $code after SIGTERM"
[ "$elapsed" -lt 25 ] || fail "SIGTERM shutdown took ${elapsed}s"

printf 'Compose runtime check passed: config path, offline actions, lock and SIGTERM shutdown.\n'
