# kallisti-rss contract

Status: IMPLEMENTATION READY for maintenance of the existing system. This reverse blueprint preserves the established contract; it does not approve a new architecture or certify a deployment. Operator authorization uses an explicit registered-user-ID allowlist. Real feed media compatibility requires operator smoke testing. Go, YAML seed configuration, SQLite, single replica; one Kallisti server and multiple text channels.

## Purpose and document authority

The bot republishes new public RSS/Atom entries, with bounded image attachments, into configured Kallisti text channels. Operators own credentials, configuration and recovery; verified allowlisted managers manage subscriptions in chat; channel readers receive posts. The supported workload is serial operation within the limits below, not a throughput or latency guarantee.

This file defines intended behavior. [PROTOCOL.md](PROTOCOL.md) defines the pinned external wire contract; [SECURITY.md](SECURITY.md) defines trust boundaries. [ARCHITECTURE.md](ARCHITECTURE.md) describes current wiring and state ownership. [IMPLEMENTATION.md](IMPLEMENTATION.md) separates observed behavior, deviations and uncertainty from requirements. [VERIFICATION.md](VERIFICATION.md) records dated checks, not new requirements. Do not silently convert a quirk in the implementation notes into a product requirement.

## Invariants
- Development uses original offline fixtures. Live credential use, credential provisioning, messages, deployment and repository publication require explicit owner instruction.
- Only explicitly configured verified registered account IDs may manage subscriptions; display names never authorize. Commands apply only to configured channels.
- Feed and media requests use bounded responses and deadlines, no ambient proxy, cookies or authorization. HTTPS always verifies TLS; the explicit public-HTTP opt-in is the only plaintext exception. All resolved addresses and every redirect must be public. Connections pin validated IPs against DNS rebinding.
- Default initial successful fetch records entries without publishing backlog. Failed initial fetch never marks a subscription initialized.
- Entry identity includes subscription/channel. SQLite atomically records entries and outbound work. Outbound nonce is random, stable and globally unique across channels.
- A send is delivered only after a matching server nonce acknowledgment. Uncertain sends are retried only within 23 hours of their first attempt; older sends become blocked for operator reconciliation. No indefinite exactly-once promise. Server message deletion can also remove nonce protection inside the window.
- Raster image bytes and dimensions are bounded before decode; images are decoded and re-encoded as PNG to strip metadata. No SVG, video, arbitrary page scraping or private-network feeds in v1.
- YAML seeds missing subscriptions. Durable chat removals persist across restart; SQLite is authoritative after seeding.
- One OS file lock per database prevents two instances. Shutdown is cancellation-aware. Secrets never appear in logging.

## Architecture and limits
Serial polling/delivery loop, independent bounded WebSocket reader, durable subscription/seen/outbox/command tables. Default poll 5m, minimum 30s; <=100 subscriptions, <=1000 entries/feed, <=25 newly queued posts per subscription per poll, <=1000 unresolved jobs per channel, feed <=4MiB, media <=8MiB, <=16 million pixels, three images per post by default, 1..10 per YAML seed (further bounded by server ready limits). Text <=2000 Unicode runes. HTTPS feeds only; allow HTTP explicitly for public feeds if configured. Server is operator-trusted and fixed, wss/https required. No startup credential generation or signup. Existing account password or session token read from exactly one secret file; server Origin and username configured. Password login uses verified fixed-origin HTTPS, retains token only in memory, and never creates an account. Read-only -discover prints stable registered IDs and text-channel IDs without posting/polling/DB writes. Two servers require separate processes, credentials and databases.

Upstream baseline: kallisti d8fd12dd2a6f1483c8fdee187ebed827b3bd2efc (verify current fetched revision in PROTOCOL.md). Binary Mumble Version/Authenticate and JSON sidecar share /ws. Wait for ServerSync and ready. POST /api/upload using one-use sidecar upload-token, then send attachment key and stable nonce. Text-channel store ID is not a Mumble voice ID.

