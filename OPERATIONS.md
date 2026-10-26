# Retire an obsolete route without losing history

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
