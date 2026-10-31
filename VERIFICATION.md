# Verification report — 2026-10-26

## Live check of v0.1.0-rc.4: plain-container run — 2026-10-31

The owner ran the `kallisti-rss:v0.1.0-rc.4` image with plain `docker run`
instead of Compose, using the live config, password file and data directory
from `deploy/docker`. The flags reproduced the Compose hardening: `--init`,
`--user 65532:65532`, `--read-only`, `--cap-drop=ALL`,
`--security-opt=no-new-privileges:true`, `--pids-limit=64`, `--memory=512m`,
`--cpus=1`, a 32 MiB `/tmp` tmpfs and `--stop-timeout 30`. Config and secret
were bind-mounted read-only and the image was given `-config
/config/config.yaml` directly. No restart policy was set.

Observed:

- 16:16:31 (container log time): `feed poll cycle completed succeeded=2
  failed=0`.
- Jobs 45 to 67 logged `delivery acknowledged`. Jobs 57 and 60 logged
  `image skipped before first send` at stage `image_decode` and were sent
  text-only; job 64 carried 3 attachments.
- A second process against the same data directory stopped with
  `database already in use`.
- `docker inspect` reported `user=65532:65532 ro=true capdrop=[ALL]
  [no-new-privileges:true]`.
- `docker stop` returned in 0.248 s with exit code 0.

The Compose container had already been removed before the run, so the bot had
been down for an unknown period. The 23 posts are taken to be entries published
during that gap, all within the 25-per-poll limit; this was inferred from the
new job IDs and was not checked against the feeds. No duplicate post was
reported.

Afterwards the bot was first started with `docker compose up` from the
repository root. That used the root `compose.yaml` (image `kallisti-rss:local`,
mount source `./config.container.yaml`). The source did not exist, so Docker
created it as an empty directory and the bot restart-looped on
`read /config/config.yaml: is a directory`, failing before opening the
database. The owner removed that container and directory and started the
`deploy/docker` service; at 20:05:43 it logged `succeeded=2 failed=0` on
`kallisti-rss:v0.1.0-rc.4`. Both Compose files use the project name
`kallisti-rss`, which makes the two easy to confuse.

With this run, every step of the README live acceptance procedure has been
run at least once on rc.1 through rc.4. The rc.4 `!feed add` replies have
still not been reported from live use.

## Live check of v0.1.0-rc.4: lost acknowledgment and blocked recovery — 2026-10-30

The bot was switched from rc.3 to `v0.1.0-rc.4` (signed tag on `c3f1ad7`, image
built locally from `git archive` of the tag) on the same server, account,
channel and database. Before the switch, offline `-status` showed
`pending=0 blocked=0 delivered=32`; the stopped data directory was backed up.
The first cycle completed with `succeeded=2 failed=0`, both subscriptions
`queued=0`.

### Lost acknowledgment during a send

To lose an acknowledgment after the server accepted a post, the owner delayed
all traffic into the bot container by 30 seconds with
`tc qdisc add dev <host veth> root netem delay 30s`, longer than the 20-second
acknowledgment timeout. Outgoing traffic was not delayed. The owner then sent
`!feed list`. Observed in the container logs:

- 18:47:19 UTC: job 34 (the reply) logged `delivery failed; retry scheduled`
  with `error_class=ack_timeout`, `failed_attempts=1`, `retry_state=durable`.
  The job stayed pending with its payload and nonce.
- 18:47:59: one reconnect attempt failed while the delay was still active.
- The owner removed the delay. 18:48:30: the bot reconnected, resent job 34
  with the same nonce and logged `delivery acknowledged job=34`.
- The owner reported a normal delivery with no duplicate reply in the
  channel: the server matched the retried nonce to the message it had
  already posted.

Side effect: the delay also slowed DNS replies, so the 18:46 poll failed both
feeds with `dns_error`; they retry on the next poll. The container did not
restart.

### Blocked-job recovery drill

A job becomes blocked only after an uncertain send ages past the 23-hour
retry horizon or the clock moves backwards. Neither was practical to produce on
the live bot, and editing the live database is against the operating rules. The
owner instead ran the operator recovery offline on a scratch copy of the rc.3
backup, using the rc.4 image with `--network=none` and the production config:

- `sqlite3` set job 32 to `state='blocked'` in the copy.
- `-status`: `pending=0 blocked=1 delivered=31`.
- `-resolve-job 32 -resolution delivered`: no output.
- `-status`: `pending=0 blocked=0 delivered=32`.

The `-outbox` step printed nothing because the suggested `grep` used
`"state"`; the field is serialized as `"State"`. The transition into `blocked`
is covered by unit tests, not by this drill. The scratch copy was deleted.

### Acceptance status

Every step of the README live acceptance procedure has now been run at least
once on rc.1 through rc.4, except a plain-container run outside Compose. The
owner later clarified that the test server runs Kallisti `v0.11.0`
(`1c201c1`), not 0.10.0 as recorded in the rc.3 entry; the server exposes no
build commit, so which checks ran on which build was not recorded. The rc.4 `!feed add`
replies have not been reported from live use yet.

## Live check of v0.1.0-rc.3 and add replies — 2026-10-30

### Owner-run live check of rc.3

The bot was switched from rc.2 to `v0.1.0-rc.3` (signed tag on `78b9f07`, image
built locally from `git archive` of the tag) on the same server, account,
channel and database. Before the switch, offline `-status` showed
`pending=0 blocked=0 delivered=21`; the stopped data directory was backed up.

The owner reports the server runs Kallisti v0.10.0. Upstream sets that version
string at `7428e2d` and keeps it through the pinned `d8fd12d` and at least 228
later commits, so it does not identify the exact server revision.

Observed, from the container logs (controlled fields only) and owner chat
transcripts:

- Restart onto the new version: the first cycle completed with
  `succeeded=2 failed=0`, both subscriptions `queued=0`.
