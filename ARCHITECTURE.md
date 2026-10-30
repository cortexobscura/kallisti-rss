# Current architecture

Evidence baseline: implementation commit `28f1a0d96a770a56a9d3cd1a9cdd1613a1afd45d`. This document describes the running design, not a proposed refactor. Statements are OBSERVED in the cited source unless marked EXTERNAL. Intended behavior belongs in [SPEC.md](SPEC.md); caveats and evidence gaps belong in [IMPLEMENTATION.md](IMPLEMENTATION.md).

## Process and module boundaries

One Go process serves one Kallisti server/account and one local SQLite database. There is one command package and one internal implementation package. Normal operation has no inbound listener, web UI, separate worker service or child process. Explicit demo mode starts a temporary loopback HTTP/WebSocket fixture.

| Source | Responsibility |
|---|---|
| [cmd/kallisti-rss/main.go](cmd/kallisti-rss/main.go) | CLI dispatch, startup, signals, reconnect loop and scheduling |
| [internal/bot/config.go](internal/bot/config.go) | Strict YAML loading, defaults, channel/manager authorization and poll intervals |
| [internal/bot/auth.go](internal/bot/auth.go) | Bounded secret reads, fixed-origin password login and session reuse |
| [internal/bot/transport.go](internal/bot/transport.go) | Mumble/JSON handshake, reader, serialized send/upload requests and acknowledgment matching |
| [internal/bot/fetch.go](internal/bot/fetch.go) | Public-only HTTP policy, DNS validation, pinned-IP dialing and response bounds |
| [internal/bot/feed.go](internal/bot/feed.go) | RSS/Atom parsing, identity/text construction, image extraction and raster normalization |
| [internal/bot/engine.go](internal/bot/engine.go) | Due-feed polling, image preparation, delivery orchestration and chat transactions |
| [internal/bot/store.go](internal/bot/store.go) | File lock, schema, identity binding, subscriptions, ingestion, queue selection and reconciliation |
| [internal/bot/delivery.go](internal/bot/delivery.go) | Closed rejection classifications, retry delays, cooldown handling and safe failure logging |
| [internal/bot/diagnostics.go](internal/bot/diagnostics.go) | Controlled feed errors, host/hash mapping and feed-only checks |
| [internal/bot/demo.go](internal/bot/demo.go) | Original synthetic fixture and end-to-end demo; compiled into the executable |

`Engine` holds a concrete `*Store`, `Config`, `Fetcher`, `Sender` and optional clock. `Fetcher` exposes `Fetch`; `Sender` exposes `Send` and `Upload`, with optional `MaxImages` capability. Tests replace these interfaces and the clock. Storage is not abstracted behind a repository interface: `Engine.Command` owns a SQL transaction through `Store.DB`. Parser, transport and database types live together in `internal/bot`; there is no separate domain/service layer or public library API.

[go.mod](go.mod) pins coder/websocket, protowire, yaml.v3, modernc SQLite, x/sys for Unix locks, x/net for HTML and x/image for WebP. There is no copied generated upstream protobuf implementation. Builds disable CGO; Unix file locking makes Windows support unestablished.

## Startup and concurrency

1. `run` parses flags and applies the dedicated offline-pause guard. Demo returns before loading config. Validation, feed checks and discovery return before opening SQLite.
2. Database modes open the store and bind `server-without-trailing-slash|username`. Inspection/pause/resolution return before YAML seeding or authentication.
3. Normal startup seeds missing subscriptions, constructs the fetcher/dialer/engine, then connects. Schema setup and seeding therefore precede registered bot-ID verification; they are not part of a rollback transaction with login.
4. The connection sends Mumble Version/Authenticate, requires ServerSync, a registered self UserState and sidecar `ready`, and checks configured channel visibility. `BindUser` then prevents sending an old queue under a different registered ID.
5. After connecting, the main goroutine polls due channels and drains work. It then handles command messages, a one-second poll ticker, a five-second delivery ticker, disconnects and cancellation. Polling, media processing, SQL mutations and delivery are serial in this goroutine.
6. Connection failures/disconnects wait 30 seconds before reconnecting. The same engine retains in-memory poll deadlines and cooldowns. SIGINT/SIGTERM cancel the context; the connection/tickers close and the store lock is released on return. This is not a guaranteed shutdown-duration bound.

