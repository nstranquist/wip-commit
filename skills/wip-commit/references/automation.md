# Automated capture

Use JSON mode so the caller can check typed evidence. Do not accept a zero exit
status as the only proof of capture.

## Preflight and setup

Run `wip --json doctor` before setup. Stop if it reports an unsafe state record,
an incomplete recovery, or another coordination domain.

Inspect the staged path set with
`git diff --cached --no-renames --name-only -z`. Parse NUL-delimited paths so
tabs, spaces, and newlines in file names stay exact.

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

Run `wip --json status` after staging. Check `capture_candidates`,
`foreign_staged`, `lease_expires_at`, `ref_health`, and `last_intent`.

Write one no-overwrite draft:

```text
wip --json plan --out plan.json
```

Review the component groups and path ownership. Add messages and bounded
verification commands. Then set `state` to `reviewed`.

## Review the generated plan

Edit only the object that `wip plan --out` created. Do not copy a partial JSON
example or create a second plan format. Add each `message` and optional typed
`verify` command. Give each verification command a bounded `timeout_ms`. Set
`state` to `reviewed` after the complete object passes review. Keep the
generated lane, source, index, lease, policy, guidance, path, group key, scope,
prefix, and rationale fields exact. Keep every staged path in exactly one
group.

Capture the exact reviewed file:

```text
wip --json capture --plan plan.json
```

## Result checks

Parse the single JSON envelope. Require `ok: true`. In `data.capture`, require
`ref_updated: true`, `gate_outcome: "passed"`, and `intent_state: "complete"`.

In `data.handoff`, require `capture_only: true` and
`landing_authorized: false`. Check the ref, tip, tree, commit sequence, and
paths against the reviewed plan.

If the command fails before the compare-and-swap ref update, fix the reported
cause and run the full plan again. If it fails after the ref update, use only
the returned `plan_id` and `plan_digest` with `wip reconcile`.

Do not run two captures for the same lane at once. Separate lanes with
disjoint leases can capture in parallel. `wip capture` renews the complete
active set during hooks and verification.

## State maintenance

Run `wip --json doctor` before archival. Archive only a reviewed preview. Reuse
the exact `cutoff` and `plan_digest` with `--apply --yes`. If a failed apply
returns a prepared receipt, continue only with:

```text
wip --json archive --resume <archive-id> --apply --yes
```

Archive and restore preserve all lane refs and commits.