- Repeated `!feed pause` and `!feed resume` replied that the subscription was
  already in that state.
- `!feed remove 1` replied `Subscription 1 removed. ...`, `!feed list` no
  longer showed it, and a second `!feed remove 1` replied
  `No matching subscription in this channel.`
- `!feed add` with the same URL restored it under ID 1. The next poll recorded
  no new baseline (`baseline_recorded=false`) and queued nothing from the 20
  feed entries, so seen history survived and nothing old was re-posted.

Still not exercised: disconnect during a send with nonce reconciliation and
blocked-job recovery.

### Add replies, fixed after rc.3

During the remove test, `!feed add 1` replied only `Feed URL rejected.`, and a
successful add replied with the same generic text whether it created a
subscription, restored a removed one or found it already active. Add of a paused
subscription also silently resumed it. Add now:

- explains that it takes a feed URL, not an ID, when given a number;
- replies `Subscription N added. ...` or `Subscription N restored ...` with
  the ID;
- leaves an existing active subscription unchanged, paused or not, and says so,
  pointing to `!feed resume N` when it is paused;
- checks for an existing subscription before the 100-subscription limit, so
  re-adding one at the limit is not a limit error.

## Live check of v0.1.0-rc.2 and pause/resume replies — 2026-10-30

### Owner-run live check of rc.2

The bot was switched from rc.1 to `v0.1.0-rc.2` (signed tag on `c768f4c`, image
built locally from `git archive` of that commit) on the same server, account,
channel and database. Before the switch, offline `-status` showed
`pending=0 blocked=0 delivered=12`. The stopped data directory was backed up,
and the service restarted with `docker compose up -d --no-build`.

Observed, from the container logs (controlled fields only) and owner reports:

- Restart onto the new version: the first cycle completed with
  `succeeded=2 failed=0`, and both subscriptions reported `queued=0`. Nothing
  was re-posted.
- A newly added subscription recorded its baseline without posting old entries.
- An Atom feed polled and delivered.
- Chat `!feed pause` and `!feed resume` changed the subscription state.

Still not exercised: chat `remove`, disconnect during a send with nonce
reconciliation, blocked-job recovery, and recording the server revision.

### Repeated pause/resume replies, fixed after rc.2

The owner noted that pausing a paused subscription, or resuming an active one,
replied `Subscription updated.` although nothing changed. The update matched
any active row regardless of its current state. The command now reads the
subscription's state inside the same transaction and replies
`Subscription N is already paused; nothing changed.` or
`Subscription N is already active; nothing changed.` without writing. Real
changes now name the action (`paused`, `resumed`, `removed`). Channel scoping,
command deduplication and rollback are unchanged.

## Live check of v0.1.0-rc.1 and rc.2 fixes — 2026-10-30

### Partial owner-run live check

The owner ran `v0.1.0-rc.1` (signed tag on `241dcb6`, image built locally from
`git archive` of the tag) with `deploy/docker/compose.yaml` against their own
Kallisti server in a dedicated `#feed-bot-testbed` channel. This is the first
recorded run against a real server. The server's Kallisti revision was not
recorded.

Observed, from the container logs (controlled fields only) and owner reports:

- `-discover` authenticated and returned the bot, text-channel and online
  registered IDs, confirming password login, TLS and the Origin policy.
- Two subscriptions (piaille.fr and soc.klan5.am, both RSS) polled across two
  cycles with no failures.
- Six new entries were queued and all six were acknowledged by nonce and
  committed (`delivery acknowledged`). Five carried their image. One image
  exceeded the 8 MiB media limit (`response_too_large`), and that post was sent
  text-only with its link, as designed.
- An allowlisted `!feed list` produced the expected reply listing both
  subscriptions (owner screenshot); the same command from a non-admin account
  produced no reply.

Not yet exercised, per the README acceptance procedure: observing the
no-backlog baseline (both subscriptions were already initialized in the logged
run), an Atom item, chat add/pause/resume/remove, restart without duplicates,
disconnect during a send with nonce reconciliation, blocked-job recovery, and
recording the server revision.

### First-start problems found during the run, fixed for rc.2

- The owner's config came from the repository-root example, so
  `password_file: ./secrets/password` resolved to `/data/secrets/password` in the
  container and startup failed with a bare `cannot read configured secret file`.
  Secret-file errors now name the configured path and, when relative, the
  resolved path, never the contents (`TestSecretErrorsNamePathNotContents`).
- `deploy/docker` defaulted to Compose project name `docker`, which collided
  with another project kept in a `deploy/docker` directory and reported its
  container as an orphan. The file now sets `name: kallisti-rss`, and the guide
  warns against `--remove-orphans` and explains removing an older copy's bot
  container.
- With that setup running, `deploy/docker/data` (and an empty root `data/` from
  an earlier root Compose start) is owned by UID 65532 and unreadable to the
  developer. Every `./...` Go command failed with `open data: permission
  denied`; the Makefile now targets `./cmd/... ./internal/...`.
- The same directories broke `make docker` and `docker compose build`. Bisecting
  with synthetic unreadable directories showed that BuildKit walks excluded
  directories when the Dockerfile copies `.` and `.dockerignore` re-includes a
  wildcard inside a subdirectory (`!sub/*.go`); adding explicit `data/` or
  `deploy/` exclusions did not help. The Dockerfile now copies only `cmd/` and
  `internal/` after the module files, so BuildKit walks just those paths;
  `.dockerignore` still filters them. CI now builds the image and runs
  `make vet` with both directories present and mode 000.

Local Linux results for the rc.2 changes, run with the owner's live
`deploy/docker/data` and root `data/` present:

- New `TestSecretErrorsNamePathNotContents`: PASS (configured and resolved path
  present; file contents absent).
- `make check`, `make serve-demo`, `make fuzz`, `make check-deploy`: PASS;
  coverage 38.7%/84.2%, demo pending=0 blocked=0 delivered=2, 112,221 fuzz
  executions. Before the Makefile change, `go list ./...` failed in this tree.
