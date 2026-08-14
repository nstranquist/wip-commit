# State compatibility

## Supported state

`v0.1.0-beta.1` reads and writes state directory version 1. Lane, lease,
intent, and profile records also use JSON schema version 1.

The state is in the Git common directory:

```text
<git-common-dir>/wip/v1/
```

All linked worktrees for one repository use this state. Lane refs remain
separate Git refs under `refs/heads/wip/`.

## Fail-closed rule

The command returns `MIGRATION_REQUIRED` when it finds either of these items:

- a state directory with a version other than the exact name `v1`;
- a lane, lease, intent, or profile with a schema version other than 1.

The command checks state-directory versions before it creates `v1`. This check
prevents an old binary from starting a second coordination domain beside newer
state.

`wip` does not migrate, rename, delete, or rewrite unsupported state. It does
not guess how a future schema works. Malformed version 1 data continues to use
its specific read, identity, or digest error.

## Upgrade procedure

Before an upgrade that changes the state version:

1. Stop all `wip` processes for the repository.
2. Use the current compatible binary to inspect every active lane.
3. Reconcile any capture that moved a lane ref but did not complete its intent.
4. Record each lane ref and commit ID.
5. Release each finished lane. Preserve every lane ref.
6. Make a filesystem backup of `<git-common-dir>/wip/v1/`.
7. Read the new release notes and use only its documented migration command.

The beta has no migration command because it has no earlier public schema. A
future migration must be explicit, idempotent, bounded, and recoverable. It
must preserve the original state until the new state and its receipt are
durable.

Do not change JSON records by hand. Do not rename `v2` to `v1`. Do not copy a
partial state directory between repositories.

## Downgrade procedure

A downgrade is supported only when the target release lists the current state
version as readable. Otherwise, use the release that owns the current state.
Finish or release its lanes before you change versions.

An older command stops when it detects a newer state directory or record. This
failure preserves lane refs and commits. It also prevents the older command
from creating new leases in a separate state version.

## Release policy

Each release note must list the state directories and record schemas that the
release can read and write. A state-version change requires all of these items:

- a reviewed compatibility design;
- an explicit migration and recovery command;
- fixtures for the old and new state;
- interruption and retry tests at every durable boundary;
- a machine-readable migration receipt;
- upgrade and downgrade instructions.

No state migration can be a hidden side effect of `wip init`, `wip status`, or
`wip commit`.
