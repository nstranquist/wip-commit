# Architecture

## Data model

All coordination state is below the repository's Git common directory:

```text
<git-common-dir>/wip/v1/
  lanes/<lane>.json
  leases/<lease>.json
  profiles/<lane>.json
  intents/<plan>.json
  init-intents/<init>.json
  archive/<archive>/receipt.json
  locks/lane-<lane>.lock
  locks/leases.lock
  locks/intent-<init>.lock
```

Durable record writes stage complete temporary files in `locks/`. Record
directories therefore contain only canonical JSON names, even when a writer
stops before publication. Temporary files are never interpreted as records.
The encoder checks the final JSON record, including its newline, against the
reader's byte limit before each first write or replacement. An oversized
capture intent fails before the lane ref compare-and-swap. An oversized archive
receipt fails before the command creates its archive batch.
First writes reserve the largest supported lifecycle form. This reservation
includes initialization steps, release timestamps, lane commit metadata, and
archive restore state.

Each checkout has its own Git index. All linked worktrees share the coordination
store and refs through the Git common directory.

Git subprocesses remove inherited repository-local environment variables that
can redirect or reinterpret the repository, common directory, worktree,
namespace, refs, shallow boundary, configuration, or object storage. Discovery
and capture therefore stay bound to the canonical checkout. An inherited
`GIT_INDEX_FILE` remains the source staged view. The capture transaction
supplies its private index explicitly to Git and prepared hooks.

Candidate verification commands remove the active WIP identity, target ref,
commit object, source index, and stale candidate-tree variables. They receive
only the current `WIP_CANDIDATE_TREE` value from the capture process.

`<git-common-dir>/wip/domain.json` owns the state version. A shared
`wip-coordination.lock` serializes public and legacy domain creation. The
standalone store stops if `<git-common-dir>/ndev-wip` exists.

The store checks the domain marker and version directories before it creates
`v1`. It returns `MIGRATION_REQUIRED` for an unsupported version or record
schema. It returns `COORDINATION_DOMAIN_CONFLICT` for another state owner.
See [STATE-COMPATIBILITY.md](STATE-COMPATIBILITY.md).

Lane refs have this form:

```text
refs/heads/wip/<agent>/<lane>
```

## Modes

`shared` mode permits several active lanes in one checkout when their leases do
not overlap. Capture always reads selected staged entries and leaves the shared
index unchanged.

`worktree` mode requires a linked non-anchor worktree. It reserves that
worktree. Another active lane cannot bind to it. A worktree-mode lane also reads
staged entries and leaves the worktree's `HEAD` and index unchanged.

The mode changes the coordination boundary. It does not change the publication
algorithm.

A dry-run creates no commit objects. The first planned group reports the
current lane commit as its parent. A later group reports an empty parent because
its preceding commit does not exist. The ordered group list defines the planned
dependency chain.

Building candidates, including during a dry-run, can add tree objects to the
Git object database. A failed non-dry-run can also leave unreachable commit
objects because publication uses one final ref update. These objects are not
published history. Normal Git garbage collection can remove them later. `wip`
does not prune or delete Git objects as a recovery action.

The split planner compares the active source index with the lane's current
commit. It does not propose staged entries that already match the lane tree.

Portable path identity applies NFC normalization, Unicode case folding, and a
stable lowercase form. Reapplying the key produces the same bytes. Path overlap
uses component boundaries after this canonicalization.

## Lock order

Commands use this lock order:

1. coordination-domain lock during store creation;
2. archive lock during archive or restore;
3. lane locks in sorted lane order;
4. state-registry lock at `locks/leases.lock`.

No command acquires those locks in the opposite order. Different lanes can run
capture work in parallel. One lane is serialized. An initialization-intent
lock is acquired by itself when a completed step is recorded.

The state-registry lock fences operational lane and lease record reads. It also
fences lease replacement and the final lane commit receipt. Windows can reject
an open during atomic replacement without this shared fence.

## Lane state

```text
creating -> active -> released
                   -> aborted
```

Creation first writes the `creating` manifest. It then creates the lane ref with
an expected-empty compare-and-swap and marks the lane `active`. An exact retry
finishes a matching `creating` lane.

Leases have an `active` or `released` stored state. An active lease also becomes
inactive when its timestamp expires. An expired lease cannot be renewed. The
owner must claim the paths again.

A new lease uses a cryptographically random identifier and complete
no-overwrite publication. Renewal and release use atomic replacement of that
same record. Normal readers reject foreign entries in lane and lease record
directories instead of skipping them.

Claim retries can repair an active exact lease that exists without its lane
back-reference. `wip init` repeats its complete canonical claim on every exact
retry. This action repairs that interruption without creating another lease.
Renewal and release inspect the complete lease registry before they write.
Release accepts a valid partial-release state so the same operation can finish.