- `make docker`, `docker compose build` and `make check-compose`: PASS in the
  real tree; the previous Dockerfile failed with `open data: permission denied`.
  A scratch copy with both directories at mode 000 reproduced the CI step
  (image build plus `make vet`): PASS.
- `make security` with `GOGC=20 GOMEMLIMIT=700MiB GOMAXPROCS=2`: PASS; no called
  vulnerabilities, gosec zero issues with five existing suppressions.
- actionlint v1.7.12, shellcheck, both release-input scripts and
  `git diff --check`: PASS.

UNRUN: hosted CI until the pull request runs; the remaining live acceptance
steps listed above.

## Kubernetes portable defaults — 2026-10-30

The Kubernetes template previously required a CSI driver supporting
`ReadWriteOncePod` and named no storage class. Non-CSI node-local provisioners
such as Rancher local-path (used by the owner's target cluster, whose only app
class `local-path-retain` is not the default) cannot provide that access mode,
and a claim without a class stays `Pending` where there is no default. The PVC
now uses `ReadWriteOnce` with a documented `storageClassName` placeholder; one
writer remains enforced by the single replica, `Recreate` and the database file
lock. The namespace enforces the restricted Pod Security Standard. An optional
`components/egress-public` Kustomize component limits egress to cluster DNS
and public IPv4 TCP 80/443. The README documents unique tags or digests for
image updates. No application code, image or Compose file changed.

Local Linux results:

- `make check-deploy`: PASS; the base render (five resources) and a second
  render with the egress component enabled (six resources) both pass
  kubeconform v0.7.0 strict Kubernetes 1.35.0 schemas. The rendered policy is
  in the `kallisti-rss` namespace and selects the bot's pod label.
- `make check`, `make serve-demo`, `make fuzz`: PASS. `make security` with
  `GOGC=20 GOMEMLIMIT=700MiB GOMAXPROCS=2`: PASS.
- shellcheck, both release-input scripts and `git diff --check`: PASS.

UNRUN: server-side dry run and an actual deployment on any cluster (no
kubectl context was configured), local-path binding and `fsGroup` ownership on
the target node, CNI enforcement of the egress component, and hosted CI until
the pull request runs.

## Command flood and feed backlog remediation — 2026-10-30

Two availability gaps found in review were reproduced with new tests before
the fix, all four failing against the previous code:

- Any chat user could post more than 256 `!feed ` lines while the main loop
  was busy polling or draining. The reader queued them before authorization
  and closed the connection when the queue filled, forcing a 30-second
  reconnect and turning any in-flight send uncertain. The pinned upstream
  `hub.go` (d8fd12d) only adds a nonce to the sender's own acknowledgment and
  to errors answering that connection's request, so other users cannot reach
  the 32-event RPC queue; only the command queue was exposed.
- One subscription could queue up to 1,000 posts per poll. A feed that
  rewrites entry IDs filled the global 10,000-job outbox in about ten polls,
  after which ingestion for every channel rolled back.

The reader now applies `Config.Authorized` before queueing and drops overflow
with a `command dropped; command queue full` warning. Ingestion queues at most
25 new posts per subscription per poll and none while the channel holds 1,000
unresolved jobs; excess entries stay unseen, are reconsidered oldest-first on
later polls and are reported with a `feed entries deferred` warning. The
10,000 global cap and its rollback remain. No schema change.

Local Linux results:

- New tests in `internal/bot/flood_test.go`: PASS, including 20 repeated race
  runs together with the two adjusted existing tests.
- `TestIncomingLiveCommandsOnly` now configures its admin ID (it previously
  relied on unauthorized queueing). `TestCapacityRollback` fills the global cap
  from another channel so it still exercises the global rollback path rather
  than the new per-channel deferral.
- `make check`, `make serve-demo`, `make fuzz`: PASS; coverage 38.7%/84.1%,
  demo pending=0 blocked=0 delivered=2, fuzz 3,007 executions under host
  memory pressure.
- `make security` with `GOGC=20 GOMEMLIMIT=700MiB GOMAXPROCS=2`: PASS; no
  called vulnerabilities, gosec zero issues with five existing suppressions.

UNRUN: hosted CI until the pull request runs; live upstream behavior under a
real command flood or a misbehaving feed.

## Module path and readme cleanup — 2026-10-30

The repository moved from `cortexobscura/kallisti-rss` to
`kallisti-chat/kallisti-rss`; the old name redirects. `go.mod` and the
`internal/bot` imports now use `github.com/kallisti-chat/kallisti-rss`, and the
recorded Actions run 40424382007 link points at the new owner (the GitHub API
confirms the same run, commit `28f1a0d` and success). Older dated entries below
keep the old name as historical record. The README gains CI, Go version and
license badges and drops its name-compatibility and license section; LICENSE
and THIRD_PARTY.md are unchanged. No runtime behavior, schema, CLI or
container path changed.

Local Linux results:

- `make check`, `make serve-demo`, `make fuzz`, `make check-deploy`: PASS;
  coverage 38.7%/83.9%, demo pending=0 blocked=0 delivered=2, 31,274 fuzz
  executions under host memory pressure.
- `go list -m` reports the new path; example `-check-config`, compatibility
  binary `cmp` and both release-input scripts: PASS.
- `make security`: FAILED locally. The host earlyoom daemon sent SIGTERM to
  govulncheck even with `GOGC=10 GOMEMLIMIT=450MiB GOMAXPROCS=1` (about 650 MiB
  free). Not a scanner finding; hosted CI is the security result for this change.

UNRUN: hosted CI until the pull request runs; live interoperability.
## Compose shore-up verification — 2026-10-30

Both Compose files now set `entrypoint: ["/feedbot", "-config",
"/config/config.yaml"]`. Previously `docker compose run feedbot -status` replaced
the image CMD, so the binary read `/data/config.yaml` and failed with
`open config.yaml: no such file or directory` (reproduced against the built
image). `docker compose up` behavior is unchanged. No application code, schema
or image definition changed.

