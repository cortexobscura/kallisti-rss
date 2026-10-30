# Implementation evidence and limits

Reverse blueprint of implementation commit `28f1a0d96a770a56a9d3cd1a9cdd1613a1afd45d`. The reconstruction changes documentation, not runtime behavior, schema, credentials or operational state. [SPEC.md](SPEC.md) remains the intended contract; [ARCHITECTURE.md](ARCHITECTURE.md) describes actual wiring. This is not a comprehensive security audit.

## Evidence classes

- OBSERVED: directly present in source, schema, wiring, tests or a recorded run. A source observation is not automatically a tested guarantee.
- INFERRED: likely intent suggested by evidence, not independently established.
- UNCERTAIN: the supported contract or interpretation is not settled by the available evidence.
- SUSPICIOUS: an inconsistency or accidental-looking behavior to investigate, not silently preserve as a requirement.
- EXTERNAL: depends on an upstream system or operator environment that local fixtures cannot establish.

## Behavior and evidence

| Classification | Behavior | Source and executable evidence |
|---|---|---|
| OBSERVED | Strict YAML, exclusive auth source, defaults and interval bounds | `LoadConfig`, `LoadDiscoveryConfig` in [config.go](internal/bot/config.go); `TestConfigStrict`, `TestDiscoveryConfigAndExclusiveAuth` in [bot_test.go](internal/bot/bot_test.go); `TestChannelIntervalConfig` in [schedule_test.go](internal/bot/schedule_test.go) |
| OBSERVED | Fixed-origin password login, memory-only reuse and non-posting discovery | `Dialer.Connect`, `login` in [auth.go](internal/bot/auth.go); `TestPasswordLoginCachedAndDiscovery`, `TestLoginFailuresAndSecretBounds` in [bot_test.go](internal/bot/bot_test.go) |
| OBSERVED | Registered-ID authorization, including explicit zero; no display-name authority | `Config.Authorized`, `Engine.Command`; `TestFailedBaselineAndCommands`, `TestIncomingLiveCommandsOnly` in [bot_test.go](internal/bot/bot_test.go) |
| OBSERVED | Database identity/lock and registered-ID restart guard | `OpenStore`, `Bind`, `BindUser` in [store.go](internal/bot/store.go); `TestExclusiveLock`, `TestStoreLifecycle`; `TestRegisteredIdentityRestartGuard` in [release_test.go](internal/bot/release_test.go) |
| OBSERVED | First successful snapshot does not post backlog; failed fetch/parse does not advance state | `Store.Ingest`, `Engine.poll`; `TestDemo`, `TestFailedBaselineAndCommands`; `TestFailedRefreshPreservesExistingSeenState` in [diagnostics_test.go](internal/bot/diagnostics_test.go) |
| OBSERVED | Atomic seen/outbox writes and command mutation/reply/replay rollback | `Store.Ingest`, `Engine.Command`; `TestCapacityRollback`; `TestChatManagementLifecycleAndRollback` in [release_test.go](internal/bot/release_test.go) |
| OBSERVED | Reader authorizes before queueing commands and drops overflow; per-subscription (25/poll) and per-channel (1,000) ingest limits defer excess entries unseen | `Connection.read` in [transport.go](internal/bot/transport.go), `Store.IngestCounts`, `Engine.poll`; `TestUnauthorizedCommandFloodKeepsConnection`, `TestAuthorizedCommandOverflowDropsInsteadOfDisconnecting`, `TestRotatingFeedCannotStarveOtherChannels`, `TestNewEntryCapDefersOldestFirst`, `TestPollLogsDeferredEntries` in [flood_test.go](internal/bot/flood_test.go) |
| OBSERVED | Due channels remain independent through cache sharing/restart | `Engine.PollDue`; `TestChannelSchedule`, `TestScheduledRestartPreservesPauseAndBaseline`; `TestSharedCacheBoundKeepsSubscriptionOrder` in [release_test.go](internal/bot/release_test.go) |
| OBSERVED | Public-address checks, bounded fetches and controlled diagnostics | [fetch.go](internal/bot/fetch.go), [diagnostics.go](internal/bot/diagnostics.go); `TestURLPolicy`, `TestFetchBoundsAndRedaction`; `TestFetchDiagnosticsRedacted`; `TestImageRedirectPolicyDiagnostics` in [image_diagnostics_test.go](internal/bot/image_diagnostics_test.go) |
| OBSERVED | Bounded parsing/raster conversion and explicit image extraction | [feed.go](internal/bot/feed.go); `TestFeedVariants`, `TestRejectFeeds`, `TestIdentityAndText`, `TestRaster`, `TestWebPRaster`, `FuzzParse` in [bot_test.go](internal/bot/bot_test.go) |
| OBSERVED | Prepared uploads can refresh before a send; attempted payload/nonce stay fixed | `Engine.Drain`, `Store.Prepare`, `Store.SavePayload`; `TestPreSendExpiredUploadRefresh`, `TestUncertainPayloadNeverChanges` |
| OBSERVED | Local rejections isolate channels; uncertainty and storage errors remain conservative | [delivery.go](internal/bot/delivery.go), `Store.NextReady`; `TestChannelIsolationWireAndBarrier`, `TestGlobalFailuresStopOtherChannels`, `TestWrongNonceCannotIsolateChannel`, `TestStorageAckCommitFailureStopsGlobally`, `TestStorageFailureFallbackCooldown` in [delivery_test.go](internal/bot/delivery_test.go) |
| OBSERVED | Lost acknowledgment retries and cooldowns survive SQLite reopen | `TestDisconnectGlobalAndDurableAckLossRetry`, `TestRetryMigrationAndLocalRestart` in [delivery_test.go](internal/bot/delivery_test.go) |
| OBSERVED | Old/backwards-clock attempts block; blocked jobs remain per-channel barriers | `Store.Prepare`, `Store.NextReady`; `TestOutboxResolutionAndClockRollback`; `TestQuarantineKeepsSameChannelBarrier` in [delivery_test.go](internal/bot/delivery_test.go) |
| OBSERVED | Offline pause validates flags and channel, makes no network requests and preserves queue/history | CLI dispatch and `Store.PauseSubscription`; actual `run` invocation in [pause_test.go](cmd/kallisti-rss/pause_test.go) |
| OBSERVED | Process SIGTERM releases the database lock during failed-auth reconnect wait | [process_test.go](cmd/kallisti-rss/process_test.go), `TestProcessSignalAndLockRelease`; not a full authenticated-process reconnect test |
| OBSERVED | Image-stage logs distinguish extraction, preparation and committed acknowledgment | `TestImageStagesTLSAfterBaseline` in [image_diagnostics_test.go](internal/bot/image_diagnostics_test.go); includes partial media failure, denied upload, channel refusal and lost acknowledgment |
| OBSERVED | Original offline demo uses real parser, raster, SQLite and wire adapter | [demo.go](internal/bot/demo.go), `TestDemo`; expected final counts `pending=0 blocked=0 delivered=2` |
| OBSERVED | Packaging excludes synthetic operational files and propagates inventory failures | [check-release-inputs.sh](scripts/check-release-inputs.sh), [test-release-input-failures.sh](scripts/test-release-input-failures.sh); actual Docker context export, not just pattern inspection |
| EXTERNAL | Correctness of upstream identity, acknowledgments, nonce retention and upload lifetime | Pinned upstream source references in [PROTOCOL.md](PROTOCOL.md); local fixture conformance is not upstream execution |