## Commands
`!feed list`, `!feed add URL`, `!feed remove ID`, `!feed pause ID`, `!feed resume ID`, `!feed help`. Every command is allowlisted and bounded; no arbitrary network fetch on reader goroutine. The reader authorizes before queueing, so unauthorized input cannot fill the command queue; when allowlisted commands overflow it, the excess is dropped with a warning instead of closing the connection. Commands are deduplicated by server message ID. Changes apply within current channel only. Status/replies contain no credentials. Unknown/unauthorized input cannot mutate state.

## Acceptance tests
1. Credential-free demo uses actual parser, media validator, SQLite and delivery engine; first fetch emits nothing; new item emits once; restart emits nothing extra.
2. RSS, Atom and representative Reddit/WordPress HTML expose title, link, plain summary and explicit image URLs; gallery-only pages remain text/link posts.
3. Private IPv4/IPv6, mapped addresses, loopback redirects, userinfo, invalid schemes, excessive XML/HTML/media, malformed raster and oversized dimensions are rejected.
4. Delivery failure retains durable nonce/payload; retry acknowledgment commits delivery; stale uncertainty blocks. Multiple channels receive independently.
5. SQLite restart, channel-scoped mutation, duplicate commands, unauthorized display-name impersonation, and second process locking are tested.
6. Wire fixture exercises binary handshake, ready, nonce-correlated message/errors, upload-token, raw upload and incoming commands without credentials.
7. Run formatting, vet, unit/integration tests, race tests, vulnerability/security checks and build. Report individual outcomes; no percentage substitutes for scenario coverage.
8. Docker/Compose and GitHub Actions definitions provided; no Gitea workflow is supplied. Verify Git exclusions and Docker build inputs with synthetic files before building. Mark container runtime testing unrun if Docker unavailable, and hosted CI unrun until executed. No release claim without live interoperability smoke test.

## Operations / non-goals
Back up stopped database plus configuration; preserve server identity and account with outbox. No multiple replicas, auto signup, native Murmur fallback, scraping galleries or video. Seen IDs retained for durable dedup; disk growth is monitored by operator and documented. Pending work is bounded. Do not silently discard delivery errors. Provide source archive, fixture demo, operator quickstart, verification report and license/dependency notices.

## Review reconciliation
Independent source review identified and fixed real string-valued error envelopes, interleaved raw voice frames during handshake, and expired uploads after a pre-send crash. Regressions cover each. Before any first send, upload preparation may refresh after nine minutes; after first attempt the payload is immutable. Offline operator tools inspect and resolve pending/blocked jobs as delivered/discarded, never automatically replay them.

Readiness: core contracts support incremental implementation, but this is not a ONE-SHOT READY replacement specification or deployment certification. Mixed CLI actions and the general database upgrade/downgrade envelope are not defined as supported contracts; current behavior and test gaps remain explicit in IMPLEMENTATION.md. Local Docker build/demo, Compose validation and hosted CI for implementation commit `28f1a0d96a770a56a9d3cd1a9cdd1613a1afd45d` pass. A recorded actual upstream runtime acceptance run remains unrun. Manager IDs are operator-provided, and real-feed access policies vary. See VERIFICATION.md for measured evidence and intentional limitations. The first-party license is The Unlicense, confirmed by the owner to match upstream preference.

## Diagnostics contract
Feed errors expose only controlled error classes/reasons, numeric HTTP status, durable subscription/channel ID, current YAML index and host plus hashed URL key. No URL paths/query strings, raw network errors or bodies are logged. Read-only `-check-feeds` bypasses authentication/media/SQLite and reports each configured fetch/parser result; `-subscriptions` provides an offline sanitized durable mapping. Failures leave baseline/seen state unchanged and do not stop other feed polls; cycle summaries report progress.


## Delivery isolation invariants
- A nonce-correlated, explicitly recognized channel/job-local rejection may defer that channel while others progress. Unknown/global/connection/storage errors stop the pass; never infer locality from every server error.
- Pending and quarantined earlier jobs remain ordering barriers within their channel. No failed job is skipped within its own destination.
- Backoff metadata is separate and durable (30s exponential, cap15m, slow-mode up to1h), preserving payload, nonce, First, destination and state. Additive idempotent schema creation supports existing v2 databases. No automatic removal/rerouting/resolution.
- Global retry cooldown survives restart when storage succeeds; storage failure uses logged memory-only cooldown and stops all sends. No warning on ordinary cooldown ticks.
- Only an acknowledged send followed by a committed delivered-state update emits delivery success. Raw errors, request text and secrets never enter failure logs.
- Legacy YAML routes remain explicit operator reconciliation work; their queue is not automatically rewritten.