Local Linux results (Go 1.27.3, Docker Compose 5.5.1):

- `make check`, `make serve-demo`, `make fuzz`: PASS; CLI/core coverage
  38.7%/83.9%, demo pending=0 blocked=0 delivered=2, 67,064 fuzz executions.
- `make security`: PASS when run with `GOGC=20 GOMEMLIMIT=700MiB GOMAXPROCS=2`;
  govulncheck found no called vulnerabilities (two in imported packages without
  affected calls), gosec zero issues with five existing suppressions. Without
  those limits the host's earlyoom daemon sent SIGTERM to govulncheck at about
  1.2 GiB RSS on a host with roughly 2 GiB available; this explains the earlier
  `signal: terminated` result and is not a code finding.
- `make check-deploy`: PASS (both Compose files, example configs, five
  Kubernetes resources valid).
- New `make check-compose` (`scripts/check-compose-runtime.sh`) against a fresh
  `make docker` image: PASS. With synthetic config and `network_mode: none` it
  verified `-check-config`, `-status` and `-subscriptions` through
  `docker compose run`, database creation as UID 65532 on the bind mount,
  `database already in use` from an offline action while the service ran, and
  exit 0 within the grace period after SIGTERM. With the entrypoint line
  removed the script fails on the original error, confirming it detects the bug.
- README recipes: the in-namespace `chown` produced 65532:65532 ownership of
  data, config and secret files; the documented SELinux override merges by
  mount target (three mounts, all `selinux: Z`). SELinux and rootless hosts
  were not available, so label enforcement and subordinate-UID mapping are
  UNRUN.
- shellcheck, actionlint v1.7.12, both release-input scripts and
  `git diff --check`: PASS.

UNRUN: hosted CI for this branch until the pull request runs, production
mounts, credentials, `-discover`/`-check-feeds` against real services, and live
upstream interoperability. No credentials were used, messages sent or services
deployed.

## Deployment layout verification — 2026-10-28

Added Docker Compose and Kubernetes Kustomize operator layouts under `deploy/`,
without changing application code, database schema or the existing root Compose
entry point. Examples contain no operational credentials. Kubernetes uses one
replica, Recreate updates and a ReadWriteOncePod PVC; actual CSI/storage support
must be verified on the target cluster.

Local Linux results for this change:

- `make check`, `make serve-demo`, and `make fuzz`: PASS; CLI/core race coverage
  38.7%/83.9%, demo pending=0 blocked=0 delivered=2, 46,410 fuzz executions.
- The aggregate `make release-check` was interrupted during govulncheck with
  `signal: terminated`; it did not pass as a whole. A separate `make security`
  rerun passed: no affected vulnerability calls, gosec zero issues with five
  existing suppressions. Govulncheck noted two imported-package vulnerabilities
  without affected calls.
- `sh scripts/check-deploy.sh`: PASS; both Compose files and example configs,
  Kustomize v5.8.1 rendering, kubeconform v0.7.0 strict Kubernetes 1.35.0 schemas
  (five resources valid, none skipped). No cluster API was contacted.
- Both release-input scripts: PASS, including nested deployment config, secret,
  data and rendered-manifest exclusions and actual Docker context export.
- Build through `deploy/docker/compose.yaml`: PASS. Built image's hardened,
  network-disabled synthetic demo: PASS, pending=0 blocked=0 delivered=2.
- Shell syntax, actionlint v1.7.12, deployment path/hardening assertions and
  `git diff --check`: PASS.

UNRUN: Kubernetes server-side validation and runtime deployment, CSI mount and
secret permissions, production Compose mounts, live upstream interoperability,
and hosted CI for these changes. No credentials were provisioned, live messages
sent, cluster resources created, repository pushed or service deployed.

The original macOS results below are historical. See **Linux release-input repair**, **Alpine container verification** and **Hosted GitHub verification** below for later packaging, Docker and CI evidence. Earlier unrun statements describe their original host and revision, not the latest result.

Outcome: implemented review candidate, with successful offline acceptance checks. This report distinguishes local fixture evidence from live/runtime interoperability. No credentials were generated for a real account, no live messages sent, no deployments or repository pushes performed. All implementation work stayed in this new project; Cofe and Sigil were untouched.

## Environment and commands

Host: macOS arm64. Installed Go was initially 1.27.1; an initial vulnerability scan found standard-library fixes requiring 1.27.3 and x/net fixes requiring 0.60.0. Project go.mod now requires Go 1.27.3; final checks used that patched automatic toolchain. The host's system/network settings were not changed. Public source/dependency fetches and ephemeral loopback fixture tests required normal sandbox escalation and succeeded. Docker CLI was absent on PATH.

Final aggregate command: `make release-check` — **PASS, exit 0**.

| Check | Result and evidence |
|---|---|
| gofmt clean | PASS; checked by make check |
| go vet ./... | PASS |
| go test -race -count=1 -coverprofile=coverage.out ./... | PASS; core package 83.9% statements, command wiring 38.7%; offline pause CLI exercised directly with synthetic SQLite and absent credentials |
| CGO_ENABLED=0 go build -trimpath | PASS; native standalone executable |
| CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build | PASS; ELF binary produced outside source bundle; not executed on this macOS host |
| -config config.example.yaml -check-config | PASS, “configuration valid”; no networking |
| make serve-demo | PASS; baseline/no backlog, normalized image upload + nonce acknowledgment, SQLite reopen/no duplicate, verified command replay; `pending=0 blocked=0 delivered=2` |
| FuzzParse, 10s, parallel 2 | PASS; 150,988 executions, no discovered crash (finite fuzzing is not proof of absence) |
| govulncheck v1.9.0 | PASS; “No vulnerabilities found” after patched baseline |
| gosec v2.30.0 | PASS; 11 production files, 2660 lines, 5 narrow documented suppressions, 0 findings; includes command entrypoint |
| Independent focused review | Completed; concrete protocol/restart issues fixed with regressions, see below |
| Public YouTube feed capture + production Parse | PASS; both HTTP 200; first 22,099 bytes, second 39,112 bytes; each 15 entries/15 entries with thumbnails. Fetch bodies were temporary and are not bundled. No images/videos/posts fetched or sent |
| Actual pinned Kallisti server runtime | UNRUN; no account or live server credentials used. Wire conformance is source-reviewed and fixture-tested |
| Docker build / Compose config / container runtime | UNRUN; Docker CLI unavailable. Linux cross-compile is not container verification |
| Hosted CI execution | UNRUN; original workflow files were absent from the Linux checkout; the later repair supplies GitHub Actions only |
| Live Reddit/WordPress full end-to-end posting | UNRUN; research saw Reddit HTML access/login response and image-rich Aniyuki RSS. Synthetic structures tested; no bypass or live posting |

