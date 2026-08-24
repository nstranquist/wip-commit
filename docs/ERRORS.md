# Error and recovery guide

`wip --json` returns one object with `ok`, `action`, and either `data` or
`error`. Successful lane commands also return `context` with lease state.

## Coordination errors

| Code | Meaning | Action |
| --- | --- | --- |
| `LANE_NOT_ACTIVE` | No active lane matches this checkout and identity | Run `wip init`, or set `WIP_LANE`, `WIP_AGENT`, and `WIP_SESSION` |
| `LANE_AMBIGUOUS` | More than one shared lane matches | Pass `--lane` or load `wip env --lane <id>` |
| `PATH_LEASE_CONFLICT` | Another active lane owns an overlapping path | Select disjoint paths, or wait for that owner to release or expire |
| `LEASE_EXPIRED` | The lease deadline passed | Run `wip init` again or claim the paths again |
| `LEASE_HEARTBEAT_FAILED` | Capture failed to refresh its exact lease set | If `ref_updated` is true, run `wip reconcile` with the returned plan evidence. Otherwise, run `wip doctor` |
| `LANE_RELEASE_RECOVERY_REQUIRED` | Lease release started before the lane manifest changed | Rerun the exact `wip release`. Do not claim, renew, or capture on that lane |
| `RELEASED_LANE_ACTIVE_LEASE` | A released or aborted lane still has an active stored lease | Stop. Preserve the records and inspect with `wip doctor` |
| `WORKTREE_CONFLICT` | An exclusive worktree binding conflicts | Use another linked worktree or release the active lane |
| `WORKTREE_MISMATCH` | The selected lane belongs to another checkout | Change to the lane's recorded checkout; do not export or use its identity elsewhere |
| `LOCK_TIMEOUT` | Another process held a required lock | The default fails fast. Inspect `wip status` and retry later. Use an explicit bounded `--lock-wait` only when blocking is intended |
| `COORDINATION_DOMAIN_CONFLICT` | Public and legacy coordination state cannot share one repository | Stop all lane processes. Preserve both state roots, then select one domain |

## Capture errors

| Code | Meaning | Action |
| --- | --- | --- |
| `SPLIT_PLAN_REQUIRED` | Automation did not provide a split plan | Pass `--plan`, or explicitly pass `--single --message` |
| `REVIEWED_PLAN_REQUIRED` | `wip capture` did not receive one version 3 plan | Run `wip plan --out <file>`, review that file, then pass it to `wip capture` |
| `REVIEW_PLAN_DIGEST_MISMATCH` | The reviewed plan differs from the exact digest supplied by NDev | Stop. Preserve the plan and request a new review digest |
| `PLAN_NOT_REVIEWED` | The version 3 plan still has draft state | Add every message, review every group, then set `state` to `reviewed` |
| `STALE_PLAN` | A bound lane, source, path, lease, or policy value changed | Create and review a new plan from the selected source |
| `PLAN_EXISTS` | `wip plan --out` found an existing destination | Select a new output path or review the existing file separately |
| `INVALID_SOURCE_POLICY` | The source mode and untracked policy are not compatible | Use `not_applicable` with `staged_index`. Use `forbid` or `include` with `worktree_snapshot` |
| `UNTRACKED_PATH_FORBIDDEN` | A selected worktree scope contains an untracked path under the `forbid` policy | Review the untracked paths. Use `include` only when all selected paths belong in the capture |
| `SOURCE_SNAPSHOT_FAILED` | Git or the filesystem failed to build the private source snapshot | Preserve the caller index. Fix the reported source error, then create a new plan |
| `MESSAGE_POLICY_REJECTED` | A message failed the named lane policy | Follow the policy in the JSON `message_policy` receipt |
| `INVALID_MESSAGE_POLICY` | A policy name or receipt is unsupported | Fix `.wip-commit.json` or use a supported `--message-policy` value |
| `INVALID_COMMIT_MESSAGE` | A message failed kernel byte or size safety | Remove unsafe controls, fix the subject, or reduce the message size |
| `SOURCE_MOVED` | The selected source no longer matches the reviewed digest | Inspect the selected source paths and create a new reviewed plan |
| `SOURCE_HEAD_MOVED` | The checkout moved from the lane base | Stop and create a new lane from the intended base |
| `REF_MOVED` | The lane ref did not match the expected cursor | Inspect the lane and receipt. Do not force-update it |
| `PLAN_SCOPE_MISMATCH` | A hook changed an unplanned private-index path | Fix the hook or expand the reviewed plan and lease |
| `HOOK_SOURCE_MOVED` | A hook path changed during capture | Inspect the hook and rerun from a stable source |
| `HOOK_TIMEOUT` | A hook exceeded its limit | Fix the hook or pass a reviewed larger timeout |
| `VERIFY_FAILED` | A candidate-tree command failed | Fix the candidate change and rerun the whole plan |
| `VERIFY_TIMEOUT` | A verify command exceeded its limit | Fix the command or pass a reviewed larger timeout |
| `GIT_FAILED` | Git failed to inspect the selected source or Git objects | Fix the reported repository error, then run the exact command again |
| `DIFF_CHECK_FAILED` | Git failed to complete the candidate whitespace check | Fix the reported Git error. Do not treat the check as passed |

