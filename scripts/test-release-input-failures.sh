#!/bin/sh
# File scans must fail the packaging check, not silently shrink its inventory.
set -eu
cd "$(dirname "$0")/.."

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT HUP INT TERM
RELEASE_TEST_REAL_FIND=$(command -v find)
export RELEASE_TEST_REAL_FIND
cat > "$work/find" <<'SH'
#!/bin/sh
if [ "$1" = "$RELEASE_TEST_FIND_PATH" ]; then
    printf 'Injected inventory failure\n' >&2
    exit 42
fi
exec "$RELEASE_TEST_REAL_FIND" "$@"
SH
chmod +x "$work/find"

for scan in cmd/kallisti-rss licenses .; do
    status=0
    RELEASE_TEST_FIND_PATH="$scan" PATH="$work:$PATH" \
        sh scripts/check-release-inputs.sh > "$work/output" 2>&1 || status=$?
    if [ "$status" -ne 42 ]; then
        printf 'Inventory failure for %s returned %s; expected 42.\n' "$scan" "$status" >&2
        exit 1
    fi
done
printf 'Source, license and exported-context scan failures stop the packaging check.\n'