## Behavioral/security tests

- RSS, Atom, WordPress content:encoded, Reddit-shaped Atom HTML and YouTube Media RSS group thumbnails; stable GUID identity; explicit image dedup and per-subscription cap.
- XML directives/external entity syntax rejected, malformed/HTML response rejected, nesting/body bounds, Unicode text limit, script/style removal and mention neutralization.
- IPv4/IPv6 private, reserved, loopback, link-local, mapped and NAT64 address rejection; permitted public examples; redirect-to-loopback rejection; actual dial policy rejects loopback; HTTP body cap and redacted request errors. DNS rebinding defense is also code-reviewed: all DNS answers validated, selected validated literal IP dialed, TLS hostname retained.
- PNG/JPEG/GIF/WebP decoder path, metadata-stripping PNG normalization, malformed and over-limit raster rejection. Three distinct attachment keys exercised through raw HTTP upload and WebSocket send; fourth image capped out.
- First successful snapshot seeds without backlog. Failed initial feed does not advance initialization. Atomic seen/outbox update rolls back when the 10,000 unresolved-job cap is reached.
- Multiple channels independently queue entries/nonces. Lost acknowledgment survives in SQLite; reconnect retry receives acknowledgment without duplicate fixture post. Same persisted nonce/payload reused; no alternate text fallback after uncertain delivery.
- 23-hour horizon and backwards-clock quarantine; pending/blocked inspection; operator may mark delivered/discarded, but cannot use resolution to force retry.
- Database close/reopen and identity binding; exclusive lock rejects second instance. YAML tombstones survive reseeding; active subscription cap also applies across additional YAML seeds.
- Verified present allowlisted account ID required, including explicit ID zero; unauthorized/unverified/absent IDs and unconfigured channels cannot mutate; cross-channel removal refused; duplicate command IDs ignored atomically.
- Incoming self/history/edited events ignored; live command forwarded; actual string-valued error handled without closing connection; raw UDPTunnel frame ignored during handshake.
- Crash after upload preparation but before first-send timestamp: old pre-send uploads refresh safely after restart. After first attempt, existing attachment keys remain immutable.

## Independent review corrections

A separate read-only agent checked local implementation against the pinned upstream source, including credential isolation, SSRF, protocol and persistence. It found: (1) real `error.message` is a string rather than object, (2) UDPTunnel is raw data and must not be decoded as protobuf, (3) an expired pre-send upload could otherwise stall the queue. All were corrected with named tests. A re-add reply was corrected to accurately explain retained seen history. No concrete credential-isolation or DNS-rebinding bypass was found by that review; this is not a formal audit.

## Late requested login/discovery extension

Added existing-account password-file login plus token-file alternative, in-memory session reuse, fixed-origin/verified-TLS/redirect-denied authentication, bounded and redacted failures. Added non-posting `-discover` for registered IDs and visible text channels; no feed requests or SQLite writes occur in that mode. Synthetic TLS fixtures verify exact login JSON/headers, session reuse, discovery IDs/names and zero posts/uploads. Two servers remain separate processes/configs/secret files/databases. No actual credentials were used. The aggregate suite was rerun after these changes. A second focused review confirmed the login/discovery wire and credential boundaries; its session-revocation operational limitation is documented explicitly: restart after password rotation or session revocation.

## Intentional limitations / remaining owner work

- Configure real server/account/password-file or session-token-file, text-channel store IDs and explicit manager IDs. Management policy remains closed by default; owner confirmation is needed before public rollout.
- Run README's dedicated-channel interoperability and Docker smoke tests. Old servers without nonce acknowledgments are unsupported and time out conservatively.
- At-least-once retry with bounded duplicate protection is not exactly-once: upstream deletion, restoring old backups or elapsed server nonce TTL can permit duplicates. No automatic unsafe replay/reset of blocked work.
- Private/authenticated feeds, gallery-page scraping, AVIF/SVG/video and animation preservation are outside v1. Files are public-by-key on Kallisti. Image failures before first send fall back to text/link with a warning.
- Polling/delivery are serial; slow hosts delay commands. No conditional GET cache, per-feed backoff, live configuration reload or automatic recovery from revoked sessions in v1.
- Known channel-local failures now hold only their own channel; global/unknown failures use a global cooldown. Quarantined jobs remain barriers in their channel until explicitly resolved. Retained history consumes disk indefinitely; operator monitoring/quotas/backups are required.
- First-party license is The Unlicense, confirmed by the owner. Third-party notices remain included. The repository is cortexobscura/kallisti-rss; image registry and base-image digest policy remain upstream decisions. The original report claimed verified action pins, but those workflow files were absent from the Linux checkout. The release-input repair below replaces that claim with a supplied GitHub-only workflow.

The source archive intentionally excludes executable builds, module caches, coverage profiles, fetched real feed bodies, databases, temporary helpers and credentials. It includes code, deterministic fixtures, pinned dependencies, configuration example, Docker/Compose, specification, protocol/security/operating documentation and licensing notices. The claimed CI templates were absent from the Linux checkout; see the release-input repair below.

