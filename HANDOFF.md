# Linux signed publication and upstream handoff

Repository: https://github.com/cortexobscura/kallisti-rss
First-party license: The Unlicense (owner confirmed). Third-party notices remain applicable.

On 2026-10-26 the public API reported a public empty repository, default branch `main`, zero branches and no committed license. This is a snapshot, not permission to overwrite later commits. This source bundle contains no `.git` directory or signed commits. Use your **existing Linux clone and existing signing setup**; do not move private keys to another machine or initialize a second repository over it.

## Review and overlay into the existing clone

Keep the running installation separate from release preparation where possible. Stop it before backing up or inspecting its SQLite database. Back up your existing clone/local modifications and operational files privately outside the clone before overlaying anything. Do not commit the backup.

From your existing clone, inspect first:

```sh
git status --short --branch
git remote -v
git branch --show-current
git log -1 --oneline
```

`git log` can report no commits for an unborn branch; that is expected only if this is still an empty clone. Confirm `origin` identifies `cortexobscura/kallisti-rss` (SSH or HTTPS) and retain local modifications. If another repository/branch/history appears, stop and reconcile it; do not force/reset/clean.

Extract the new bundle **outside** the clone. Set RELEASE_DIR to the extracted `kallisti-rss` directory. A preview overlay from inside your clone:

```sh
RELEASE_DIR=/absolute/path/to/extracted/kallisti-rss
rsync -av --dry-run --exclude='.git/' --exclude='bin/' --exclude='data/' \
  --exclude='secrets/' --exclude='config.yaml' --exclude='config.container.yaml' \
  "$RELEASE_DIR/" ./
```

Review every proposed overwrite against your local edits. Only after resolving overlaps, repeat without `--dry-run`. Never add `--delete`; preserve `.git`, user config, secrets and database. Nonstandard operational paths/config names also require manual protection.

**Rename cleanup is explicit:** the old bundle had `cmd/kallisti-feedbot/main.go` and `cmd/kallisti-feedbot/pause_test.go`; they moved to `cmd/kallisti-rss/`. An overlay leaves the old directory behind. Inspect it for local changes, merge those changes into the new command directory if necessary, then move the old directory to your private backup **outside the repository**. Otherwise its stale module imports can break `go test ./...`. Similarly retire the old incident-specific `RETIRE-CHANNEL-5.md` after review; its generic replacement is `OPERATIONS.md`. Do not delete unrelated files. Git will record tracked removals when you stage them.

`make build` produces `bin/kallisti-rss` and an identical `bin/kallisti-feedbot` compatibility executable. Existing service paths/CLI flags, YAML, credential paths, database schema and identity bindings remain valid. The Go module and source command path changed; no SQLite reset is required. The container entrypoint remains `/feedbot`, Compose service remains `feedbot`, and image tag is now `kallisti-rss:local`.

## Run acceptance gates

```sh
sh scripts/check-release-inputs.sh
make release-check
./bin/kallisti-rss -config config.example.yaml -check-config
cmp bin/kallisti-rss bin/kallisti-feedbot
docker compose config --quiet
docker build -t kallisti-rss:test .
docker run --rm --network=none --memory=512m --cpus=1 kallisti-rss:test -demo
```

`scripts/check-release-inputs.sh` checks Git exclusions and Docker's actual build-context filtering with synthetic files. It needs Git, Docker and Buildx; it never copies operational contents. Keep `.gitignore`, `.dockerignore`, `scripts/` and `.github/workflows/ci.yml` when copying or extracting the source. Stop if any are missing or the input check fails. `.dockerignore` admits only Go source, module files and license notices; update and retest that list when build inputs change.

`release-check` runs formatting check, vet, race/unit/integration tests, native build, synthetic demo, ten-second parser fuzzing, govulncheck and gosec. Go 1.27.3+ is required; initial toolchain/modules/scanners may download public dependencies. Docker commands require an existing Docker runtime; do not treat skipped commands as passing. The network-disabled container demo still uses its own loopback fixture, never live feeds/accounts.

Before deployment, separately verify nonroot UID65532 data/secret/config mount permissions through the supplied Compose setup on your host. No container start against live configuration is part of the fixture gates above. Keep deployment separate from publication. Record the actual Kallisti revision, Go/OS versions and tested outcomes. Owner reports successful real images and intervals, but hosted CI/Docker and a reproducible upstream runtime acceptance run remain distinct evidence.

