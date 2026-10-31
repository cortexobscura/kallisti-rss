# Operations

Detailed operator reference for kallisti-rss. Start with [README.md](README.md)
for setup; this page covers runtime behavior, state, diagnostics and recovery.

- [Credentials, login and discovery](#credentials-login-and-discovery)
- [Runtime behavior](#runtime-behavior)
- [YAML baseline and SQLite state](#yaml-baseline-and-sqlite-state)
- [Per-channel polling and image diagnostics](#per-channel-polling-and-image-diagnostics)
- [Images and feed compatibility](#images-and-feed-compatibility)
- [Delivery recovery and backup](#delivery-recovery-and-backup)
- [Channel-isolated retries and delivery diagnostics](#channel-isolated-retries-and-delivery-diagnostics)
- [Troubleshoot a feed warning](#troubleshoot-a-feed-warning)
- [Reconcile obsolete YAML destinations explicitly](#reconcile-obsolete-yaml-destinations-explicitly)
- [Container builds and CI](#container-builds-and-ci)

## Credentials, login and discovery

Provision a dedicated **registered account** in Kallisti using its normal account flow. Do not use an administrator account for the bot. Grant it view/send/upload permissions only in the target text channels. The bot does not create accounts, join voice, or elevate permissions.

Put that account's existing password in a private local file and reference it with `password_file` in YAML. The bot POSTs `{name,password}` to the fixed HTTPS `/api/login`, requests a token via `X-Kallisti-Session: token`, and keeps the returned session only in memory. It never creates accounts, writes credentials, logs passwords/tokens or sends them to feeds. Reconnects reuse the session; a fresh login occurs after 29 days on a reconnect. Restart the process after rotating password_file or revoking its session; a rejected cached token is deliberately not replaced on every network failure. Session login creates a normal server-side login session; no chat message is sent by login itself.

Alternatively, use `token_file` with an existing web-account session token from your trusted login workflow. Configure **exactly one** of password_file/token_file. Token files are reread on reconnect; upstream sessions normally expire after 30 days. Password login requires a permanent web account with a password; native-Mumble-only login responses are explicitly unsupported. Keep all credential files out of source control and issue reports.

Discovery authenticates and briefly appears online, then prints the bot's registered ID, visible text-channel store IDs/names, and registered IDs/names of users online during handshake. It performs no feed polling, uploads, messages or SQLite writes; configured channel visibility is not required for discovery. Have intended managers online, verify their identities through your normal trusted moderation workflow, then set `admin_ids`. Offline users are not enumerated. IDs are **registered Mumble uint32 IDs**, not display names or web database IDs. Channel IDs are **text-channel store IDs**, not voice IDs. An empty manager list disables all chat management.

## Runtime behavior

The first **successful** poll seeds current entry IDs without posting backlog. A failed/HTML login response does not initialize the subscription. The default poll interval is five minutes; 30 seconds to 24 hours is accepted. Per-feed HTTP failures are isolated and retried on the next poll. Connections retry every 30 seconds; the delivery scheduler wakes every five seconds, while failed jobs use persisted backoff beginning at 30 seconds. Logs contain subscription/job identifiers and controlled error classes/reasons, never full feed URLs, tokens, raw server error text or response bodies. SIGTERM/SIGINT cancel ongoing requests. Polling and delivery are serial and bounded; large feed sets or slow image hosts can delay command processing.

Normal startup verifies configured channels are visible and the bot is registered; sending permissions are verified by actual server send results. Discovery is fixture-tested; no live account was used while building this bundle.

## YAML baseline and SQLite state

YAML **seeds missing `(channel, URL)` pairs** on each startup. SQLite then owns initialized/paused/deleted state and seen IDs. YAML is never rewritten. A removed seed has a durable tombstone, so restarting does not re-add it. Adding a new YAML URL creates a new subscription. Removing a URL from YAML alone does not delete its SQLite subscription; use the chat command. Changing `max_images` in YAML does not change an existing SQLite seed; set it before first subscription or edit the stopped database after backup. Chat additions default to three images. Keep configured channel IDs until their subscriptions and pending work are resolved.

The exact durable subscription state can be exported with the SQLite CLI while the bot is stopped:

```sh
sqlite3 -json data/feedbot.db 'SELECT id,channel,url,max_images,initialized,paused,deleted FROM subscriptions ORDER BY id;' > subscriptions.json
```

This is an operator export, not a YAML import. To reset completely, **stop the bot, back up and move the whole data directory**, create a fresh private directory, and start with the desired YAML. A reset discards local history and pending work; first-poll baselining prevents old entries flooding but cannot reconcile uncertain earlier sends. Never reset to work around an uncertain send without reviewing it first. Database rows and source URLs remain local; no telemetry is sent by the bot.

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

## Reconcile obsolete YAML destinations explicitly

Changing a feed's channel in YAML creates a new `(channel, URL)` subscription. `yaml_feed=0` marks an existing SQLite subscription absent from current YAML; it is **not** disabled. This behavior is unchanged by the retry fix. New-route jobs and old-route jobs are separate, with distinct stable nonces.

1. Stop the bot and back up the entire data directory. Keep the current config and secrets separate from the source upgrade. Rebuild the new source; use the existing database, not a fresh one.
2. Inspect `-subscriptions` and `-outbox` while stopped. Identify which old subscriptions and queued destinations you actually want to retire. Do not assume an uncertain post failed just because the local job is still pending.
3. While stopped, pause each verified obsolete subscription with `./bin/kallisti-rss -config config.yaml -pause-subscription ID -expected-channel CHANNEL`. Both positive IDs are mandatory; a missing/deleted subscription or wrong stored channel fails without pausing anything. Other action flags and positional arguments are refused. The command uses the exclusive database lock, performs no login/network requests, does not seed YAML or enqueue a reply, and preserves seen history and existing queue payloads/nonces. Repeat pauses are safe. Verify `paused: true` with `-subscriptions`. YAML reseeding does not undo a pause. This works even when the old remote channel no longer exists. The existing chat `!feed resume ID` reverses a pause if that channel is available and configured; there is no offline resume mode. Chat pause/remove remains available from an allowlisted account in the original channel. Neither route clears queued jobs.
4. Reconcile each already queued job separately against the destination channel. Only after confirming it was actually delivered, use the offline `-resolve-job ID -resolution delivered`. If you intentionally choose to abandon that old-route job, use `-resolve-job ID -resolution discarded`. These commands do not send anything or reroute content. The owner must make that decision for each job; the bot never guesses. Resolving the oldest barrier permits the next same-channel job to become eligible.
5. Restart and watch classified failures/acknowledgments. New channels can progress during known local failures, but global/unknown failures still require investigation. Keep the old channel configured until its subscriptions and unresolved jobs are reconciled, then remove it from the configured channel list if desired.

Do not delete SQLite, edit nonces/First, mark unknown work delivered, or use a new database to clear a queue. No live state is changed by downloading this bundle.

### Worked example: retire an obsolete route
Stop the bot/service and back up the complete data directory, configuration and credential files privately. Keep existing state when upgrading. Removing a feed from YAML alone does not remove its SQLite subscription; changing destination creates a new pair. `yaml_feed=0` means a persisted pair is absent from current YAML, not disabled.

Inspect while stopped:

```sh
./bin/kallisti-rss -config config.yaml -subscriptions
./bin/kallisti-rss -config config.yaml -status
./bin/kallisti-rss -config config.yaml -outbox
```

Keep outbox output private: it contains original content, URLs and attachment keys. Verify the intended subscription ID and stored channel from fresh output. For example, **only if subscription 3 is the selected obsolete route in channel 42**:

```sh
./bin/kallisti-rss -config config.yaml -pause-subscription 3 -expected-channel 42
```

Pause is guarded, idempotent and offline; it preserves seen history and queued work, and survives YAML reseeding/restart. Recheck `-subscriptions` for `paused: true`. No remote channel is needed. The existing authorized chat `!feed resume ID` reverses a pause if that original channel is available/configured; there is no offline resume mode.

Inspect each pending/blocked job privately and reconcile any uncertain attempt with the actual destination. The resolution command has no channel guard: verify each job's channel before using its ID. Only mark delivered when remote delivery is confirmed; choose discarded only to intentionally abandon retries. For example, **after verifying job 12 is the intended job**:

```sh
./bin/kallisti-rss -config config.yaml -resolve-job 12 -resolution discarded
```

Discard preserves rows/payload/nonce/First/retry history and stops attempts. It neither proves non-delivery nor erases remote messages. No bulk purge, reroute or retry-now command exists. Outbox inspection is capped at 1000 rows. Pause/remove affects future polling only; reconcile existing work separately.

Recheck status/outbox and subscriptions before restarting your usual single process. Never reset the database, edit nonces/First or restore a stale backup to bypass an uncertain send. Keep configured channel IDs until their subscriptions and queued work are resolved. Backups predating delivery can reintroduce uncertainty and require reconciliation.

## Container builds and CI

Both deployment layouts use the root Alpine Dockerfile and UID/GID 65532, with a
read-only root, resource bounds and no inbound service. No public image is
assumed; build your own. The root `compose.yaml` remains compatible with existing installs using
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