## Guarded offline pause
`-pause-subscription ID -expected-channel CHANNEL` pauses exactly one active subscription under the existing exclusive database lock, with positive ID/channel guards and incompatible-flag rejection. It makes no network requests, preserves seen/outbox/retry history and does not seed YAML. Repeat pause is idempotent; restart/reseeding preserves paused state. No bulk deletion, rerouting or automatic queue resolution.

## Channel polling extension
Additive `channel_poll_intervals` maps configured channel store IDs to 30s..24h durations, overriding global `poll_interval`. Independent in-memory deadlines survive reconnects; process startup polls immediately without resetting durable baseline or paused state. Only due channels ingest shared bounded fetch results; subscription order and per-subscription seen state remain independent. At most eight parsed results are cached within a batch; no stale cross-cycle cache. Failures wait the channel interval, database reads cool down 30s. Delivery timers/pacing/durable queue semantics remain separate.

Image-stage logs use controlled counts and classes only; acknowledged attachment counts require both a matching server acknowledgment and committed delivered state. Baseline extraction counts are not delivery proof.

## Configuration and executable surfaces

The executable is `bin/kallisti-rss`; `make build` also produces the byte-identical `bin/kallisti-feedbot` compatibility name. Linux and macOS are the documented native targets; the OS file lock uses Unix APIs. Production exposes no inbound HTTP service or UI. The loopback server belongs only to explicit demo mode and tests.

YAML is one document, at most 64 KiB, with unknown fields rejected. There are no application environment-variable overrides or live reload. `-config` defaults to `config.yaml`; relative file paths resolve from the process working directory. [config.example.yaml](config.example.yaml) is the editable example, not operational configuration.

| Field | Contract |
|---|---|
| `server`, `origin` | Required HTTPS origins; no userinfo, query, fragment or non-root path |
| `username`, `password_file`, `token_file` | Nonempty username, at most 128 bytes; exactly one secret-file path. Validation does not read the secret or prove authentication |
| `database` | Defaults to `feedbot.db`; one cooperative process on a local filesystem |
| `channels`, `admin_ids` | 1..100 unique positive channel store IDs in normal mode; registered uint32 manager IDs, empty by default. Discovery can omit channels |
| `poll_interval`, `channel_poll_intervals` | Global default 5m; overrides name configured channels; all effective intervals 30s..24h |
| `feeds` | At most 100 YAML seeds; each names a configured channel and validated public-feed URL. Active persisted subscriptions also have a total cap of 100 |
| `feeds[].max_images` | 0 means default 3; explicit values 1..10; only applied when a missing seed is created |
| `allow_http_feeds` | False by default. Opt-in public HTTP does not permit private destinations, credentials or access-control bypasses |

Use one action at a time; do not rely on accidental precedence between combined action flags.

| Action | Effects and boundary |
|---|---|
| No action flag | Validate config, open/bind/seed SQLite, authenticate, bind registered bot ID, then poll, deliver and handle commands |
| `-demo` | Ignore real config; use a temporary database, synthetic feed/media and an ephemeral loopback protocol fixture |
| `-check-config` | Validate YAML without networking, database access or credential-file reads |
| `-check-feeds` | Fetch/parse YAML feeds only; no authentication, media or database access. Does not inspect chat-added subscriptions |
| `-discover` | Authenticate and print initial online registered users and visible text channels as JSON; no feed polling, posting or database access |
| `-status` | Offline queue counts for pending, blocked and delivered jobs |
| `-subscriptions` | Offline sanitized JSON mapping of nondeleted subscriptions to current YAML, including paused state |
| `-outbox` | Offline JSON for up to 1000 pending/blocked jobs in ID order; contains original payloads and attachment keys, so keep private |
| `-resolve-job ID -resolution delivered\|discarded` | Offline manual terminal-state update for exactly one unresolved job; no channel guard, retry, remote deletion or send |
| `-pause-subscription ID -expected-channel CHANNEL` | Guarded offline pause as specified above |