The test names above identify specific assertions, not exhaustive coverage. The latest recorded suite reports 38.7% command-package and 83.9% bot-package statement coverage. These are measurements, not acceptance thresholds or a security score.

## Observed quirks and limits

1. **Offline does not mean read-only.** `main.run` calls `OpenStore` and `Bind` for `-status`, `-outbox`, `-subscriptions`, pause and resolution. A mistyped absent path can create a database/lock, initialize tables and bind identity before inspection. These modes do not seed YAML or read secret contents. The blueprint now states their actual filesystem effects.
2. **Only pause has strict mixed-flag rejection.** Outside its guard, [main.go](cmd/kallisti-rss/main.go) dispatches by precedence: demo, config validation, feed checks, discovery, subscription view, outbox view, positive job resolution, status, then normal operation. For example, positive resolution takes precedence over status. Nonpositive resolution IDs do not select resolution, and a standalone `-resolution` does not select a mode. UNCERTAIN: mixed/invalid action combinations have no defined compatibility contract or comprehensive CLI tests. Use one documented action; do not treat accidental fallthrough as intended behavior.
3. **Configuration is a seed, not reconciliation.** Existing pairs keep their stored image cap, initialized/paused/deleted state and seen IDs. Changing/removing a YAML route does not rewrite the old subscription or its queued destination. `Channels` guards commands and startup visibility, not a filter on all stored subscriptions/jobs. This is established behavior, not an instruction to silently reroute data.
4. **Registration protects delivery, not every earlier startup write.** Normal startup opens/binds/seeds before `Dialer.Connect` and `BindUser`. A changed registered ID prevents queue draining but need not roll back prior schema/seed work. `TestRegisteredIdentityRestartGuard` exercises the binding operation, not an all-or-nothing startup transaction.
5. **Feed order is not time order.** `Ingest` reverses the parser's entry slice and never parses/sorts publication dates. INFERRED: this targets commonly newest-first feeds. The implementation does not establish chronological order for every publisher or guarantee capture of entries that disappear between polls.
6. **Pause/remove does not cancel queued posts.** Pending and blocked jobs survive. Restoring a tombstoned subscription retains initialization/seen history rather than taking a fresh baseline. Unseen entries still available after resume can publish.
7. **Output privacy is surface-specific.** Diagnostic logs use safe classes and host/hash identifiers. Chat `!feed list` includes full source URLs; `-outbox` includes text, image URLs, upload keys and nonces. These are not sanitized log views. Do not use credentials in feed URLs or paste queue dumps publicly.
8. **First-attempt markers are conservative.** `First` is persisted before `Send`; failure before bytes leave the process can still consume the retry horizon. A nonce-matched acknowledgment followed by failed SQLite persistence remains uncertain and pending. No replacement nonce, text fallback or forced replay follows an attempted send.
9. **Session revocation needs operator recovery.** Password login is cached until a reconnect after 29 days, or process restart. Rejected cached sessions are not automatically discarded on every network error. Token-file mode rereads the file on reconnect. This is documented behavior, not a tested automatic revocation-recovery flow.
10. **Inspection and retention are bounded differently.** `Inspect` returns at most 1000 unresolved jobs with no pagination. `Status` omits discarded count. The 10,000 unresolved-job cap does not bound retained seen, command, delivered, discarded or retry rows. Operators must monitor disk use.
11. **Storage upgrades are narrowly evidenced.** Schema creation is idempotent/additive; the retry-table test starts from the compatible pre-retry layout. UNCERTAIN: arbitrary older schemas, incompatible manual edits and downgrades have no supported-version envelope or migration ledger. Do not infer universal upgrade safety.
12. **Fixture helpers are not production selectors.** `Store.Next` is used by tests, while delivery uses `NextReady` for barriers/cooldowns. `Engine.Poll` is used by demo/tests; the production loop uses `PollDue`. Neither helper should replace its production counterpart merely to simplify control flow.