## Recovery errors

| Code | Meaning | Action |
| --- | --- | --- |
| `PLAN_NOT_APPLIED` | The intent exists but the ref stayed at the old commit | Create a new plan from the current selected source |
| `INTENT_NOT_FOUND` | The supplied plan ID has no durable record | Use the exact JSON result from the failed capture |
| `INTENT_DIGEST_MISMATCH` | The record or supplied digest differs | Stop. Do not edit the receipt or guess a digest |
| `INVALID_INTENT` | A digest-valid receipt has invalid identities, bounds, paths, or commit structure | Stop. Preserve the receipt and lane ref. Do not mark or reconcile it |
| `CAPTURE_RECEIPT_MISMATCH` | Git objects do not match immutable evidence | Stop and inspect the repository. Do not force reconciliation |
| `HANDOFF_NOT_READY` | The lane has no complete validated capture intent | Reconcile an applied intent or complete a reviewed capture first |
| `HANDOFF_CHAIN_INCOMPLETE` | No continuous completed intent chain reaches the lane tip | Preserve all intents and refs. Reconcile the missing segment or inspect the gap |
| `HANDOFF_CHAIN_AMBIGUOUS` | More than one completed intent continues the same parent | Stop. Preserve all intents and refs for a separate recovery review |
| `HANDOFF_TIP_MISMATCH` | The supplied intent does not name the lane tip segment | Use no selector for the complete chain, or supply the exact tip intent evidence |
| `HANDOFF_TOO_LARGE` | The cumulative handoff exceeds a segment, commit, or byte bound | Stop. Preserve the lane and use a separately reviewed bounded handoff procedure |

## State compatibility errors

| Code | Meaning | Action |
| --- | --- | --- |
| `MIGRATION_REQUIRED` | A state directory or record uses an unsupported schema | Stop. Use the compatible `wip` release and follow [STATE-COMPATIBILITY.md](STATE-COMPATIBILITY.md) |
| `STORE_FAILED` | Version 1 lane or lease state is malformed or unsafe to read | Stop. Preserve the state and inspect the reported file condition |
| `PROFILE_READ_FAILED` | A supported profile is malformed, has the wrong identity, or has an invalid policy | Preserve the profile and confirm the lane owner and policy |
| `INTENT_READ_FAILED` | A supported intent is malformed or unsafe to read | Stop. Preserve the intent and lane ref. Do not guess recovery data |
| `INTENT_WRITE_FAILED` | A capture intent cannot fit or cannot be published | The lane ref did not move. Reduce the plan size or fix the reported storage error |
| `INIT_INTENT_FAILED` | An initialization receipt is malformed or changed | Stop. Preserve the receipt, worktree, lane ref, lease, profile, and installed files |
| `INSTALL_CONFLICT` | The binary target exists with different bytes or an unsafe type | Preserve the target. Select another `--install-dir`, or review the target separately |
| `INSTALL_FAILED` | Binary publication or verification failed | Preserve any complete target and retry the exact initialization command |
| `SKILL_INSTALL_CONFLICT` | The target skill directory contains different content | Select another `--skill-dir`, or remove the target only after a separate review |
| `SKILL_INSTALL_FAILED` | Embedded skill publication or verification failed | Preserve the matching partial bundle and retry the exact initialization command |
| `STATE_NOT_INITIALIZED` | A state upgrade has no owned domain marker | Run `wip init`. Do not use upgrade as first initialization |
| `UPGRADE_BLOCKED` | State has an active lane, pending recovery, or unhealthy record | Stop all `wip` processes, release lanes, and make `wip doctor` healthy before a new preview |
| `UPGRADE_PLAN_DIGEST_MISMATCH` | Apply does not match the current byte-bound preview | Review a new preview. Do not reuse the old digest |
| `UPGRADE_PLAN_MOVED` | The target bytes changed after preview or while acquiring the lock | Preserve the target and review a new preview |
| `UPGRADE_TARGET_MISSING` | Binary or skill upgrade target does not exist | Use the no-overwrite first-install path in `wip init` |
| `UPGRADE_CONFLICT` | The upgrade target has an unsafe type, unexpected entry, or excessive size | Preserve and inspect the target. Do not replace it automatically |
| `UPGRADE_FAILED` | Atomic replacement or post-write verification failed | Preserve the target and rerun preview. A new reviewed upgrade can repair a partial skill bundle that fails digest validation |

## Coordination migration errors

