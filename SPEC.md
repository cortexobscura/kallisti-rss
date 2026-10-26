# kallisti-rss contract

Status: implemented review candidate; operator authorization policy is an explicit registered-user-ID allowlist. Real feed media compatibility requires operator smoke testing. Go, YAML seed configuration, SQLite, single replica; one Kallisti server and multiple text channels.

## Invariants
- No live credentials, messages, deployment or repository publication during development.
- Only explicitly configured verified registered account IDs may manage subscriptions; display names never authorize. Commands apply only to configured channels.
- Feed and media HTTP requests use verified TLS, bounded responses and deadlines, no ambient proxy, cookies or authorization. All resolved addresses and every redirect must be public. Connections pin validated IPs against DNS rebinding.
- Default initial successful fetch records entries without publishing backlog. Failed initial fetch never marks a subscription initialized.
- Entry identity includes subscription/channel. SQLite atomically records entries and outbound work. Outbound nonce is random, stable and globally unique across channels.
- A send is delivered only after a matching server nonce acknowledgment. Uncertain sends are retried only within 23 hours of their first attempt; older sends become blocked for operator reconciliation. No indefinite exactly-once promise. Server message deletion can also remove nonce protection inside the window.
- Raster image bytes and dimensions are bounded before decode; images are decoded and re-encoded as PNG to strip metadata. No SVG, video, arbitrary page scraping or private-network feeds in v1.
- YAML seeds missing subscriptions. Durable chat removals persist across restart; SQLite is authoritative after seeding.
- One OS file lock per database prevents two instances. Shutdown is cancellation-aware. Secrets never appear in logging.

## Architecture and limits
Serial polling/delivery loop, independent bounded WebSocket reader, durable subscription/seen/outbox/command tables. Default poll 5m, minimum 30s; <=100 subscriptions, <=1000 entries/feed, feed <=4MiB, media <=8MiB, <=16 million pixels, three images per post by default, 1..10 per YAML seed (further bounded by server ready limits). Text <=2000 Unicode runes. HTTPS feeds only; allow HTTP explicitly for public feeds if configured. Server is operator-trusted and fixed, wss/https required. No startup credential generation or signup. Existing account password or session token read from exactly one secret file; server Origin and username configured. Password login uses verified fixed-origin HTTPS, retains token only in memory, and never creates an account. Read-only -discover prints stable registered IDs and text-channel IDs without posting/polling/DB writes. Two servers require separate processes, credentials and databases.

Upstream baseline: kallisti d8fd12dd2a6f1483c8fdee187ebed827b3bd2efc (verify current fetched revision in PROTOCOL.md). Binary Mumble Version/Authenticate and JSON sidecar share /ws. Wait for ServerSync and ready. POST /api/upload using one-use sidecar upload-token, then send attachment key and stable nonce. Text-channel store ID is not a Mumble voice ID.

## Commands
`!feed list`, `!feed add URL`, `!feed remove ID`, `!feed pause ID`, `!feed resume ID`, `!feed help`. Every command is allowlisted and bounded; no arbitrary network fetch on reader goroutine. Commands are deduplicated by server message ID. Changes apply within current channel only. Status/replies contain no credentials. Unknown/unauthorized input cannot mutate state.

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

Readiness: the specified implementation and offline gates pass. This is not a deployment certification: local Docker build/demo and Compose validation pass, but hosted CI and actual upstream runtime interoperability remain unrun. Manager IDs are operator-provided, and real-feed access policies vary. See VERIFICATION.md for measured evidence and intentional limitations. The first-party license is The Unlicense, confirmed by the owner to match upstream preference.

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