## Contradictions and suspicious behavior

| Classification | Evidence and consequence | Treatment in this reconstruction |
|---|---|---|
| SUSPICIOUS | README describes opt-in public HTTP as port 80 only. `ValidateURL` checks allowed schemes and ports separately; with HTTP enabled it accepts explicit HTTP:443 and HTTPS:80 as well. Existing URL tests do not cover this pairing | Record the mismatch; do not make cross-scheme port acceptance a new requirement or silently change compatibility. Public-address restrictions still apply |
| SUSPICIOUS | CLI action precedence/fallthrough can select a different operation than a user combining flags expects; only pause rejects conflicting explicitly supplied flags | Record exact dispatch and the missing supported contract. No broader validation change is included |
| OBSERVED documentation drift | Existing current-status prose still called hosted CI unrun and security prose described an unpublished bundle, despite publication and a completed run | Update current prose and add the SHA-bound receipt. Preserve dated historical verification sections rather than relabeling their old outcomes |

These entries are not an authorization to fix runtime behavior during a documentation task. Any later change must preserve required error handling, compatibility, immutable attempted sends, tombstones and tests. A tighter interface or schema policy needs an explicit decision when implementation work is requested.

## Remaining verification obligations

- EXTERNAL: execute the dedicated-channel upstream workflow against a recorded Kallisti revision with owner authorization. Source citations and synthetic wire fixtures cannot prove live interoperability or the server's actual nonce/deletion behavior. A partial owner-run check of `v0.1.0-rc.1` passed on 2026-10-30 (login, discovery, polling, image and text delivery, acknowledgments, `!feed list` and a denied user); restart deduplication, disconnect/nonce reconciliation, blocked recovery, Atom delivery, add/pause/resume/remove and the server revision remain unrecorded.
- EXTERNAL: verify production data/config/secret mount permissions and real TLS/Origin policy. The network-disabled container demo uses no operational mounts.
- OBSERVED gap: no deterministic injected DNS-answer/rebinding suite covers every resolver outcome; direct private dialing, IP policy and redirects are covered. Do not replace implementation review with a stronger test claim.
- OBSERVED gap: the real-process signal test uses a missing secret and no network; authenticated reconnect/cancellation remains tested at lower levels rather than as a full process acceptance flow.
- OBSERVED gap: no automated backup/restore recovery acceptance test establishes reconciliation of remote sends against an old snapshot. There is an operator runbook, not an automatic restore service.
- UNCERTAIN: required-check branch protection and a private vulnerability-reporting channel are not established by workflow/source files. Do not claim they are configured.

## Readiness and evidence freshness

IMPLEMENTATION READY for bounded changes to the existing contract. This is not ONE-SHOT READY for an independent replacement: unsupported CLI combinations and the general schema compatibility envelope still require explicit treatment, and exact protocol behavior remains tied to the pinned source/fixtures. No new product or architecture decision was needed to document the supported workflows; unresolved behavior remains labeled rather than normalized.

[GitHub Actions run 40424382007](https://github.com/kallisti-chat/kallisti-rss/actions/runs/40424382007) completed successfully on 2026-10-27 at 00:11:15 UTC for the baseline commit above. It passed packaging regressions, `make release-check`, config/binary checks, Compose validation, image build and the hardened Alpine demo. That result applies to the committed implementation, not these uncommitted documentation changes. Fresh local results for the reconstruction are reported with the final response; [VERIFICATION.md](VERIFICATION.md) retains the dated historical evidence.