Capture snapshots the lane's complete active lease set and refreshes it before
work starts. A heartbeat refreshes the same set at one-third of the lease
lifetime. The publication callback compares and refreshes the set while it
holds the registry lock. Every refresh audits the complete active registry for
a cross-lane path overlap.

A heartbeat failure remains authoritative after the ref update. The failed
result includes `ref_updated`, `plan_id`, `plan_digest`, and the applied intent
state. The operator must use that evidence with `wip reconcile`.

## Capture transaction

For one split plan, `wip` performs these steps:

1. Normalize and validate all groups, messages, paths, and verify commands.
2. Check that every planned path is inside the active lease set.
3. Resolve the lane ref and source `HEAD` to exact object IDs.
4. Snapshot the pre-commit and post-commit hook files.
5. Read the source index and calculate a digest for the selected paths.
6. Create a private index from the current lane commit.
7. For each group, copy only its exact source index entries into the private
   index.
8. Run the copied pre-commit hook, check the changed-path scope, run `git diff
   --check`, and run verify commands in a candidate-tree checkout.
9. Create each commit with `git commit-tree`. No ref points to the partial chain.
10. Recheck the lane, lease set, source `HEAD`, selected index digest, and hook
    paths.
11. Write a durable `prepared` intent.
12. Update the lane ref once with the expected old object ID.
13. Mark the intent `applied`, advance the lane cursor, and mark it `complete`.
14. Run the copied post-commit hook once for each commit with that commit's
    private tree loaded.

The source `HEAD`, source index, and source worktree are not rewritten by this
algorithm.

## Intent state and recovery

```text
prepared -> applied -> complete
```

The following table describes crash recovery:

| Last durable event | Ref state | Recovery |
| --- | --- | --- |
| No intent | old | Run the plan again |
| `prepared` intent | old | The plan was not applied; run it again |
| `prepared` intent | new | Run `wip reconcile` |
| `applied` intent | new | Run `wip reconcile` |
| Lane cursor advanced | new | Run `wip reconcile` to mark complete |
| `complete` intent | new | Reconciliation is an idempotent no-op |

Reconciliation rejects a different ref, parent, tree, message, path set, scope,
or final tree. It does not reconstruct or guess missing evidence.

## Initialization transaction

`wip init` validates and bootstraps the coordination domain, then writes a
deterministic `pending` intent before it changes a worktree, lane, lease,
profile, binary, or skill. The intent records these ordered steps:

1. `worktree-ready`
2. `lane-ready`
3. `lease-ready`
4. `profile-ready`
5. `binary-ready`
6. `skill-ready`

Each step is idempotent. A retry must use the same setup values. A per-intent
lock makes concurrent exact retries advance the completed-step prefix without
regression. The intent changes to `complete` after all six steps pass. Recovery
output uses the absolute repository and worktree paths and the resolved base
commit ID. The binary uses complete no-overwrite publication. A matching
partial embedded skill bundle is resumable.

The staged-path read is mandatory when the target worktree exists. A Git read
error stops initialization. Staged whitespace errors remain a reported,
nonfatal finding. A dry-run for a worktree that does not exist reports that the
staged check did not run.

## State inspection and archival

`wip doctor` opens existing state without creating it. It scans at most 10,000
entries in each record directory. It validates refs, record links, profiles,
capture intents, and initialization intents. Capture-intent loading validates
canonical paths, bounds, object identities, the commit chain, and the digest
before doctor accepts any intent state.

`wip archive` selects only released or aborted lanes. A preview binds the
candidate records to an exact cutoff and digest. Apply compares each complete
candidate record again while it holds the archive, lane, and lease-registry
locks. It moves lease and profile records before it moves the lane manifest
under `archive/<id>/`. It preserves every lane ref.

The archive receipt uses `prepared`, `complete`, `restoring`, and `restored`
states. An interrupted apply returns the prepared receipt ID. `wip archive
--resume <id>` continues only that immutable receipt. Restore records
`restoring` before its first move, so an interrupted restore can continue by
the same receipt ID. A retry can also recreate a missing receipt in its
deterministic empty batch. Each receipt validates record placement and binds
lane and released lease records to exact candidates. Extra records fail closed.
Restore writes the lane manifest last.

Resume and restore first inspect the existing domain. They return
`ARCHIVE_NOT_FOUND` without creating state when the domain is not initialized.

## Split-plan format

The top-level JSON value is an array. Each item contains one message, one or
more path scopes, and optional verify commands.

```json
[
  {
    "message": "fix(storage): preserve atomic state",
    "files": ["internal/storage"],
    "verify": [
      {
        "argv": ["go", "test", "./internal/storage"],
        "directory": ".",
        "timeout_ms": 120000
      }
    ]
  }
]
```

The decoder rejects unknown fields, duplicate object keys, trailing JSON, and
input larger than 1 MiB. See [PLAN.schema.json](PLAN.schema.json).
