# Architecture

## Data model

Capture coordination state is below the repository's Git common directory:

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

The separate review and landing authority uses this root:

```text
<git-common-dir>/wip/review/v1/
  handoffs/<handoff-digest>.json
  plans/<review-digest>.json
  landing-intents/<intent>.json
  archive/
  reconciliation/
  locks/
```

The standalone capture command does not create a landing decision. The
coordination migration creates the required review directories and preserves
legacy review records there.

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
`GIT_INDEX_FILE` remains the caller view only for `staged_index` source mode.
The capture transaction supplies its private index explicitly to Git and
prepared hooks. A `worktree_snapshot` uses a separate temporary source index.

Candidate verification commands remove the active WIP identity, target ref,
commit object, caller index, and stale candidate-tree variables. They receive
only the current `WIP_CANDIDATE_TREE` value from the capture process.

`<git-common-dir>/wip/domain.json` owns the state version. A shared
`wip-coordination.lock` serializes public and legacy domain creation. The
standalone store stops if `<git-common-dir>/ndev-wip` exists.

The store checks the domain marker and version directories before it creates
`v1`. It returns `MIGRATION_REQUIRED` for an unsupported version or record
schema. It returns `COORDINATION_DOMAIN_CONFLICT` for another state owner.
See [STATE-COMPATIBILITY.md](STATE-COMPATIBILITY.md).

Coordination migration uses a byte-for-byte read-only preview. The reviewed
poststate manifest binds every required directory and the regular
`wip/v1/locks/leases.lock` artifact. Apply uses one writer fence. It publishes
the registry lock before it publishes imported lane and lease records. It then
moves the complete legacy root into a sealed archive. The migration never
writes to both coordination roots.

Lane refs have this form:

```text
refs/heads/wip/<agent>/<lane>
```

## Coordination modes

`shared` mode permits several active lanes in one checkout when their leases do
not overlap. Use `staged_index` as the controlled source exception in this mode.

`worktree` mode requires a linked non-anchor worktree. It reserves that
worktree. Another active lane cannot bind to it. Use `worktree_snapshot` as the
preferred source in this mode.

The user interface recommends `worktree` mode for one agent per task. Use
`shared` mode only when lanes must use the same checkout and index.

The coordination mode changes the ownership boundary. It does not change the
publication algorithm.

## Source modes

`staged_index` selects exact changed entries from the caller index. Its
untracked policy is `not_applicable`. Foreign staged entries remain unchanged.

`worktree_snapshot` selects exact tracked changes from the reviewed scopes. Its
untracked policy is `forbid` or `include`. The default `forbid` policy stops if
an untracked path exists in a selected scope.

Both source modes bind `source_mode`, `untracked_policy`, `source_scopes`,
`source_paths`, and `source_digest` in the version 3 plan. Capture reconstructs
that source and compares the digest before and after all gates.

A direct commit dry-run creates candidate Git objects in a temporary object
directory. It removes that directory before it returns. It snapshots hook bytes
but does not execute repository hooks or verification commands. It also skips
the publication callback, commit creation, intent writes, lease renewal, and ref
updates.

The result uses schema `3.0.0` and reports `gate_outcome: "skipped"`. Its
`execution` object reports planned, executed, and skipped counts. All executed
counts are zero. The first group reports the current lane commit as its parent.
A later group reports an empty parent because its preceding commit does not
exist.

Guided `wip capture` does not accept `--dry-run`. It rejects that combination
before repository discovery, store access, lease renewal, or handoff creation.

A failed non-dry-run can leave unreachable commit objects because publication
uses one final ref update. These objects are not published history. Normal Git
garbage collection can remove them later. `wip` does not prune or delete Git
objects as a recovery action.

The split planner compares the selected source with the lane's current commit.
It does not propose source paths that already match the lane tree.

Portable path identity applies NFC normalization, Unicode case folding, and a
stable lowercase form. Reapplying the key produces the same bytes. Path overlap
uses component boundaries after this canonicalization.

## Lock order

Commands use this lock order:

1. initialization-intent lock during setup or a reviewed init abort.
2. coordination-domain lock during store creation.
3. archive lock during archive or restore.
4. lane locks in sorted lane order.
5. state-registry lock at `locks/leases.lock`.

No command acquires those locks in the opposite order. Different lanes can run
capture work in parallel. One lane is serialized. Initialization holds its
per-intent lock from before the first setup side effect through the final
journaled step. An exact retry therefore serializes with both another retry and
`init-abort`. The abort path acquires no lower-order state lock.

An active handoff holds the lane lock through intent selection, validation,
lease renewal, and status construction. A final guided capture keeps its lane
lock through handoff creation and lane and lease release.

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

Authenticated `status`, `env`, `claim`, `plan`, and `handoff` also renew the
complete active lease set. `status --observe`, `env --observe`, and `plan
--observe` are pure reads. An observed plan cannot write an output file. A
handoff for a released lane does not renew leases.