All offline database modes take the exclusive lock and bind server/account identity before acting. They do not seed YAML or read credentials, but opening the store can create files, initialize schema and write identity metadata. They are not filesystem-read-only inspection tools. Stop the bot first and verify the configured database path.

## State lifecycle and recovery

Subscription identity is the exact `(channel, URL)` pair. YAML omission is not deletion or a routing policy: persisted subscriptions and queued destinations remain until explicitly managed. A new subscription records its first successfully parsed snapshot, including a valid empty snapshot, without backlog. A failed fetch, parse or ingestion transaction cannot advance that baseline. A restored or resumed subscription retains seen history; unseen entries still in its next snapshot may therefore be queued.

Ingestion records seen IDs and new jobs atomically. Chat mutation, replay marker and reply job also commit together. At 10,000 pending/blocked jobs, transactions requiring further capacity fail without committing their new work. One poll queues at most 25 new posts per subscription, and a channel with 1,000 unresolved jobs queues none; the excess entries stay unseen and are reconsidered oldest-first on later polls while still in the feed, with a `feed entries deferred` warning. Entries that leave the feed while deferred are not posted. These limits keep one feed from flooding its channel or exhausting the shared queue for other channels. Pausing, removing or changing YAML does not erase queued jobs. Rows in `seen`, `commands`, terminal outbox states and retry history are retained; there is no pruning or automatic repair.

Persisted jobs progress from `pending` to `delivered` only after matching acknowledgment and a successful local commit, or to `blocked` when the retry horizon/clock guard refuses another attempt. An operator may resolve pending/blocked work as `delivered` or `discarded` after reconciliation. Discard means stop retrying, not prove non-delivery. Retrying never changes an attempted job's destination, payload, nonce or first-attempt marker.

Back up while stopped: preserve the whole data directory, any WAL/SHM files, config and securely managed credential files. Restore with the same server/account/registered identity and permissions. Old backups can reintroduce uncertainty; reconcile before reconnecting. There is no automatic backup/restore command or proof that a snapshot matches remote delivery history. See [OPERATIONS.md](OPERATIONS.md).

## Build, fixtures and completion contract

Use Go 1.27.3 or newer with the pinned module graph. The CGO-free binary embeds the pure-Go SQLite driver; normal operation still needs config, a secret file, writable local storage, CA trust and the external Kallisti/feed services. Initial builds/scans can download toolchains, modules and scanner data. Runtime tests use synthetic fixtures, not production accounts.

`make release-check` comprises `make check`, `make serve-demo`, `make fuzz` and `make security`. These run formatting verification, vet, race/unit/integration tests, native build, credential-free demo, ten-second parser fuzzing, govulncheck and gosec. Packaging additionally runs both scripts under `scripts/`, example-config validation, compatibility-binary comparison, Compose validation and an Alpine build with the hardened network-disabled demo. GitHub CI executes these underlying checks; it does not publish or deploy. Docker builder and runtime use Alpine 3.24, preserving UID/GID 65532, CA certificates and license notices.

A change is complete only when its affected acceptance scenarios and the required offline gates pass, with failed/unrun checks reported separately. Keep tests for authorization rejection, transaction rollback, no backlog, immutable retries, ordering barriers, restart and cancellation. A passing suite, coverage figure or owner report does not establish upstream interoperability. Release/deployment verification additionally requires the recorded dedicated-channel workflow in README, explicit owner authorization and production mount-permission checks. Preserve original fixtures and third-party notices; do not weaken invariants to make a check pass.


## Deployment templates

`deploy/docker` and `deploy/kubernetes` supply operator-facing installation and
recovery instructions with container-path configuration examples. The root
Compose entry point remains supported. Kubernetes is single-replica with
Recreate updates and exclusive persistent storage suitable for SQLite; it adds
no application networking, health endpoint or multi-replica support. Credentials
remain externally provisioned. `make check-deploy` validates template rendering
and schemas, not a live cluster or upstream installation.
