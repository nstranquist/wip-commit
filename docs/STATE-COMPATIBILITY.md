# State compatibility

## Supported state

The current development command reads and writes state directory version 1.
Lane, lease, and archive-receipt records use JSON schema version 1.

New profiles and initialization intents use record schema version 2. Their
readers also accept version 1.

New captures use capture-intent schema version 3. This schema binds
`source_mode`, `untracked_policy`, `source_digest`, and `source_paths`. Readers
also accept capture-intent schema versions 1 and 2 without changing their
historical digest fields.

Archive receipt state values are `prepared`, `complete`, `restoring`, and
`restored`. A restored receipt remains valid history after a newer receipt
archives the same records. Each record must remain live or have exactly one
validated newer archive owner.

The state is in the Git common directory:

```text
<git-common-dir>/wip/v1/
```

The separate review authority uses this root:

```text
<git-common-dir>/wip/review/v1/
```

Its required directories are `archive`, `handoffs`, `landing-intents`, `locks`,
`plans`, and `reconciliation`. The standalone capture path does not convert a
handoff into landing authority.

The owner marker is `<git-common-dir>/wip/domain.json`. Marker schema version 2
binds the owner, writable state version, minimum reader protocol, and sorted
required reader capabilities. New repositories write marker version 2.

The command reads marker version 1 without changing it. Read-only doctor,
handoff, status-observe, and environment-observe operations remain available.
Every mutation stops with `MIGRATION_REQUIRED` until the operator applies the
digest-bound marker upgrade. An old binary rejects marker version 2 because it
does not recognize the reader-floor fields. This is the required fail-closed
behavior.

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
- a domain marker schema version other than 1 or 2.
- a marker version 2 reader floor above `wip.command/3.0.0`, a different
  protocol major, or a required capability that the binary does not advertise.
- a lane, lease, or archive receipt with a schema version other than 1.
- a capture intent with a schema version other than 1, 2, or 3.
- an initialization intent or profile with a schema version other than 1 or 2.

The command checks state-directory versions before it creates `v1`. This check
prevents an old binary from starting a second coordination domain beside newer
state.

The domain marker closes the concurrent initialization gap. A future writer
must acquire the same domain lock before it changes the marker or creates a new
version directory.

`wip` does not migrate, rename, delete, or rewrite an unknown state directory or
record. It does not guess how a future schema works. The state-marker upgrade
changes only the validated owner marker from schema 1 to schema 2.

The separate `wip migrate` command handles only the reviewed legacy NDev
coordination layout. It does not act as a general repair for unknown schemas.

A version 1 profile implies the historical `conventional-v1` policy. Version 1
initialization and capture intents keep their original digest form.

Readers calculate each version 1 digest before they infer any current default.
They do not rewrite an old record during a read.

`init-intents` and `archive` are additive version 1 directories. A read-only
inspection accepts their absence as an empty older version 1 store and does not
create them. The next mutating command creates the missing directories after it
validates the domain and layout. Other missing required record directories are
reported by `wip doctor`.

Only a non-dry-run `wip init` or an exact reviewed migration apply can claim an
uninitialized repository. Other lane commands inspect the domain first and
return `LANE_NOT_ACTIVE` without creating the marker, version directory, or
shared coordination lock. Once marker version 2 owns version 1 state, a
mutating command can create only additive directories in that validated domain.

Archive resume and restore also inspect before mutation. They return
`ARCHIVE_NOT_FOUND` without creating a domain when no state exists. Legacy
queue commands and legacy dry-runs do not create `ndev-wip`. Only explicit
legacy lane creation can claim that compatibility domain.

Every version 1 writer bounds the complete encoded record before publication.
The bound includes the final newline. This rule keeps each new record readable
by the same release.

## Marker upgrade procedure

Before a marker upgrade:

1. Stop all `wip` processes for the repository.
2. Use the current compatible binary to inspect every active lane.
3. Reconcile any capture that moved a lane ref but did not complete its intent.
4. Record each lane ref and commit ID.
5. Release each finished lane. Preserve every lane ref.
6. Make a filesystem backup of `<git-common-dir>/wip/`, including
   `domain.json`.
7. Require a healthy `wip --json doctor` result with no active lanes or pending
   initialization, capture, or archive recovery.
8. Run the read-only preview:

   ```text
   wip --json upgrade --state
   ```

9. Review `path`, `current_sha256`, `desired_sha256`, and `plan_digest`. Apply
   only that plan:

   ```text
   wip --json upgrade --state --apply --yes \
     --plan-digest sha256:<reviewed-digest>
   ```

10. Run `wip --json doctor` again. Require marker schema 2, the expected reader
    floor, no pending recovery, and `healthy: true`.

Preview does not write. Apply acquires the coordination lock, compares the
exact old marker digest again, atomically replaces only `domain.json`, and
verifies the desired digest. A repeated apply to the current marker is
idempotent. Any byte change requires a new preview.

A future state-directory or record migration must also be explicit,
idempotent, bounded, and recoverable. It must preserve the original state until
the new state and its receipt are durable.

Do not change JSON records by hand. Do not rename `v2` to `v1`. Do not copy a
partial state directory between repositories.

## Coordination-domain migration

Use this procedure only for the legacy NDev root at
`<git-common-dir>/ndev-wip/v1`.

1. Stop all capture, queue, landing, and reconciliation writers.
2. Back up every ref and both coordination roots.
3. Record the exact executable, source revision, and portable-skill digest.
4. Run the byte-for-byte read-only preview:

   ```text
   wip --json migrate --preview
   ```

5. Save the exact `data.plan` object as a plan file.
6. Review all manifests, refs, dispositions, conflicts, and producer fields.
7. Apply only a ready plan with its exact digest:

   ```text
   wip --json migrate \
     --apply \
     --plan migration-plan.json \
     --plan-digest sha256:<reviewed-digest> \
     --yes
   ```

8. Run `wip --json migrate --status --migration-id <id>`.
9. Require a complete receipt and a sealed legacy archive.
10. Run `wip --json doctor` with the source-matched command.

Apply writes a plan-bound writer fence before it publishes canonical records.
All capture-store writers stop while that fence is active. The migration
publishes the exact regular `wip/v1/locks/leases.lock` artifact before imported
lane or lease records become readable. It imports valid capture records and
copies review records. It creates the `handoffs` and `plans` review directories
before record publication.

The migration then moves the complete legacy root into a sealed archive. It
does not dual-write the legacy and canonical roots. It does not authorize
landing.

If apply stops, use `--resume` with the same plan and digest. Use `--rollback`
only before the migration moves the legacy source. After that move, preserve
the archive and resume forward.

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

`wip doctor`, `wip status --observe`, `wip env --observe`, `wip plan
--observe`, migration preview, migration status, and archive preview are
read-only. They do not create a domain marker or version directory. A
released-lane handoff is also read-only. An active-lane handoff renews leases.

Authenticated `status`, `env`, and `plan` renew active leases unless
`--observe` is set. This renewal replaces only existing lease records after
complete identity validation.

A marker without its version directory is initialized but unhealthy state. It
is not an unclaimed domain.

`wip archive` moves only released records after an exact reviewed preview. It
rechecks records under lock, preserves refs, and supports receipt-based resume
and restore.