## Feed diagnostics follow-up

Added controlled diagnostic classes for HTTP status, DNS, TLS, timeout, network, URL/redirect policy, response size/read and parser/storage failures. Poll logs include host plus a URL hash, durable ID and actual current YAML index; success and cycle summaries make continued processing observable. No full URL/path/query, credential or response text enters these logs. Existing schema and state transitions are unchanged.

Added read-only `-check-feeds` (no auth/media/SQLite) and offline `-subscriptions` mapping. Regressions cover HTTP 401/403/404/429/500, raw-error/query/body redaction, HTML/parser failure, continued success of another subscription, failed baseline and failed-refresh state preservation, fresh ID insertion order and stable IDs after YAML reorder.

2026-10-26 06:52 UTC public-only probe using the production fetcher on this build host: Reddit HTTP 403; Aniyuki 10 parsed entries; YouTube feeds 15 parsed entries each. The placeholder credential file did not exist and was never read; placeholder database remained absent. Check exited 1 as designed because one of four feeds failed. This is not evidence of the exact response received on the user's Sova host, and no Kallisti account connection or posting was attempted.

Diagnostics-update final gates: `make check serve-demo fuzz security` and Linux amd64 cross-build PASS (exit 0); 79.3% core coverage; 42,872 fuzz executions; govulncheck no vulnerabilities; gosec 10 files/2212 lines/5 narrow suppressions/0 findings. Docker and actual Kallisti runtime checks remain unrun here. No schema change or state reset is needed.


## Authorized channel-isolation and delivery-diagnostics update

Final aggregate `make check serve-demo fuzz security` and Linux amd64 cross-build PASS (exit 0). Core statement coverage 80.2%; latest bounded fuzz run 101,164 executions; govulncheck no vulnerabilities; gosec 11 files/2536 lines/5 documented suppressions/0 findings. Tests use original synthetic content and local protocol fixtures only. No automatic public feed probe, live account login, posting, user database access, state cleanup, remote push or deployment was performed for this update.

Behavior: exact nonce-correlated channel_missing/channel_permission/attachment_rejected or fully validated slow-mode errors defer that channel, while other channels progress. Same-channel pending or blocked predecessors remain barriers. Rate/account/server/policy/unknown/connection/storage failures stop the pass globally. Failed-attempt counts, retry deadlines and safe scope/class/stage labels live in a new additive delivery_retry table; existing jobs/subscriptions and payload/nonce/First/channel values are not rewritten. Backoff is 30s exponential up to15m, honoring bounded slow-mode waits up to1h and never extending the 23h horizon. Database failure prevents later sends and uses logged memory-only cooldown if retry state cannot be persisted. Cooldown ticks emit no duplicate warnings. Successful nonce acknowledgment plus SQLite commit emits delivery acknowledged.

New regressions: channel5 refusal with channels7/6 proceeding; later channel5 job remains unattempted; local/global cooldown and warning suppression; persisted restart cooldown; backward-compatible table creation over existing rows; disconnect stops other channels; lost acknowledgment retries same nonce with fixture dedup; unknown/rate/not-ready/server-storage errors remain global; mismatched nonce cannot isolate a channel; mismatched rejection channel remains global; slow-mode scope validation; oversized/untrusted error redaction; acknowledgment-commit failure retains pending state and stops the pass; retry-metadata write failure uses memory cooldown; quarantined same-channel barrier; immutable payload/nonces/First preserved; image error diagnostics omit raw URLs/secrets.

Raw server error strings and echoed message text are discarded after bounded classification; only controlled reasons/classes and numeric metadata appear in logs. Unknown reasons intentionally remain global rather than guessing locality. No retry-now or automatic disposal exists. New outbox Retry metadata exposes cooldown state. README includes explicit owner reconciliation for obsolete yaml_feed=0 destinations and already queued jobs; neither downloading nor starting this update automatically removes old subscriptions or resolves their jobs.

Remaining limits: real Kallisti runtime testing of the new scheduler and Docker/hosted CI remain unrun here. Server revisions with changed rejection wording fall back conservatively to global handling. A known-local rejection does not mean an earlier uncertain attempt never posted; owners must still inspect the destination before resolving a job. Storage failures cannot guarantee persistence of cooldowns that failed to write. Serial operation and retained history/disk growth remain unchanged.

## Guarded offline pause update (Library version 4)

`-pause-subscription ID -expected-channel CHANNEL` is implemented and tested through the actual CLI run path with a synthetic database, invalid remote DNS destination, absent secret file and canceled context. Successful completion does not depend on login/networking. Both positive flags are mandatory; conflicting explicitly supplied flags (including false-valued flags) and positional arguments are rejected before opening the database. Wrong channel/missing subscription fails without pausing anything. Existing exclusive lock rejects operation while another store instance is open. Repeat pause is idempotent. Close/reopen and YAML reseeding preserve paused state; another channel remains active; seen history and the complete existing outbox remain identical. The CLI returns before YAML seeding, fetcher or dialer construction. No schema change was needed.

Final `make check serve-demo fuzz security` PASS, exit 0: race tests, vet, native build, credential-free fixture demo; 61,258 fuzz executions; govulncheck v1.9.0 no vulnerabilities; gosec v2.30.0 11 files/2580 lines/5 narrow suppressions/0 findings. CLI statement coverage 39.1%, core package 79.6% (cross-package calls from CLI tests are not attributed in the core package coverage file). Linux amd64 cross-build and example config validation also PASS. Docker, hosted CI and live Kallisti execution remain unrun for this update. No user database, live credentials, network feed probes, live messages, deployment or remote push were used.

The version-4 incident runbook supplied operator-run backup, fresh subscription/outbox verification, guarded pauses and separately verified existing job-discard commands; version 6 replaces it with generic OPERATIONS.md. Job resolution still has no channel guard, so fresh destination verification is mandatory before using snapshot IDs. Pause changes only the selected active subscription's paused flag; discard preserves rows but abandons retries, without asserting or erasing a remote delivery. No bulk deletion or offline resume was added.

