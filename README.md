# kallisti-rss

[![ci](https://github.com/kallisti-chat/kallisti-rss/actions/workflows/ci.yml/badge.svg?branch=main)](https://github.com/kallisti-chat/kallisti-rss/actions/workflows/ci.yml)
[![go version](https://img.shields.io/github/go-mod/go-version/kallisti-chat/kallisti-rss)](go.mod)
[![license: unlicense](https://img.shields.io/badge/license-unlicense-blue)](LICENSE)

Go RSS/Atom bot for Kallisti text channels, with public-feed image attachments, durable SQLite subscriptions, and in-chat management. This is a v0.1 release candidate with passing local fixture checks and owner-reported live use. Local Docker fixtures and hosted CI for implementation commit `28f1a0d` pass; partial owner-run live checks of `v0.1.0-rc.1` through `v0.1.0-rc.3` against a Kallisti v0.10.0 server passed on 2026-10-30 (see [VERIFICATION.md](VERIFICATION.md)), and the full acceptance procedure below remains a release gate. Compatibility targets Kallisti commit `d8fd12dd2a6f1483c8fdee187ebed827b3bd2efc`, not generic Murmur.

## Start with the offline demo

Requires Go **1.27.3 or newer**, Linux or macOS. Go can download the requested toolchain automatically. Dependencies are pinned in go.mod/go.sum; initial build needs Internet access. No other running service is required.

```sh
make check
make serve-demo
make security
make fuzz
```

The demo binds an ephemeral loopback server with synthetic credentials. It exercises the production parser, image validator, SQLite engine, binary Mumble/JSON WebSocket handshake, raw PNG upload, nonce acknowledgment, restart deduplication, and command transaction. It fetches no live feeds and sends no live messages. Test fixtures are original synthetic content. Fuzzing is bounded to ten seconds by default. `make check` includes vet, unit/integration/race checks and a static binary build; security is a separate explicit target and mandatory in CI. See [VERIFICATION.md](VERIFICATION.md) for exact observed results.

## Configure a real test server

1. Provision a dedicated **registered account** in Kallisti using its normal account flow. Do not use an administrator account for the bot. Grant it view/send/upload permissions only in the target text channels. The bot does not create accounts, join voice, or elevate permissions.
2. Put that account's existing password in a private local file and reference it with `password_file` in YAML. The bot POSTs `{name,password}` to the fixed HTTPS `/api/login`, requests a token via `X-Kallisti-Session: token`, and keeps the returned session only in memory. It never creates accounts, writes credentials, logs passwords/tokens or sends them to feeds. Reconnects reuse the session; a fresh login occurs after 29 days on a reconnect. Restart the process after rotating password_file or revoking its session; a rejected cached token is deliberately not replaced on every network failure. Session login creates a normal server-side login session; no chat message is sent by login itself.
3. Alternatively, use `token_file` with an existing web-account session token from your trusted login workflow. Configure **exactly one** of password_file/token_file. Token files are reread on reconnect; upstream sessions normally expire after 30 days. Password login requires a permanent web account with a password; native-Mumble-only login responses are explicitly unsupported. Keep all credential files out of source control and issue reports.
4. Copy config.example.yaml to config.yaml. Set the HTTPS server, allowed Origin, username and credential-file path. Discover channel and registered account IDs without posting:

```sh
make build
./bin/kallisti-rss -config config.yaml -discover
```

Discovery authenticates and briefly appears online, then prints the bot's registered ID, visible text-channel store IDs/names, and registered IDs/names of users online during handshake. It performs no feed polling, uploads, messages or SQLite writes; configured channel visibility is not required for discovery. Have intended managers online, verify their identities through your normal trusted moderation workflow, then set `admin_ids`. Offline users are not enumerated. IDs are **registered Mumble uint32 IDs**, not display names or web database IDs. Channel IDs are **text-channel store IDs**, not voice IDs. An empty manager list disables all chat management.
5. Set target `channels` and per-channel `feeds`, then validate and run. The commands below create directories only; enter credentials using a trusted editor. For two servers use **two dedicated accounts, configs, processes/containers, secret files and SQLite directories**. Each process remains single-server/single-replica; do not share a bot database across servers.

```sh
mkdir -p data secrets
chmod 700 data secrets
cp config.example.yaml config.yaml
# Edit config.yaml and secrets/password, then:
chmod 600 config.yaml secrets/password
make build
./bin/kallisti-rss -config config.yaml -check-config
./bin/kallisti-rss -config config.yaml
```

The first **successful** poll seeds current entry IDs without posting backlog. A failed/HTML login response does not initialize the subscription. The default poll interval is five minutes; 30 seconds to 24 hours is accepted. Per-feed HTTP failures are isolated and retried on the next poll. Connections retry every 30 seconds; the delivery scheduler wakes every five seconds, while failed jobs use persisted backoff beginning at 30 seconds. Logs contain subscription/job identifiers and controlled error classes/reasons, never full feed URLs, tokens, raw server error text or response bodies. SIGTERM/SIGINT cancel ongoing requests. Polling and delivery are serial and bounded; large feed sets or slow image hosts can delay command processing.

Normal startup verifies configured channels are visible and the bot is registered; sending permissions are verified by actual server send results. Discovery is fixture-tested; no live account was used while building this bundle.

## Chat commands

In a configured channel, from an explicitly allowlisted verified registered ID:

```
!feed help
!feed list
!feed add https://example.org/feed.xml
!feed pause 3
!feed resume 3
!feed remove 3
```

All commands are scoped to their channel, deduplicated by server message ID, and ignored from the bot itself, guests, unverified accounts, history responses and edits. There is no display-name authorization. Registered ID 0 is supported **only if explicitly listed**. Messages from anyone else are discarded before queueing; if allowlisted commands arrive faster than the bot can process them, the excess is dropped with a `command dropped; command queue full` warning. Commands are live events: if the connection is down, commands posted during that outage are not replayed from history. Send them again after reconnection. `list` is bounded to 2000 characters and may truncate large lists.

Pause/remove stops future polling for that subscription; already queued messages stay queued and can be reconciled offline. Pausing a paused subscription or resuming an active one changes nothing; the reply says it was already in that state. Resume retains seen IDs, so entries published while paused may be delivered if still present in the next feed response. Re-adding a removed URL restores it under the same ID with its existing seen history. `add` takes the feed URL, not an ID. Adding a URL that is already subscribed in the channel changes nothing, even if it is paused; use `resume` for that. Never reuse a database with a different server, account name or registered bot ID; startup rejects that mismatch.

## YAML baseline and SQLite state

YAML **seeds missing `(channel, URL)` pairs** on each startup. SQLite then owns initialized/paused/deleted state and seen IDs. YAML is never rewritten. A removed seed has a durable tombstone, so restarting does not re-add it. Adding a new YAML URL creates a new subscription. Removing a URL from YAML alone does not delete its SQLite subscription; use the chat command. Changing `max_images` in YAML does not change an existing SQLite seed; set it before first subscription or edit the stopped database after backup. Chat additions default to three images. Keep configured channel IDs until their subscriptions and pending work are resolved.

The exact durable subscription state can be exported with the SQLite CLI while the bot is stopped:

```sh
sqlite3 -json data/feedbot.db 'SELECT id,channel,url,max_images,initialized,paused,deleted FROM subscriptions ORDER BY id;' > subscriptions.json
```

This is an operator export, not a YAML import. To reset completely, **stop the bot, back up and move the whole data directory**, create a fresh private directory, and start with the desired YAML. A reset discards local history and pending work; first-poll baselining prevents old entries flooding but cannot reconcile uncertain earlier sends. Never reset to work around an uncertain send without reviewing it first. Database rows and source URLs remain local; no telemetry is sent by the bot.

## Images and feed compatibility

Explicit image candidates are collected from RSS image enclosures, Media RSS content/thumbnails (including groups), Atom image enclosure links, and `<img src>` inside feed HTML. Relative links resolve against the configured feed URL. Candidates are deduplicated; default **three per item**, configurable per YAML seed using `max_images: 1..10` (`0` means default three). The server's advertised attachment count and byte limit further constrain uploads.

PNG, JPEG, GIF and static WebP decode locally; GIF uses its first frame. Animated WebP may be unsupported and skipped. Images are normalized to PNG, removing metadata, before upload. AVIF/SVG/video are not decoded. Each input and normalized output is at most 8 MiB, dimensions at most 8192 per side and 16 million pixels. An invalid, unreachable, unsupported or over-limit image is skipped **before the first send**, with a log warning; title/link/summary still publish. The payload never falls back to different text or a new nonce after an uncertain send. Images are not scraped from article/gallery pages. Lazy-load-only `data-src`, CSS backgrounds, external galleries, JSON feeds and authenticated feeds are outside v1.

The Aniyuki example is represented by an original WordPress fixture with multiple HTML image tags and stable GUIDs. Upstream research observed image-rich `content:encoded`; this bundle did not fetch live Aniyuki images. Reddit returned an HTML login/block page in the research environment: that is an error, not an empty successful feed. Use feeds whose public access terms permit this client; no login bypass is included. Both supplied YouTube URLs returned HTTP 200 during read-only inspection: the production parser extracted 15 entries with thumbnails from each. The optional URLs are commented in config.example.yaml. Generic YouTube Atom thumbnail and news RSS support is also fixture-tested; video is neither downloaded nor played.

Images are **rehosted on your Kallisti server**. Upstream attachment URLs use unguessable keys but are not protected by channel ACL on GET. Anyone who obtains that URL may read the file, including outside a private channel. Use public, redistributable feeds; public availability does not itself grant republication rights. Feed-controlled text is converted to plain text, truncated by Unicode rune count and neutralizes `@` pings; URLs remain visible. No feed credentials/cookies are supported. HTTPS is required by default; explicit `allow_http_feeds: true` enables public HTTP port 80 only. Private, loopback, reserved, link-local and mixed public/private DNS answers are rejected. Feed redirects reapply the policy and never inherit server credentials.

## Delivery recovery and backup

Delivery offers durable retry with **bounded duplicate protection**, not exactly-once delivery. Before a send, SQLite persists its nonce, payload and first-attempt time. A matching nonce acknowledgment marks delivery complete. Lost acknowledgments reuse the same payload and nonce for less than 23 hours. After that (or a backwards clock), the job is blocked for operator reconciliation. Other channels can proceed; later jobs in the same channel remain behind that barrier. Upstream dedup lasts 24 hours and is stored with the message: server-side message deletion can remove protection even inside that window. Protect clocks, server data, and the bot account. Do not run multiple replicas or copy the database to another instance.

The database holds an exclusive OS lock. Stop the running bot before using offline maintenance commands:

```sh
./bin/kallisti-rss -config config.yaml -status
./bin/kallisti-rss -config config.yaml -outbox > unresolved.json
# After inspecting the channel and confirming the specific outcome:
./bin/kallisti-rss -config config.yaml -resolve-job 12 -resolution delivered
# Or intentionally abandon that job without sending it:
./bin/kallisti-rss -config config.yaml -resolve-job 12 -resolution discarded
```

No command automatically resets or resends an old uncertain job. A refused send stays pending until resolved or aged out. Recognized channel-local rejections delay only that channel; connection, storage, account/server-rate, policy and unknown failures pause all delivery conservatively. A crashed preparation may leave orphan uploads; upstream removes unattached files after ten minutes. Before a first send, upload sources and preparation time are retained; a restart refreshes preparation older than nine minutes. After any attempted send, keys and payload remain frozen. An expired key on an uncertain job can cause refusal; reconcile/discard it instead of silently creating new attachments. Server throttling, permission problems and quota limits can also require operator action. There is a 10,000 unresolved-job cap and 100 active-subscription limit. Each poll queues at most 25 new posts per subscription, and a channel with 1,000 unresolved jobs queues none until it drains; deferred entries stay unseen and post oldest-first on later polls if still in the feed (`feed entries deferred` warning). Seen IDs, command IDs and delivered rows are retained indefinitely to preserve deduplication: monitor disk usage and use a dedicated quota/volume; no unsafe automatic pruning is included.

For backup: stop the process, copy the entire data directory (including any WAL/SHM files), config, and securely managed token file. Restore while stopped with the same server/account and permissions. A snapshot predating delivered sends may reintroduce unknown work, so reconcile before reconnecting. SQLite is not encrypted; protect backups and filesystem permissions.

## Container and CI

Deployment guides and editable examples are available in:

- [deploy/docker](deploy/docker/README.md): Compose, container config example,
  optional image settings, persistent data and credential mounts.
- [deploy/kubernetes](deploy/kubernetes/README.md): Kustomize, a single-replica
  Deployment, PVC, ConfigMap generation and separately managed credentials.

Both use the root Alpine Dockerfile and UID/GID 65532, with a read-only root,
resource bounds and no inbound service. No public image is assumed; build your
own. The root `compose.yaml` remains compatible with existing installs using
`config.container.yaml`; new installs can follow the Docker directory guide.

Run `make check-deploy` to validate both Compose files, deployment configuration
examples and rendered Kubernetes schemas using synthetic/public inputs. This
requires Docker Compose and Go; pinned Kustomize/kubeconform tools and schemas
may be downloaded. It does not contact a cluster or start a production service.

Run `make check-compose` after building the image to exercise
`deploy/docker/compose.yaml` offline: synthetic config, networking disabled,
offline actions through `docker compose run`, the database lock and SIGTERM
shutdown. Both Compose files pin `-config /config/config.yaml` in the
entrypoint, so `docker compose run feedbot -status` reads the mounted config.

The original build report did not execute Docker; see [VERIFICATION.md](VERIFICATION.md) for later local results. The GitHub workflow in `.github/workflows/ci.yml` runs `make release-check`, validates the example config and compatibility binary, validates Compose, runs the offline Compose runtime check, and builds/runs the demo image with external networking disabled for the demo. Actions use pinned commits and read-only repository permissions on GitHub-hosted runners. No image publication or deployment is configured, and no Gitea workflow is supplied. A successful hosted run is recorded in VERIFICATION.md; it does not verify live interoperability or production mounts. Required-check branch protection must be configured by the repository owner.

Before building, run `sh scripts/check-release-inputs.sh` with Git, Docker and Buildx available. It verifies Git exclusions and Docker's build-context filtering using synthetic files. `.dockerignore` admits only Go source, module files and license notices; configuration, credentials, databases and backups must stay outside the build context. Preserve the dotfiles when copying the source. `.gitignore` protects conventional untracked operational paths, not already tracked files or every custom filename. Review all staged content before publishing. Base images still use tags rather than pinned digests.

## Before considering a deployment verified

Run the offline suite, then in a dedicated channel with your account: validate startup registration/channel IDs; confirm no backlog; publish one new RSS and one Atom item; verify expected images and plain summary; test an allowlisted add/pause/resume/remove and a denied user; restart and check no duplicate; disconnect during a send and reconcile the stable nonce; inspect blocked recovery using offline commands. Confirm TLS and Origin policy and test the built container separately. Record server revision, Go version and outcomes. A fixture pass does not substitute for this runtime interoperability test.

See [SPEC.md](SPEC.md) for intended behavior, [ARCHITECTURE.md](ARCHITECTURE.md) for current structure, and [IMPLEMENTATION.md](IMPLEMENTATION.md) for evidence and known gaps. [PROTOCOL.md](PROTOCOL.md), [SECURITY.md](SECURITY.md), and [THIRD_PARTY.md](THIRD_PARTY.md) cover protocol, trust and licensing. No upstream project files, live credentials or downloaded feed/media bodies are included.

## Troubleshoot a feed warning

`feed poll failed` is nonfatal to the run loop. A failed feed does not advance its baseline or seen IDs; the bot continues to other subscriptions and retries on the next scheduled poll cycle (five minutes by default). Initial successful polls deliberately record current entries without posting them. `feed poll completed` means fetch/parse/SQLite ingestion succeeded, not that a chat send was acknowledged; delivery has its own outbox.

New diagnostics include durable `subscription`, `channel`, one-based `yaml_feed`, `feed_host`, a short hashed `feed_key`, `error_class`, fixed `reason`, and `http_status` where available. Full URLs, query strings, paths, credentials, response bodies and raw transport errors are omitted. The hash distinguishes multiple URLs on the same host without disclosing query tokens. `yaml_feed=0` means the stored subscription is not present in the current YAML. A fresh database assigns IDs in YAML insertion order; existing IDs persist even if YAML is reordered.

After updating source, rebuild and run the read-only check on the same host as the bot:

```sh
make build
./bin/kallisti-rss -config config.yaml -check-feeds
```

This reads configured feeds only: no login, credential-file reads, image fetches, uploads, posting, or SQLite access. It can run while the bot is running. Exit 0 means every configured feed fetched/parsed; exit 1 means at least one failed. This does not inspect chat-added feeds absent from YAML. To see the actual durable mapping, stop the running bot and run:

```sh
./bin/kallisti-rss -config config.yaml -subscriptions
```

That offline JSON view prints ID/channel, matching YAML index, host/hash, initialized/paused state and image cap. It uses the same exclusive database lock and makes no network requests. Keep the existing configuration, secrets and data directory when upgrading; the earlier diagnostics update needs no reset. The retry-isolation update adds only an idempotent retry-metadata table and index on startup; existing subscriptions, jobs, payloads, nonces, destinations and First values remain intact.

For HTTP 403/401, the upstream is denying the request; do not bypass login/access controls. For 429, reduce request frequency and respect upstream policy. For DNS/timeouts/TLS, investigate connectivity/certificates on the bot host without disabling TLS or public-address validation. For parse_error, a server may have returned HTML instead of RSS/Atom. In a read-only probe from the build environment, Reddit returned **403** while Aniyuki and both supplied YouTube feeds parsed successfully. Results may differ on your host; the old generic warning alone cannot establish its cause.


## Channel-isolated retries and delivery diagnostics

`delivery acknowledged` is emitted only after a matching nonce acknowledgment **and** successful SQLite delivered-state commit. Failure logs include job/channel, stage, controlled reason/class, scope, first-attempt marker/age, failed-attempt count and next retry time. `First` is written before Send; it does not prove bytes reached the server. Unknown or oversized server errors are never printed or persisted as raw text.

Only a nonce-correlated rejection with an audited meaning permits another channel to proceed:

| Error class | Scope / behavior |
|---|---|
| channel_missing, channel_permission | Hold this destination's earliest job; other channels continue |
| attachment_rejected | Hold this destination behind the rejected immutable attachment reference; no replacement after first send |
| channel_slow_mode | Local only with matching channel, exact slow-mode metadata and bounded wait; respect that wait |
| server_rate_limit, server_not_ready, server_storage, content_domain_blocked, content_word_blocked, text_length_rejected, invalid_nonce | Global conservative cooldown |
| disconnected, write_failed, ack_timeout, rejection_channel_mismatch, unknown errors | Global cooldown; outcome/meaning may be uncertain |
| local storage failure | Stop delivery globally; no subsequent send without durable state |

The scheduler considers only the oldest unresolved job in each channel. Pending and quarantined (`blocked`) jobs are both ordering barriers. A local failure is not bypassed by another message in its own channel. Other channels may therefore be delivered out of global job-ID order, but each channel retains its order. A global failure immediately stops the current pass; the persisted global cooldown applies across reconnect/restart.

Retries back off 30 seconds, 60 seconds, 120 seconds, and so on up to 15 minutes; validated slow-mode waits can extend this to one hour. The next retry never extends the conservative 23-hour first-attempt horizon. Cooldown timer ticks do not repeat warnings or send requests. If SQLite cannot persist retry metadata, the process uses a 30-second memory-only global cooldown and logs that limitation; restarting cannot preserve a write that failed. No automatic reset, discard, route change, or false delivered marking is performed.

`-outbox` now adds `Retry` metadata (failed attempts, next-attempt Unix time, scope, class, stage). Its payload contains original feed text/URLs and attachment keys; review privately rather than pasting the entire dump publicly. There is no retry-now or bypass-horizon command. Fixing an underlying permission problem takes effect on the next eligible retry.

## Reconcile obsolete YAML destinations explicitly

Changing a feed's channel in YAML creates a new `(channel, URL)` subscription. `yaml_feed=0` marks an existing SQLite subscription absent from current YAML; it is **not** disabled. This behavior is unchanged by the retry fix. New-route jobs and old-route jobs are separate, with distinct stable nonces.

1. Stop the bot and back up the entire data directory. Keep the current config and secrets separate from the source upgrade. Rebuild the new source; use the existing database, not a fresh one.
2. Inspect `-subscriptions` and `-outbox` while stopped. Identify which old subscriptions and queued destinations you actually want to retire. Do not assume an uncertain post failed just because the local job is still pending.
3. While stopped, pause each verified obsolete subscription with `./bin/kallisti-rss -config config.yaml -pause-subscription ID -expected-channel CHANNEL`. Both positive IDs are mandatory; a missing/deleted subscription or wrong stored channel fails without pausing anything. Other action flags and positional arguments are refused. The command uses the exclusive database lock, performs no login/network requests, does not seed YAML or enqueue a reply, and preserves seen history and existing queue payloads/nonces. Repeat pauses are safe. Verify `paused: true` with `-subscriptions`. YAML reseeding does not undo a pause. This works even when the old remote channel no longer exists. The existing chat `!feed resume ID` reverses a pause if that channel is available and configured; there is no offline resume mode. Chat pause/remove remains available from an allowlisted account in the original channel. Neither route clears queued jobs.
4. Reconcile each already queued job separately against the destination channel. Only after confirming it was actually delivered, use the offline `-resolve-job ID -resolution delivered`. If you intentionally choose to abandon that old-route job, use `-resolve-job ID -resolution discarded`. These commands do not send anything or reroute content. The owner must make that decision for each job; the bot never guesses. Resolving the oldest barrier permits the next same-channel job to become eligible.
5. Restart and watch classified failures/acknowledgments. New channels can progress during known local failures, but global/unknown failures still require investigation. Keep the old channel configured until its subscriptions and unresolved jobs are reconciled, then remove it from the configured channel list if desired.

Do not delete SQLite, edit nonces/First, mark unknown work delivered, or use a new database to clear a queue. No live state is changed by downloading this bundle.

For retiring obsolete subscriptions and queued work, see [OPERATIONS.md](OPERATIONS.md). Verify fresh IDs before running its operator-only commands.

## Per-channel polling and image diagnostics

Existing YAML remains valid. `poll_interval` defaults to five minutes. Optional overrides use configured text-channel store IDs:

```yaml
poll_interval: 5m
channels: [6, 7, 8, 9, 10, 11, 12]
channel_poll_intervals:
  8: 1m
  11: 15m
```

Here channel 8 polls every minute, channel 11 every fifteen minutes, and other channels inherit five minutes. Values must be 30s..24h; unknown channel IDs, invalid durations and duplicate keys fail configuration validation. Keep the rest of your existing config. Run `-check-config`, then restart the stopped bot using the same database; no migration/reset is required. Overrides apply to all persisted active subscriptions in that channel, including chat-added subscriptions; they do not resume paused subscriptions or change routes.

Only due channels are fetched/ingested. Polling starts immediately on process startup, preserving existing baseline/seen state. Reconnects retain the current deadlines. Subsequent deadlines are the channel interval after a completed due batch, on success or failure. There is no catch-up burst; slow serial requests/delivery can delay checks. A one-second scheduler tick checks deadlines, not all feeds. Database-read errors cool down for 30 seconds. Identical URLs due in one batch share parsed results/failures where cache capacity permits (maximum eight retained results, released at last use); slower channels never ingest early. There is no cross-cycle stale cache. Original subscription traversal and independent seen state are preserved.

Delivery still has its own five-second timer, server pacing and durable retries; polling overrides are not post rate limits. An existing backlog can send regardless of the next poll deadline.

Safe image evidence in logs:

- `feed poll completed`, stage `extraction`: `image_entries` and `image_candidates` count the parsed snapshot, including already-seen/baseline entries. Zero means no usable candidates extracted; nonzero alone does not mean a new post was queued. The parser accepts explicit enclosure/Media RSS/HTML img sources; it does not scrape linked pages.
- `image skipped before first send`: `image_fetch`, `image_decode` or `image_upload` identifies the failed stage, with a sanitized class/status. This separates failed download/policy checks, unsupported or oversized raster validation, and upload rejection.
- `image preparation completed`: candidates, attempted (after server cap), downloaded, validated and uploaded counts. `text_fallback=true` means all attempted images failed before first send. Partial success sends the successfully uploaded subset.
- `delivery acknowledged`: `attachments_acknowledged` counts attachment keys in the matched, durably committed message acknowledgment. `text_only=true` with zero image candidates means no image was queued; with candidates it indicates fallback. It does not prove a client rendered the image.
- Failed delivery logs include `attachments_in_payload`, never an acknowledgment claim. Permission rejection or lost acknowledgment preserves the existing immutable payload/nonce and normal retry policy; no replacement text-only send is created after uncertainty.

Logs omit raw feed/HTML/media content, full URLs/query tokens and upload keys. The new tests use synthetic fresh RSS entries after a baseline, including enclosure and content:encoded images, through a local TLS upload/WebSocket fixture. The owner reports successful live images from Reddit, Pleroma, Mastodon and GoToSocial. Read-only inspection confirmed the Cofe test RSS item omitted its attachment (tracked by the owner as Cofe issue 33); the image itself passed download/raster checks. No live image delivery was attempted by this build task.
