# Architecture

## Data model

All coordination state is below the repository's Git common directory:

```text
<git-common-dir>/wip/v1/
  lanes/<lane>.json
  leases/<lease>.json
  profiles/<lane>.json
  intents/<plan>.json
  locks/lane-<lane>.lock
  locks/leases.lock
```

Each checkout has its own Git index. All linked worktrees share the coordination
store and refs through the Git common directory.

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

## Lock order

Commands use this lock order:

1. lane lock;
2. lease-registry lock.

No command acquires those locks in the opposite order. Different lanes can run
capture work in parallel. One lane is serialized.

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
