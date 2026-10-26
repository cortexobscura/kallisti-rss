#!/bin/sh
# Check packaging rules with synthetic files, never operational contents.
set -eu
cd "$(dirname "$0")/.."

test -f .gitignore
test -f .dockerignore
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT HUP INT TERM

mkdir -p "$work/git" "$work/context"
cp .gitignore "$work/git/.gitignore"
git -C "$work/git" init -q
for path in config.yaml config.container.yaml secrets/password data/feedbot.db \
    data-backup-example/feedbot.db bin/kallisti-feedbot coverage.out \
    .env .env.local backup.sqlite backup.db-wal unresolved.json subscriptions.json \
    runtime.log .verification/output; do
    if ! git -C "$work/git" -c core.excludesFile=/dev/null check-ignore -q --no-index "$path"; then
        printf 'Git does not ignore %s\n' "$path" >&2
        exit 1
    fi
done
for path in config.example.yaml go.mod go.sum cmd/kallisti-rss/main.go \
    internal/bot/store.go LICENSE licenses/example_LICENSE \
    .github/workflows/ci.yml .gitignore .dockerignore scripts/check-release-inputs.sh; do
    if git -C "$work/git" -c core.excludesFile=/dev/null check-ignore -q --no-index "$path"; then
        printf 'Git hides required source: %s\n' "$path" >&2
        exit 1
    fi
done

cp .dockerignore "$work/context/.dockerignore"
# Exercise every currently required build input, plus harmless decoy secrets.
printf '%s\n' go.mod go.sum LICENSE THIRD_PARTY.md > "$work/expected.unsorted"
find cmd/kallisti-rss internal/bot -type f -name '*.go' >> "$work/expected.unsorted"
find licenses -type f ! -name README.md >> "$work/expected.unsorted"
LC_ALL=C sort "$work/expected.unsorted" > "$work/expected"
while IFS= read -r path; do
    mkdir -p "$work/context/$(dirname "$path")"
    printf 'synthetic source\n' > "$work/context/$path"
done < "$work/expected"
for path in config.yaml config.container.yaml private-custom.yaml secrets/password \
    data/feedbot.db data-backup-example/feedbot.db bin/kallisti-feedbot coverage.out \
    .git/config .env .github/workflows/ci.yml cmd/kallisti-rss/password \
    internal/bot/.env licenses/private.key scripts/private-token; do
    mkdir -p "$work/context/$(dirname "$path")"
    printf 'synthetic excluded file\n' > "$work/context/$path"
done
printf 'FROM scratch\nCOPY . /\n' | docker buildx build --network=none \
    --output "type=local,dest=$work/export" -f - "$work/context"
(cd "$work/export" && find . -type f) > "$work/exported-paths"
cut -c 3- "$work/exported-paths" > "$work/actual.unsorted"
LC_ALL=C sort "$work/actual.unsorted" > "$work/actual"
diff -u "$work/expected" "$work/actual"
printf 'Git exclusions and Docker source-only context verified.\n'