A separate WebSocket reader forwards live command candidates into a 256-message queue and RPC events into a 32-event queue. It ignores unrelated binary events after handshake and does not fetch feeds or mutate subscriptions. Queue overflow closes the connection instead of blocking indefinitely. History/edit events and the bot's own messages do not execute commands; there is no outage-history replay.

`Connection` serializes send and upload-token operations under a mutex because token replies lack request IDs. RPC requests have a 20-second context and at least 250 ms spacing. Send completion matches the immutable job nonce. Timeout closes the connection and leaves delivery uncertain. Upload POSTs use a separate bounded HTTP request on the fixed server client. A successful delivery is followed by a cancellation-aware 300 ms pause; each `Drain` pass handles at most 100 loop iterations. Slow serial work can delay commands and scheduled polls.

## State ownership

`OpenStore` takes a nonblocking advisory `flock` on `<database>.lock`, opens/creates the database, sets one SQL connection and enables WAL, FULL synchronous writes and a 5000 ms busy timeout. Newly created database/lock files use mode 0600. Existing permissions and other writers are the operator's responsibility.

| Store | Authoritative content and lifecycle |
|---|---|
| YAML | Server/auth settings, manager/channel lists, timing and missing subscription seeds. Never rewritten; omission does not delete persisted subscriptions |
| `meta` | Server/account-name identity and registered bot ID, recorded on first binding and checked thereafter |
| `subscriptions` | Unique exact `(channel,url)` pair, image cap, initialized/paused/deleted flags; deletion is a retained tombstone |
| `seen` | Primary key `(sub,item)`; retained entry identities for independent subscription/channel deduplication |
| `outbox` | Unique random nonce, destination, serialized payload, first-attempt marker and pending/blocked/delivered/discarded state |
| `commands` | Primary-key server message ID; committed with mutation and reply job to prevent duplicate handling |
| `delivery_retry` | Per-job attempts, next eligible Unix time and controlled scope/class/stage; terminal-job history remains stored |
| Engine memory | Per-channel next poll, 30-second subscription-read cooldown and fallback global cooldown; process restart clears these |
| Dialer memory | Cached password-login token with a 29-day reconnect refresh deadline; no token persistence |
| One poll batch | At most eight shared parsed results/failures, released after last use; no cross-cycle feed cache |

Schema setup uses additive `CREATE TABLE/INDEX IF NOT EXISTS`; there is no schema-version ledger, migration runner, foreign-key cascade or retention job. Tests cover adding the retry table to an existing compatible layout, not arbitrary historical versions or downgrades. SQLite rows and backup files are not encrypted.

## Poll and ingestion flow

`PollDue` reads nondeleted subscriptions in ID order, skips paused ones, and selects channels whose in-memory deadline is due. Identical exact URLs can share a fetch/parse result within the eight-result cache; channels not due do not ingest that result. A completed due batch schedules the next attempt from completion time, including failed feeds. Subscription-read failure waits 30 seconds.

The public fetcher validates the URL, validates every resolved address and dials the first validated literal address while retaining the hostname for TLS. It rejects ambient proxies and uses no cookies or server credentials. Each followed redirect is checked again. [SECURITY.md](SECURITY.md) and the actual address list in `fetch.go` define this boundary.

The parser accepts RSS/RDF/Atom roots, rejects XML directives, limits depth to 64 and entries to 1000, and bounds the feed to 4 MiB. Entry identity prefers Atom ID, then GUID, then link, then a title/summary digest; persisted IDs are hashed. Titles and summaries become bounded plain text with `@` neutralized; links remain visible. Images come only from explicit feed/HTML references, not linked-page scraping.

`Ingest` rereads subscription flags inside a transaction. A first successful snapshot, even empty, records seen IDs and initialization but queues nothing. Later unseen entries append jobs with independent random 24-byte nonces encoded as 48 hex characters. It iterates the parsed entries in reverse; it does not sort publication timestamps. Seen updates, new jobs and initialization commit together. Capacity failure rolls the transaction back.

## Preparation, sending and recovery

Before the first attempt, `Drain` can fetch selected image candidates, validate PNG/JPEG/GIF/WebP dimensions, re-encode PNG and upload successful results. Inputs and normalized outputs are bounded to 8 MiB; each dimension is at most 8192 and total pixels at most 16 million. The seed cap and server `maxUploads` further limit count. Individual image failures skip that image and can leave a text-only payload.