| Code | Meaning | Action |
| --- | --- | --- |
| `MIGRATION_PRODUCER_INVALID`, `MIGRATION_PRODUCER_MISMATCH` | The executable identity is missing, invalid, or different from the reviewed producer | Use the exact source-stamped executable that created the reviewed plan |
| `MIGRATION_PLAN_BLOCKED`, `MIGRATION_PLAN_INVALID`, `MIGRATION_PLAN_DIGEST_MISMATCH`, `MIGRATION_PLAN_TOO_LARGE` | The migration plan is blocked, malformed, moved, or outside its bound | Preserve both roots. Create and review a new preview |
| `MIGRATION_PRESTATE_CHANGED`, `MIGRATION_SOURCE_MOVED`, `MIGRATION_REF_MOVED`, `MIGRATION_CANONICAL_MOVED`, `MIGRATION_REPOSITORY_MISMATCH` | A bound repository, source, destination, or ref changed after preview | Restore the writer freeze. Create and review a new preview unless a journal requires resume |
| `MIGRATION_SOURCE_INVALID`, `MIGRATION_SOURCE_UNSAFE`, `MIGRATION_SOURCE_STATE_INVALID` | The legacy source layout, file type, or record state is unsafe | Preserve the source. Repair it only through a separate reviewed recovery plan |
| `MIGRATION_DESTINATION_INVALID`, `MIGRATION_DESTINATION_UNSAFE` | The canonical destination contains an invalid or unsafe path | Preserve the destination. Do not replace or remove it automatically |
| `MIGRATION_LOCKED`, `MIGRATION_IN_PROGRESS`, `MIGRATION_FENCE_CONFLICT` | Another migration owns the lock or writer fence | Inspect migration status. Resume only the exact owning plan |
| `MIGRATION_RESUME_REQUIRED`, `MIGRATION_INTERRUPTED` | A durable migration journal requires forward recovery | Run `--resume` with the exact plan, digest, producer, and writer freeze |
| `MIGRATION_NOT_STARTED`, `MIGRATION_ROLLED_BACK` | The supplied resume or rollback action has no resumable journal | Create and review a new preview |
| `MIGRATION_ROLLBACK_REFUSED`, `MIGRATION_ROLLBACK_INVALID`, `MIGRATION_ROLLBACK_FAILED` | Rollback is unsafe, moved, or failed | Preserve all migration artifacts. Resume forward if the legacy source moved |
| `MIGRATION_ARCHIVE_CONFLICT`, `MIGRATION_ARCHIVE_INVALID`, `MIGRATION_ARCHIVE_MISSING`, `MIGRATION_ARCHIVE_MOVED`, `MIGRATION_ARCHIVE_UNSAFE` | The legacy archive is missing, changed, unsafe, or conflicts with another path | Preserve the archive. Do not reconstruct or delete it by hand |
| `MIGRATION_JOURNAL_INVALID`, `MIGRATION_JOURNAL_FAILED`, `MIGRATION_RECEIPT_INVALID`, `MIGRATION_STATUS_INVALID` | Durable migration evidence is invalid or its write failed | Stop all writers. Preserve the journal, plan, receipt, roots, and refs |
| `MIGRATION_PLAN_FAILED`, `MIGRATION_PREPARE_FAILED`, `MIGRATION_SOURCE_MOVE_FAILED`, `MIGRATION_SEAL_FAILED`, `MIGRATION_FENCE_FAILED` | A migration file or directory operation failed | Preserve all artifacts. Inspect status, then resume only the exact plan |
| `MIGRATION_SOURCE_NOT_REMOVED` | A completed migration still has an unsealed or duplicate legacy source | Stop all writers. Preserve both locations and inspect the exact migration receipt |

## Inspection and archival errors

| Code | Meaning | Action |
| --- | --- | --- |
| `ARCHIVE_PLAN_REQUIRED` | Apply has no reviewed cutoff or digest | Run the preview, then reuse its exact `--cutoff` and `--plan-digest` |
| `ARCHIVE_PLAN_MOVED` | The eligible record set differs from the preview | Repeat every `--lane` filter from the preview, or run a new preview. Do not reuse a digest after records change |
| `ARCHIVE_EMPTY` | The reviewed plan has no exact eligible candidate | Run a new preview with an older cutoff or a valid lane |
| `ARCHIVE_REFUSED` | A lane, lease, or ref is not safe to archive | Run `wip doctor` and repair the reported state first |
| `ARCHIVE_CONFLICT` | Live and archived records conflict | Preserve both copies and inspect the receipt before retry |
| `ARCHIVE_NOT_FOUND` | A resume or restore receipt does not exist | Use the exact receipt ID from the earlier result. The command does not initialize state |
| `ARCHIVE_FAILED` | A receipt, batch layout, record binding, file placement, or later archive owner is invalid | Preserve the live and archived records. Use `wip doctor` and do not move files by hand |

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
