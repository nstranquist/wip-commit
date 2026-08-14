# Error and recovery guide

`wip --json` returns one object with `ok`, `action`, and either `data` or
`error`. The error object has a stable `code` and a human-readable `message`.

## Coordination errors

| Code | Meaning | Action |
| --- | --- | --- |
| `LANE_NOT_ACTIVE` | No active lane matches this checkout and identity | Run `wip init`, or set `WIP_LANE`, `WIP_AGENT`, and `WIP_SESSION` |
| `LANE_AMBIGUOUS` | More than one shared lane matches | Pass `--lane` or load `wip env --lane <id>` |
| `PATH_LEASE_CONFLICT` | Another active lane owns an overlapping path | Select disjoint paths, or wait for that owner to release or expire |
| `LEASE_EXPIRED` | The lease deadline passed | Run `wip init` again or claim the paths again |
| `WORKTREE_CONFLICT` | An exclusive worktree binding conflicts | Use another linked worktree or release the active lane |
| `LOCK_TIMEOUT` | Another process held a required lock | Wait, inspect `wip status`, and retry |

## Capture errors

| Code | Meaning | Action |
| --- | --- | --- |
| `SPLIT_PLAN_REQUIRED` | Automation did not provide a split plan | Pass `--plan`, or explicitly pass `--single --message` |
| `INVALID_COMMIT_MESSAGE` | A message failed the naming policy | Use a concrete Conventional Commit subject of at most 72 characters |
| `SOURCE_INDEX_MOVED` | A selected staged entry changed during capture | Inspect the staged paths and rerun the complete plan |
| `SOURCE_HEAD_MOVED` | The checkout moved from the lane base | Stop and create a new lane from the intended base |
| `REF_MOVED` | The lane ref did not match the expected cursor | Inspect the lane and receipt; do not force-update it |
| `PLAN_SCOPE_MISMATCH` | A hook changed an unplanned private-index path | Fix the hook or expand the reviewed plan and lease |
| `HOOK_SOURCE_MOVED` | A hook path changed during capture | Inspect the hook and rerun from a stable source |
| `HOOK_TIMEOUT` | A hook exceeded its limit | Fix the hook or pass a reviewed larger timeout |
| `VERIFY_FAILED` | A candidate-tree command failed | Fix the candidate change and rerun the whole plan |
| `VERIFY_TIMEOUT` | A verify command exceeded its limit | Fix the command or pass a reviewed larger timeout |

## Recovery errors

| Code | Meaning | Action |
| --- | --- | --- |
| `PLAN_NOT_APPLIED` | The intent exists but the ref stayed at the old commit | Run the complete plan again from current staged state |
| `INTENT_NOT_FOUND` | The supplied plan ID has no durable record | Use the exact JSON result from the failed capture |
| `INTENT_DIGEST_MISMATCH` | The record or supplied digest differs | Stop. Do not edit the receipt or guess a digest |
| `CAPTURE_RECEIPT_MISMATCH` | Git objects do not match immutable evidence | Stop and inspect the repository. Do not force reconciliation |

If a failed `commit` result says `ref_updated: true`, preserve its `plan_id` and
`plan_digest`, then run `wip reconcile` with both values. Do not rerun the plan
until reconciliation says that the ref was not applied.