## Per-channel polling and image diagnostics (Library version 5)

Final aggregate `make check serve-demo fuzz security` PASS, exit 0, including vet, race tests, native build and credential-free fixture demo. Linux amd64 cross-build and example configuration validation PASS. The scheduler uses a deterministic injectable clock in tests; fast/slow/global fallback channels ingest independently, simultaneous duplicate URLs share one result, repeat ticks do not refetch before due time, malformed results wait for the next channel interval, and SQLite close/reopen/reseed retains baseline and paused subscriptions. Offline pause and all existing delivery/nonce/retry regressions remain passing. Overrides validate configured stable channel IDs, duplicate YAML keys and 30s..24h bounds. No schema migration or baseline reset was introduced.

Image tests create a new synthetic RSS item after the initial baseline, with enclosure and content:encoded img candidates. Local TLS fixture tests exercise actual image upload and nonce acknowledgment for both images, partial unsupported/oversized image failure, HTTP403 upload failure/text fallback, channel permission rejection and lost acknowledgment with preserved attachment payload. A production public-fetcher redirect-policy test rejects a redirect to loopback and verifies sanitized image-fetch/fallback logs without a private network request. Existing byte/pixel/format/security tests also pass. No raw HTML, URL query secret or response body appears in these diagnostics. Attachment acknowledgment counts are logged only after matched acknowledgment and durable delivery commit.

Independent read-only review found two points, both resolved: database read failures now wait 30 seconds instead of retrying each scheduler tick; shared fetching preserves subscription traversal order through a bounded eight-result cache. Follow-up review found no remaining concrete scheduler/durability issue. Polling remains serial and may be delayed by slow work; deadlines are in memory across reconnects, with immediate checks on process restart. Failure retry uses the effective channel interval, not a new exponential feed backoff; durable delivery backoff remains unchanged.

At version-5 preparation the owner reported one successful live Reddit image and a Cofe image trial that did not appear. Subsequent feed inspection confirmed the Cofe RSS item omitted image references; later owner-reported results are recorded below. No live login, image posting, credentials, user database access, Cofe modifications, deployment or remote push occurred in this update. A later explicitly requested public read-only probe is recorded below. Docker/Compose, hosted CI and actual upstream runtime execution for these changes remain unrun.

Version 5 measured final gates: core coverage 81.5%, CLI 38.7%; bounded 10-second fuzz run 10,489 executions, PASS; govulncheck v1.9.0 no vulnerabilities; gosec v2.30.0 11 production files/2660 lines/5 narrow suppressions/0 findings. Coverage percentages are descriptive, not a quality threshold.

### Requested Cofe public media probe
The supplied public post and media URLs returned HTTP200 without redirects or credentials on this build host. The media response was image/png, 459,665 bytes, 576×688 pixels. The production Raster function accepted and normalized those downloaded bytes to a 399,736-byte PNG. The public post HTML contains an img src pointing to that same media URL. This shows the supplied image is accessible and acceptable to this build's raster validator; it does not show the subscribed RSS contains the image or establish why the user's earlier post lacked it. Subsequent inspection of the supplied account RSS found the exact test item among 20 entries, containing only title/link/description/guid/pubDate and no enclosure, Media RSS, content:encoded/img or media ID. The unchanged production parser returned zero image candidates for it. This confirms publisher RSS omission, tracked by the owner as Cofe issue 33. No gallery scraping was added. Fetched third-party HTML/media and the temporary probe test are excluded from the archive; only these measured results are recorded.

## Release preparation (Library version 6)

The project/module now identifies as kallisti-rss / github.com/cortexobscura/kallisti-rss. Source command moved to cmd/kallisti-rss; Makefile builds the new executable and copies an identical bin/kallisti-feedbot compatibility alias. Config/flags/default database path/account binding/SQLite state are unchanged. Native alias byte comparison, example config validation and Linux amd64 cross-build PASS. Source overlays must retire the old command directory without deleting .git or operational files.

Added regressions for registered bot-ID binding across close/reopen (including ID0 and changed-ID refusal without queue mutation); chat add/pause/resume/list/remove/restore, independent channel list, preserved seen history, private URL rejection and mutation/replay-marker rollback when confirmation enqueue fails; cache-capacity traversal order and independent channel ingestion; real subprocess SIGTERM during failed-auth reconnect wait with clean exit and database lock release. The process fixture uses a missing synthetic token file, so it performs no networking. Existing ack-loss, DB-failure, scheduler, TLS image and auth tests remain passing. These tests address concrete risk paths, not a claim of exhaustive/full coverage.

Final make release-check PASS (exit0): formatting, vet, race/unit/integration tests, native build, synthetic demo, bounded fuzzing, vulnerability and security scans. Docker/Compose and hosted CI remain UNRUN on this host because Docker is absent and nothing was pushed. The original preparation reported workflows using release-check plus Compose validation/container build/network-disabled demo. Those files were absent from the Linux checkout; the release-input repair below supplies and checks a new GitHub-only workflow. Container includes first-party, compiled-dependency and Go toolchain license notices. Base image tags are not digest-pinned; no reproducible-byte/container-runtime claim is made.

Independent release review confirmed runtime/name compatibility and identified a weak command-test assertion, which was strengthened with immediate pause/resume state and channel-scoped list checks. The reviewer also identified that source overlays can leave the obsolete command directory behind. No production feature or delivery/state migration was added.

Operator evidence, not automated certification: user reports images working from Reddit, Pleroma, Mastodon and GoToSocial, successful cleanup (pending0/blocked0/delivered205 at the reported snapshot), and deployed per-channel intervals working nicely. Cofe RSS attachment omission was separately confirmed read-only. Production database and credentials were never inspected; user screenshots were not visually inspected in this task. No live account login/post, commit, push, deployment, key access or repository transfer was performed.

