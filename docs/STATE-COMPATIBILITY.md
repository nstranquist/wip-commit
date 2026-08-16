# State compatibility

## Supported state

`v0.1.0-beta.1` reads and writes state directory version 1. Lane, lease,
capture-intent, initialization-intent, profile, and archive-receipt records also
use JSON schema version 1.

Archive receipt state values are `prepared`, `complete`, `restoring`, and
`restored`.

The state is in the Git common directory:

```text
<git-common-dir>/wip/v1/
```

The owner marker is `<git-common-dir>/wip/domain.json`. It binds the state
owner and writable version before a command creates record directories.

Legacy Nicos Tools state uses `<git-common-dir>/ndev-wip/v1/`. Both
implementations acquire `<git-common-dir>/wip-coordination.lock` before domain
creation. If one domain exists, the other command returns
`COORDINATION_DOMAIN_CONFLICT`.

All linked worktrees for one repository use this state. Lane refs remain
separate Git refs under `refs/heads/wip/`.

## Fail-closed rule

When the command finds either of these items, it returns
`MIGRATION_REQUIRED`:

- a state directory with a version other than the exact name `v1`.
- an unknown top-level entry inside the `v1` directory.
- a domain marker schema version other than 1.
- a lane, lease, capture intent, initialization intent, profile, or archive
  receipt with a schema version other than 1.

The command checks state-directory versions before it creates `v1`. This check
prevents an old binary from starting a second coordination domain beside newer
state.

The domain marker closes the concurrent initialization gap. A future writer
must acquire the same domain lock before it changes the marker or creates a new
version directory.

`wip` does not migrate, rename, delete, or rewrite unsupported state. It does
not guess how a future schema works. Malformed version 1 data continues to use
its specific read, identity, or digest error.

`init-intents` and `archive` are additive version 1 directories. A read-only
inspection accepts their absence as an empty older version 1 store and does not
create them. The next mutating command creates the missing directories after it
validates the domain and layout. Other missing required record directories are
reported by `wip doctor`.

Only a non-dry-run `wip init` claims an uninitialized repository. Other lane
commands inspect the domain first and return `LANE_NOT_ACTIVE` without creating
the marker, version directory, or shared coordination lock. Once version 1
state exists, a mutating command can create only the additive directories that
are missing from that same validated domain.

Archive resume and restore also inspect before mutation. They return
`ARCHIVE_NOT_FOUND` without creating a domain when no state exists. Legacy
queue commands and legacy dry-runs do not create `ndev-wip`. Only explicit
legacy lane creation can claim that compatibility domain.

Every version 1 writer bounds the complete encoded record before publication.
The bound includes the final newline. This rule keeps each new record readable
by the same release.

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

When the target release lists the current state version as readable, a
downgrade is supported. Otherwise, use the release that owns the current state.
Finish or release its lanes before you change versions.

When an older command detects a newer state directory or record, it stops. This
failure preserves lane refs and commits. It also prevents the older command
from creating new leases in a separate state version.

## Release policy

Each release note must list the state directories and record schemas that the
release can read and write. A state-version change requires all of these items:

- a reviewed compatibility design.
- an explicit migration and recovery command.
- fixtures for the old and new state.
- interruption and retry tests at every durable boundary.
- a machine-readable migration receipt.
- upgrade and downgrade instructions.

No state migration can be a hidden side effect of `wip init`, `wip status`, or
`wip commit`.

`wip doctor`, `wip status`, `wip env`, `wip plan`, and archive preview are
read-only. They do not create a domain marker or version directory. A marker
without its version directory is reported as initialized but unhealthy state;
it is not misreported as an unclaimed domain. `wip archive` moves only released
records after an exact reviewed preview. It rechecks the exact records under
lock, preserves all lane refs, and supports receipt-based resume and restore.
