# kallisti-rss

[![ci](https://github.com/kallisti-chat/kallisti-rss/actions/workflows/ci.yml/badge.svg?branch=main)](https://github.com/kallisti-chat/kallisti-rss/actions/workflows/ci.yml)
[![go version](https://img.shields.io/github/go-mod/go-version/kallisti-chat/kallisti-rss)](go.mod)
[![license: unlicense](https://img.shields.io/badge/license-unlicense-blue)](LICENSE)
Go RSS/Atom bot for Kallisti text channels. It posts new feed entries with
their images, keeps subscriptions in SQLite, and is managed from chat.

**Status:** v0.1 release candidate. Offline checks and hosted CI pass, and the
owner ran the live acceptance procedure against a Kallisti `v0.11.0` server
on 2026-10-30 (see [VERIFICATION.md](VERIFICATION.md)); the separate
plain-container check is not yet recorded. It targets
Kallisti commit `d8fd12dd2a6f1483c8fdee187ebed827b3bd2efc`, not generic Murmur.

## Quick start: offline demo

Requires Go 1.27.3 or newer on Linux or macOS. No running services are needed.

```sh
make check        # vet, unit/integration/race tests, static build
make serve-demo   # end-to-end run against a local synthetic server
make fuzz         # bounded fuzzing (10s by default)
make security     # static and vulnerability audits
```

The demo uses synthetic credentials and fixtures; it fetches no live feeds and
sends no live messages.

## Configure a real test server

1. Create a dedicated, non-admin **registered account** for the bot and grant it
   view/send/upload in the target text channels only.
2. Put its password (or an existing session token) in a private file.
   Configure exactly one of `password_file` or `token_file`.
3. Copy `config.example.yaml` to `config.yaml` and set the HTTPS server,
   allowed Origin, username and credential file.
4. Discover channel and user IDs without posting anything:

   ```sh
   make build
   ./bin/kallisti-rss -config config.yaml -discover
   ```

   Have your managers online, then put their **registered user IDs** in
   `admin_ids` and the **text-channel store IDs** in `channels`.
5. Add per-channel `feeds`, validate, and run:

   ```sh
   mkdir -p data secrets && chmod 700 data secrets
   chmod 600 config.yaml secrets/password
   ./bin/kallisti-rss -config config.yaml -check-config
   ./bin/kallisti-rss -config config.yaml
   ```

The first successful poll of each feed records its current entries without
posting them, so there is no backlog flood. Run one process per server, with its
own account, config and data directory. Login, session and discovery details are
in [OPERATIONS.md](OPERATIONS.md#credentials-login-and-discovery).

## Chat commands

From an allowlisted registered ID, in a configured channel:

```
!feed help
!feed list
!feed add https://example.org/feed.xml
!feed pause 3
!feed resume 3
!feed remove 3
```

- Commands only affect the channel they are sent in. Guests, unverified users
  and display names are never trusted.
- `add` takes a URL. Re-adding a removed URL restores it with its seen history;
  adding an existing one changes nothing.
- Pause and remove stop future polling; already queued posts stay queued.
- Commands sent while the bot is disconnected are not replayed.

## Deploy

- [deploy/docker](deploy/docker/README.md): Docker Compose.
- [deploy/kubernetes](deploy/kubernetes/README.md): Kustomize, single replica.

Both run as UID 65532 with a read-only root and no inbound port. No public image
is published; build your own. `make check-deploy` and `make check-compose`
validate the deployment files offline.

## Before considering a deployment verified

In a dedicated channel with the bot's real account:

- [ ] startup confirms the registered ID and channel IDs
- [ ] a new subscription posts no backlog
- [ ] one new RSS item and one new Atom item post with expected images and a plain summary
- [ ] an allowlisted user can add, pause, resume and remove; a non-allowlisted user is ignored
- [ ] a restart posts no duplicates
- [ ] a disconnect during a send is reconciled with the same nonce
- [ ] a blocked job is recovered with the offline commands
- [ ] TLS and Origin policy are confirmed, and the built container is tested separately

Record the server revision, Go version and outcomes. Fixture tests do not
substitute for this run.

## Documentation

| Doc | Covers |
|---|---|
| [OPERATIONS.md](OPERATIONS.md) | state, backups, delivery recovery, diagnostics, polling, images, CI |
| [SPEC.md](SPEC.md) | intended behavior and contracts |
| [ARCHITECTURE.md](ARCHITECTURE.md) | current structure |
| [PROTOCOL.md](PROTOCOL.md) | Kallisti protocol notes |
| [SECURITY.md](SECURITY.md) | trust model and reporting |
| [IMPLEMENTATION.md](IMPLEMENTATION.md) | evidence and known gaps |
| [VERIFICATION.md](VERIFICATION.md) | recorded check results |
| [THIRD_PARTY.md](THIRD_PARTY.md) | licensing |