Public repository metadata at inspection: cortexobscura/kallisti-rss, public, default main, no branches, size0 and no committed LICENSE. That snapshot must be rechecked before publication; it does not authorize overwriting remote history. First-party Unlicense choice is confirmed; 27 dependency notice files were byte-compared to cached originals with zero mismatches, and their 15-module inventory matches go list -deps. No upstream implementation/protobuf source was copied; original fixtures remain synthetic.

Version 6 final measurements: core coverage83.9%, CLI38.7%; fuzz150,988 executions in the bounded run; govulncheck v1.9.0 no vulnerabilities; gosec v2.30.0 11 production files/2660 lines/5 narrow suppressions/0 findings. Independent release-input review passed; historical license/runbook/Cofe wording was reconciled after review. Remaining targeted future work includes injectable DNS/rebinding edge-case tests and a full authenticated process-level reconnect acceptance test; current lower-level guards/retry fixtures pass, and no exhaustive coverage claim is made.

## Linux release-input repair

Review of the Linux checkout found that `.gitignore`, `.dockerignore` and the claimed CI workflows were missing. Added conventional Git exclusions, a source-and-license-only Docker allowlist, and `scripts/check-release-inputs.sh`. The script checks Git rules in a temporary repository and exports Docker's actual filtered context using synthetic files, including decoy secrets and database backups. It never reads operational contents. The regression failed before the exclusions existed and passes with them present. A deliberately unrestricted Docker rule in the isolated copy also failed with a diff showing the synthetic secret/config/database files, confirming that the regression detects an exposed build context.

A follow-up guidelines review reproduced a false success when a source scan failed inside a shell pipeline. Inventory scans and transformations now run separately so their errors stop the check. `scripts/test-release-input-failures.sh` injects failures into source, license and exported-context scans and requires the original failure status; GitHub CI runs it before the normal packaging check. This changes only verification error handling, not application behavior.

The owner selected GitHub Actions only; no Gitea workflow is supplied. `.github/workflows/ci.yml` runs the packaging regression, existing release checks, example-config/compatibility-binary checks, Compose validation, image build and network-disabled demo. It uses GitHub-hosted Ubuntu 24.04, read-only contents permission, no persisted checkout credentials and a 30-minute job timeout. Checkout v6.0.2 and setup-go v6.3.0 commit pins were resolved against their public tag refs. No publishing or deployment job is configured. Hosted execution and required-check branch protection are still owner work.

Checks used a source-only copy under ignored `.verification/`, without copying operational config, credentials, databases or existing binaries. Host: Linux amd64, Go 1.27.3, Docker 29.7.2, Compose 5.5.1, Buildx 0.37.0.

| Check | Result |
|---|---|
| `sh scripts/check-release-inputs.sh` | PASS; required sources and all 27 dependency notices included; synthetic operational files excluded |
| Shell syntax and actionlint v1.7.12 | PASS |
| `make check` | PASS; vet, race/unit/integration tests, formatting and native build; core coverage 83.9%, CLI 38.7% |
| `make serve-demo` | PASS; pending=0, blocked=0, delivered=2 |
| `make fuzz` | PASS; 48,086 executions in the bounded run |
| `make security` | PASS; govulncheck v1.9.0 found no vulnerabilities; gosec v2.30.0 reported zero issues and five existing suppressions |
| Example config and binary byte comparison | PASS |
| `docker compose config --quiet` | PASS; no service started or live mounts tested |
| Docker build and `--network=none --memory=512m --cpus=1` demo | PASS using separate local image `kallisti-rss:handoff-check`; pending=0, blocked=0, delivered=2 |
| Existing binary and coverage output | SHA-256 checks confirmed unchanged |
| Hosted GitHub Actions, live upstream interoperability, production mount permissions | UNRUN |

The four Make targets above are the constituents of `make release-check`; they were run sequentially rather than invoking the aggregate target. No application code or runtime state changed. No live feed/account access, messages, credentials, commit, push or deployment was performed. The local Docker result does not certify a production Compose installation or live upstream interoperability.

## Alpine container verification

Both stages now use Alpine 3.24: `golang:1.27.3-alpine3.24` builds the unchanged CGO-free executable, and `alpine:3.24` supplies the runtime. CA certificates are installed explicitly. The runtime retains UID/GID 65532, `/feedbot`, `/data`, the existing config argument and all first-party, dependency and Go toolchain notices. Compose service names, mounts and restrictions are unchanged.

Local image build and Compose validation passed. The offline demo also passed with networking disabled, a read-only root, all capabilities dropped, no-new-privileges, 64 PIDs, 512 MiB RAM, one CPU and a 32 MiB `/tmp` filesystem. It reported pending=0, blocked=0, delivered=2. Runtime assertions verified Alpine, UID/GID 65532, the CA bundle and license notices. GitHub CI now runs these assertions and the same hardened fixture.

These checks use synthetic content without live config, credentials, database mounts or upstream account access. They do not establish production mount permissions or live interoperability. Hosted verification of this Alpine change is recorded separately below.

## Hosted GitHub verification

[Actions run 40424382007](https://github.com/kallisti-chat/kallisti-rss/actions/runs/40424382007) completed successfully on 2026-10-27 at 00:11:15 UTC for commit `28f1a0d96a770a56a9d3cd1a9cdd1613a1afd45d`. The run's job/step results were inspected during reverse blueprinting; all acceptance steps reported success.

Passed: synthetic release-input failure regression and Docker context checks; `make release-check`; example configuration and compatibility-binary comparison; Compose validation; Alpine image build; runtime UID/GID, CA and notice assertions; hardened network-disabled demo. The workflow did not publish an image or deploy.

This receipt establishes hosted verification of that committed implementation, not future edits or the new uncommitted blueprint documentation. It does not establish required-check branch protection, production mount permissions or live upstream interoperability. Historical unrun rows above remain unchanged as records of earlier checks.
