<!--
Use a lowercase Conventional Commit title: type(scope): imperative summary
The scope is optional. Example: fix(bot): report pause state on repeat commands
Keep the title at most 72 characters, without a trailing period.

Replace the placeholders below. Use lowercase prose in every bullet; preserve
case only for exact identifiers, paths or quoted errors. Reference the commit(s)
that implement each change, using linked short hashes. Remove unused bullets.
-->

## pr summary:

- describe one change and why it matters ([commit](../../commit/full-commit-sha)).
- describe another change ([commit](../../commit/full-commit-sha)).

## note to reviewers:

- highlight the behavior, files or decisions that need particular attention.
- call out any effect on the contracts in `SPEC.md`: public-only feed networking, registered-id authorization, immutable attempted-send payloads and nonces, no-backlog initialization or sqlite tombstones.
- describe sharp edges, compatibility changes, migration steps or rollback limits.
- state which of `make check`, `make serve-demo`, `make fuzz` and `make security` passed, and disclose anything failed, blocked or untested, including live kallisti or container behavior.
