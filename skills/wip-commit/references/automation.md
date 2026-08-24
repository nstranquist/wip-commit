# Automated capture

Use JSON mode so the caller can check typed evidence. Do not accept a zero exit
status as the only proof of capture.

## Preflight and setup

Run `wip --json contract`, then `wip --json doctor` before setup. Stop if the
contract is incompatible or doctor reports an unsafe state record, an
incomplete recovery, or another coordination domain. If
`data.upgrade_required` is true, complete a reviewed state-upgrade preview and
apply before lane setup. For healthy initialized state with no required
upgrade, run `wip --json status --observe`. `LANE_NOT_ACTIVE` selects a new
standalone `wip init` lane. It does not authorize a legacy fallback.

Validate the complete contract document and portable payload digest. If a
binary or skill upgrade is required, review the target-specific `wip --json
upgrade` preview and apply only its exact digest. Do not upgrade more than one
target in one command.

Inspect the worktree and staged path sets. Parse NUL-delimited Git output so
tabs, spaces, and newlines in file names stay exact. Do not use the full staged
index as implicit authority.

Use explicit setup values. Use one linked worktree for each automated task:

```text
wip --json init \
  --mode worktree \
  --create-worktree <absolute-worktree-path> \
  --lane <task-slug> \
  --agent <agent-id> \
  --session <session-id> \
  --path <owned-path> \
  --non-interactive \
  --no-install \
  --no-install-skill
```

If initialization fails after it writes an intent, parse `intent_id`,
`intent_state`, `completed_steps`, and `recovery`. Retry only the exact command
in `recovery`.

Run `wip --json status`. Check `capture_candidates`,
`foreign_staged`, `lease_expires_at`, `ref_health`, and `last_intent`.

Write one no-overwrite draft:

```text
wip --json plan \
  --source worktree_snapshot \
  --untracked forbid \
  --path <owned-path> \
  --observe

wip --json plan \
  --source worktree_snapshot \
  --untracked forbid \
  --path <owned-path> \
  --out plan.json
```

The observed command must change no repository bytes. It cannot use `--out`.
Use it to check the selected source before the operational plan renews leases
and creates `plan.json`.

Use `--untracked include` only when the reviewed source must contain the exact
untracked paths. For a controlled shared-index exception, use `--source
staged_index --untracked not_applicable` after you stage only the owned paths.

Review the component groups and path ownership. Add messages and bounded
verification commands. Then set `state` to `reviewed`.

## Review the generated plan

Edit only the object that `wip plan --out` created. Do not copy a partial JSON
example or create a second plan format. Add each `message` and optional typed
`verify` command. Give each verification command a bounded `timeout_ms`. Set
`state` to `reviewed` after the complete object passes review. Keep the
generated lane, source mode, untracked policy, source digest, source scopes,
lease, policy, guidance, path, group key, scope, prefix, and rationale fields
exact. Keep every source path in exactly one group.

Capture the exact reviewed file:

```text
wip --json capture --plan plan.json
```

Do not use `wip commit --dry-run` as gate evidence. That command validates the
candidate structure without executing hooks or verification commands. Its
schema `3.0.0` result reports `gate_outcome: "skipped"`, a `no-exec` policy,
and zero executed counts. It does not renew the lease or emit a handoff.

## Result checks

Parse the single JSON envelope. Require `ok: true`. In `data.capture`, require
`ref_updated: true`, `gate_outcome: "passed"`, and `intent_state: "complete"`.

In `data.handoff`, require `capture_only: true`, `landing_authorized: false`,
`review_eligible: true`, and an exact `handoff_digest`. Validate every segment
and its intent digest. Require a continuous parent chain from the lane base to
the lane tip. Check the final tree, commit sequences, and paths against all
reviewed plans. The handoff does not authorize landing.

If the command fails before the compare-and-swap ref update, fix the reported
cause and run the full plan again. If it fails after the ref update, use only
the returned `plan_id` and `plan_digest` with `wip reconcile`.

Do not run two captures for the same lane at once. Separate lanes with
disjoint leases can capture in parallel. `wip capture` renews the complete
active set during hooks and verification.

## State maintenance

Run `wip --json doctor` before archival. Archive only a reviewed preview. Reuse
the exact `cutoff` and `plan_digest` with `--apply --yes`, and repeat every lane
in the preview's `lane_filters`. If a failed apply returns a prepared receipt,
continue only with:

```text
wip --json archive --resume <archive-id> --apply --yes
```

Archive and restore preserve all lane refs and commits.