`env` compares the canonical lane worktree with the current repository root.
A mismatch stops the command and leaves the lease unchanged.

A heartbeat failure remains authoritative after the ref update. The failed
result includes `ref_updated`, `plan_id`, `plan_digest`, and the applied intent
state. The operator must use that evidence with `wip reconcile`.

## Capture transaction

For one split plan, `wip` performs these steps:

1. Validate the reviewed-plan bindings and named message policy.
2. Normalize and validate all groups, message safety, paths, and verify commands.
3. Check that every planned path is inside the active lease set.
4. Resolve the lane ref and source `HEAD` to exact object IDs.
5. Snapshot the pre-commit and post-commit hook files.
6. Rebuild the selected source and require its digest to equal the reviewed
   plan.
7. Create a private index from the current lane commit.
8. For each group, copy only its exact source entries into the private index.
9. Run the copied pre-commit hook, check the changed-path scope, run `git diff
   --check`, and run verify commands in a candidate-tree checkout.
10. Create each commit with `git commit-tree`. No ref points to the partial chain.
11. Recheck the lane, lease set, source `HEAD`, selected source digest, and hook
    paths. This second source check detects changes during the transaction.
12. Write a durable `prepared` intent.
13. Update the lane ref once with the expected old object ID.
14. Mark the intent `applied`, advance the lane cursor, and mark it `complete`.
15. Run the copied post-commit hook once for each commit with that commit's
    private tree loaded.

The source `HEAD`, caller index, and source worktree are not rewritten by this
algorithm.

## Intent state and recovery

```text
prepared -> applied -> complete
```

The following table describes crash recovery:

| Last durable event | Ref state | Recovery |
| --- | --- | --- |
| No intent | old | Run the plan again |
| `prepared` intent | old | The plan was not applied. Run it again. |
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

A restored receipt is durable history. If a later receipt archives the same
records, validation follows each non-live record only to one newer,
non-restored receipt. The newer receipt must be valid and must own that exact
record. No owner, more than one owner, an invalid owner, or changed candidate
data stops validation. This rule lets repeated archive and restore cycles keep
all receipts without weakening missing-record checks.

Resume and restore first inspect the existing domain. They return
`ARCHIVE_NOT_FOUND` without creating state when the domain is not initialized.

## Reviewed-plan format

`wip plan` emits one version 3 object. The object binds the lane, ref, source
`HEAD`, source mode, untracked policy, source scopes, source paths, source
digest, active leases, and message policy.

Each group contains review guidance, an empty draft message, exact files, and
optional verify commands. A reviewer adds messages and changes `state` to
`reviewed`.

`wip capture` accepts only this object. It calculates a canonical SHA-256 plan
digest. A new capture stores that digest and its private-source binding in
capture-intent schema version 3.

`wip commit` also accepts the object. It keeps the legacy top-level array for
compatibility. Legacy arrays do not qualify for `wip capture`.

The decoder rejects unknown fields, duplicate keys, trailing JSON, and input
larger than 1 MiB. See [PLAN.schema.json](PLAN.schema.json).

## Operational status

Status compares three exact trees: the caller index, source `HEAD`, and lane
tip. It classifies capture candidates, captured staged entries, foreign staged
entries, and empty lease scopes. These classes describe index state. They do
not authorize a worktree-source plan.

Status also validates the lane ref and latest capture intent. The JSON envelope
contains a stable lease context for all successful lane commands.

## Capture handoff

The handoff loader builds one unique chain of completed intents from the lane
base to the current tip. It validates every intent and commit object. A gap,
branch, cycle, ambiguity, or moved ref fails closed.

The version 2 output contains every segment, commit, source binding, and review
digest. A canonical `handoff_digest` binds the complete output. A handoff is
review-eligible only when every segment has its version 3 private-source and
review bindings.

The completed-intent path does not skip Git-object validation. The handoff
command holds the lane lock through chain validation, active-lease renewal, and
status construction. `capture --final` keeps the same lock through handoff
validation and release.

For a released lane, handoff acquires the existing regular lane lock without
creating state or rewriting the lock owner record. It verifies the opened file
against the path before and after lock acquisition.

The output contains no agent ID, session ID, absolute path, remote URL, or file
content. It explicitly states that capture does not authorize landing. A
separate review authority must persist the handoff and make any landing plan.

See [HANDOFF.schema.json](HANDOFF.schema.json) and
[KEP-0003](KEP-0003-agent-facing-capture-workflow.md).

## Schema contract validation

Tests validate real plan, status, legacy-plan, and handoff command data against
the published Draft 2020-12 schemas. They also require a strict schema to reject
invalid data.

The test-only validator is `github.com/santhosh-tekuri/jsonschema/v6` version
6.0.3. The dependency uses Apache-2.0 and supports Go 1.21 and newer. Version
6.0.3 was the current stable release on the 2026-08-18 review date. Tests load
only repository schema files. They do not enable remote schema loading in the
product runtime.
