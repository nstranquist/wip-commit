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
| `LEASE_HEARTBEAT_FAILED` | Capture could not refresh its exact lease set | If `ref_updated` is true, run `wip reconcile` with the returned plan evidence. Otherwise, run `wip doctor` |
| `LANE_RELEASE_RECOVERY_REQUIRED` | Lease release started before the lane manifest changed | Rerun the exact `wip release`; do not claim, renew, or capture on that lane |
| `RELEASED_LANE_ACTIVE_LEASE` | A released or aborted lane still has an active stored lease | Stop. Preserve the records and inspect with `wip doctor` |
| `WORKTREE_CONFLICT` | An exclusive worktree binding conflicts | Use another linked worktree or release the active lane |
| `LOCK_TIMEOUT` | Another process held a required lock | Wait, inspect `wip status`, and retry |
| `COORDINATION_DOMAIN_CONFLICT` | Public and legacy coordination state cannot share one repository | Stop all lane processes. Preserve both state roots, then select one domain |

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
| `GIT_FAILED` | Git could not inspect the staged path set | Fix the reported index or repository error, then run the exact command again |
| `DIFF_CHECK_FAILED` | Git could not complete the staged whitespace check | Fix the reported Git error. Do not treat the check as passed |

## Recovery errors

| Code | Meaning | Action |
| --- | --- | --- |
| `PLAN_NOT_APPLIED` | The intent exists but the ref stayed at the old commit | Run the complete plan again from current staged state |
| `INTENT_NOT_FOUND` | The supplied plan ID has no durable record | Use the exact JSON result from the failed capture |
| `INTENT_DIGEST_MISMATCH` | The record or supplied digest differs | Stop. Do not edit the receipt or guess a digest |
| `INVALID_INTENT` | A digest-valid receipt has invalid identities, bounds, paths, or commit structure | Stop. Preserve the receipt and lane ref; do not mark or reconcile it |
| `CAPTURE_RECEIPT_MISMATCH` | Git objects do not match immutable evidence | Stop and inspect the repository. Do not force reconciliation |

## State compatibility errors

| Code | Meaning | Action |
| --- | --- | --- |
| `MIGRATION_REQUIRED` | A state directory or record uses an unsupported schema | Stop. Use the compatible `wip` release and follow [STATE-COMPATIBILITY.md](STATE-COMPATIBILITY.md) |
| `STORE_FAILED` | Version 1 lane or lease state is malformed or unsafe to read | Stop. Preserve the state and inspect the reported file condition |
| `PROFILE_READ_FAILED` | A version 1 profile is malformed or has the wrong identity | Pass the full identity only after you confirm the lane owner |
| `INTENT_READ_FAILED` | A version 1 intent is malformed or unsafe to read | Stop. Preserve the intent and lane ref; do not guess recovery data |
| `INTENT_WRITE_FAILED` | A capture intent cannot fit or cannot be published | The lane ref did not move. Reduce the plan size or fix the reported storage error |
| `INIT_INTENT_FAILED` | An initialization receipt is malformed or changed | Stop. Preserve the receipt, worktree, lane ref, lease, profile, and installed files |
| `INSTALL_CONFLICT` | The binary target exists with different bytes or an unsafe type | Preserve the target. Select another `--install-dir`, or review the target separately |
| `INSTALL_FAILED` | The binary could not be published or verified | Preserve any complete target and retry the exact initialization command |
| `SKILL_INSTALL_CONFLICT` | The target skill directory contains different content | Select another `--skill-dir`, or remove the target only after a separate review |
| `SKILL_INSTALL_FAILED` | The embedded skill could not be published or verified | Preserve the matching partial bundle and retry the exact initialization command |

## Inspection and archival errors

| Code | Meaning | Action |
| --- | --- | --- |
| `ARCHIVE_PLAN_REQUIRED` | Apply has no reviewed cutoff or digest | Run the preview, then reuse its exact `--cutoff` and `--plan-digest` |
| `ARCHIVE_PLAN_MOVED` | The eligible record set differs from the preview | Run a new preview. Do not reuse the old digest |
| `ARCHIVE_EMPTY` | The reviewed plan has no exact eligible candidate | Run a new preview with an older cutoff or a valid lane |
| `ARCHIVE_REFUSED` | A lane, lease, or ref is not safe to archive | Run `wip doctor` and repair the reported state first |
| `ARCHIVE_CONFLICT` | Live and archived records conflict | Preserve both copies and inspect the receipt before retry |
| `ARCHIVE_NOT_FOUND` | A resume or restore receipt does not exist | Use the exact receipt ID from the earlier result. The command does not initialize state |
| `ARCHIVE_FAILED` | A receipt, batch layout, record binding, or file placement is invalid | Preserve the live and archived records. Use `wip doctor` and do not move files by hand |

If archive apply returns a receipt with state `prepared`, preserve the state
directory and run `wip archive --resume <archive-id> --apply --yes`. Do not run
a new apply plan over a partial receipt.

If restore stops with state `restoring`, rerun `wip archive --restore
<archive-id> --apply --yes`. The command validates every split file placement
before it continues.

Doctor can also report typed findings such as
`LANE_CREATE_RECOVERY_REQUIRED`, `LEASE_OWNER_MISMATCH`,
`LEASE_REFERENCE_UNLISTED`, `LANE_RELEASE_RECOVERY_REQUIRED`,
`RELEASED_LANE_ACTIVE_LEASE`, and `ARCHIVE_RECOVERY_REQUIRED`. Preserve the
reported records and follow the finding message. Do not edit a receipt or lease
by hand.

`wip doctor` returns structured data with `ok: true`. It returns exit code 1
when an error or recovery finding needs operator action.

If a failed `commit` result says `ref_updated: true`, preserve its `plan_id` and
`plan_digest`, then run `wip reconcile` with both values. Do not rerun the plan
until reconciliation says that the ref was not applied.