If distributing standalone binaries, include LICENSE, THIRD_PARTY.md, licenses/ and the Go toolchain LICENSE beside them. The container already copies notices to `/usr/share/doc/kallisti-rss/`. The GitHub workflow `.github/workflows/ci.yml` runs these gates on pushes and pull requests, with read-only repository permissions and pinned action commits. It does not publish images or deploy. No Gitea workflow is supplied. Base images still use explicit version/tag references, so digest pinning/reproducible image policy remains an upstream decision.

## Review the staged source, then sign locally

Inspect and stage only reviewed source/docs/workflows, including known renamed-file removals. Do not blindly stage operational files. `.gitignore` excludes conventional secret/data/config/build locations, private exports and database backups, but cannot know every custom filename and does not protect already tracked files. Verify exclusions before staging with `git check-ignore config.yaml secrets/password data/feedbot.db coverage.out bin/kallisti-feedbot`; all five paths must be reported. Review custom operational paths separately.

```sh
git status --short
git diff --check
git diff
# Stage only the files and tracked removals you reviewed, e.g. git add <paths>.
git diff --cached --check
git diff --cached --stat
git diff --cached --name-only
git diff --cached
git var GIT_AUTHOR_IDENT
git var GIT_COMMITTER_IDENT
```

Check that the identity is the intended account and the staged diff contains no passwords/tokens, private configs, SQLite/WAL/SHM/backups, outbox exports, runtime logs, downloaded media, binaries or coverage files. Review existing staged changes too. Inspect signing configuration on your Linux machine using your established workflow; do not paste private-key material into an agent/chat. No name/email/signing-key settings are changed by these instructions.

```sh
git commit -S -m "Add Kallisti RSS and Atom channel bot"
git log -1 --show-signature --format=fuller
git verify-commit HEAD
```

If signing or local verification fails, stop and fix your existing signing setup. SSH signature verification can require an appropriate local allowed-signers configuration; do not silently switch identity, disable signing, or substitute an unsigned commit. GitHub's Verified display is a separate check after publication.

## Check the remote again and publish without force

```sh
git fetch origin
git ls-remote --symref origin HEAD
git ls-remote --heads --tags origin
```

If these fail, stop. If any refs/history now exist, review/reconcile them before proceeding; the earlier empty-repository snapshot is no longer sufficient. No force push, reset or history rewrite is prescribed. For a still-empty remote and your reviewed signed HEAD intended as initial `main`:

```sh
git push --dry-run origin HEAD:refs/heads/main
git push --set-upstream origin HEAD:refs/heads/main
```

Review the dry-run before the actual push. A normal push rejects conflicting history. Confirm the repository files, commit signature display and Actions results on GitHub afterward. A successful push does not establish CI success. Address failed checks before tagging/releasing; no automatic image publication or repository transfer is configured.

## Upstream review packet

Send upstream the repository/commit link, concise purpose/compatibility summary, first-party license and notice inventory, actual CI/container results, `SPEC.md`, `PROTOCOL.md`, `SECURITY.md`, `VERIFICATION.md` and known limitations. Preserve existing nonce/uncertainty semantics, public-only fetch policy, manager allowlist, single-replica DB lock and YAML/SQLite precedence during review. Supply no production database or credentials. Agree with maintainers on destination organization, access, ownership and private security-reporting contact before any repository transfer; transfer is not part of this handoff.

Known limits include serial polling/delivery, retained history/disk growth, bounded duplicate protection (not exactly-once), unsupported old servers without nonce echoes, no private feeds/gallery scraping/video, and operator reconciliation for uncertain sends. See SPEC and README for exact bounds.

Optional Pi/other-agent kickoff:

> Work in this existing clone. Read AGENTS.md, SPEC.md, PROTOCOL.md, SECURITY.md, VERIFICATION.md and HANDOFF.md. Inspect git status and preserve all local changes and operational files. Run make release-check and available container fixture gates; report passed, failed and unrun separately. Review only concrete release regressions and existing acceptance gaps; do not expand product scope or reset SQLite. Do not read private keys/credentials, post live messages, commit, push, deploy or transfer unless I explicitly authorize that action. Use my established Linux signing workflow for any later authorized commit.