Prepared attachment keys and preparation time are saved while `first=0`. Preparation older than nine minutes, or dated in the future, can be refreshed before the first attempt. `Prepare` persists the first-attempt marker before calling `Send`; it is evidence of intent to attempt, not proof that bytes reached the server. Once set, the production path no longer changes payload, upload keys, nonce or destination.

`NextReady` selects the earliest eligible pending job with no older pending/blocked predecessor in its channel and no active persisted global cooldown. A nonce-matched acknowledgment followed by successful `Delivered` persistence completes delivery. An acknowledgment without a successful database commit leaves the job pending and stops the pass globally.

Only exact, nonce-correlated channel/attachment rejections or fully validated slow mode isolate one destination. Unknown, connection, account/rate, policy and storage failures remain global. Retry metadata is durable when its transaction succeeds; otherwise a logged 30-second memory-only global cooldown stops sending. Backoff starts at 30 seconds, doubles to 15 minutes, and can honor validated slow mode up to one hour. Eligibility never extends the 23-hour first-attempt horizon. A refused old/backwards-clock attempt becomes blocked, preserving its channel barrier.

EXTERNAL: upstream acknowledgment correctness, 24-hour nonce lookup and public-by-key attachment lifecycle come from the pinned source contract in [PROTOCOL.md](PROTOCOL.md). Local fixtures model those semantics but do not run the upstream server or prove exactly-once delivery.

## Management and trust boundaries

The server is trusted for identity and acknowledgment assertions; verified TLS authenticates that fixed origin. Passwords go only to `/api/login`; session bearer tokens go only to `/ws`; one-use upload tokens go only to `/api/upload`. Server redirects are refused. Token files are reread on reconnect; password sessions are reused in memory rather than automatically replaced after every rejection.

`Config.Authorized` requires a present registered uint32 ID, `verified=true`, membership in `admin_ids` and a configured destination. `Engine.Command` also bounds text, requires a positive message ID and the `!feed ` prefix. The replay marker, channel-scoped SQL change and queued reply commit atomically. Add validates URL syntax/policy without fetching on the reader; DNS/public-address validation occurs on later fetch. Pause/remove never remove existing outbox work.

Logs expose controlled diagnostics, IDs, counts and feed host plus a short URL hash, not raw remote errors or full URLs. This is not a universal output-redaction claim: `-outbox` includes original payloads/keys and chat `list` includes full subscription URLs. Protect these separately. There are no metrics, health/readiness HTTP endpoints or telemetry exporter.

## Packaging and operation

[Makefile](Makefile) builds the primary and compatibility binaries and supplies the local acceptance interface. [Dockerfile](Dockerfile) uses a Go 1.27.3 Alpine 3.24 builder and Alpine 3.24 runtime with CA certificates, UID/GID 65532 and notices under `/usr/share/doc/kallisti-rss/`. Runtime paths remain `/feedbot`, `/data` and `/config/config.yaml`.

[compose.yaml](compose.yaml) supplies one `feedbot` service with entrypoint `/feedbot -config /config/config.yaml`, so `docker compose run` arguments append action flags: read-only root, no capabilities, no-new-privileges, 64 PIDs, 512 MiB RAM, one CPU, a 32 MiB `/tmp`, private operator-provided mounts and no published ports. The image has no operational config or credentials. Production mount permissions and runtime connectivity require separate operator verification.

The source-only [.dockerignore](.dockerignore) is tested with synthetic decoy secrets and Docker's actual context export. The [GitHub workflow](.github/workflows/ci.yml) runs the same release checks, packaging regressions, config/binary checks, Compose validation and hardened offline container demo. It has read-only repository permissions, pinned actions and no publish/deploy step. Images use version tags rather than digest pins; reproducible image bytes are not promised.

Deployment templates also live in [deploy/docker](deploy/docker/README.md) and
[deploy/kubernetes](deploy/kubernetes/README.md). The Docker layout builds the
root Dockerfile and preserves the same runtime paths; the root Compose file
remains compatible with existing installs. Kubernetes uses a single-replica
Recreate Deployment, a ReadWriteOncePod PVC, generated configuration and an
operator-managed Secret. No runtime listener or health probe is added.
`make check-deploy` validates example configs, Compose and Kubernetes schemas;
`make check-compose` runs the Docker layout offline against a prebuilt image;
cluster storage, permissions and interoperability still need operator testing.
