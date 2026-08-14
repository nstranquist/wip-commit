---
name: wip-commit
description: Safely capture one agent's exact staged paths into an isolated local Git ref. Use when concurrent agents share a repository or checkout, when work needs split Conventional Commits, or when a linked worktree needs the same guarded capture flow.
---

# WIP Commit

Capture only the paths that belong to the current task. Keep the checked-out
branch, source `HEAD`, complete source index, and other agents' staged entries
unchanged.

## Inspect before changing state

1. Read the repository's agent instructions.
2. Run `git status --short --branch` and `git diff --cached --name-only`.
3. Stop if a merge, rebase, cherry-pick, or revert is in progress. Ask the
   operator to finish or cancel that Git operation.
4. Identify the exact paths owned by this task. Do not claim or stage a broad
   directory when another agent can change a child path.
5. Run `wip version`. If the command is absent, build or install it by using
   this repository's documented method.

Never stash, reset, restore, clean, merge, push, or force-update a ref. Do not
delete another lane, worktree, lock, profile, intent, or receipt.

## Initialize one lane

For an interactive setup, run:

```text
wip init
```

Accept the recommended `shared` mode when agents edit the same checkout. Use
`worktree` mode when the task has a dedicated linked worktree. Both modes use
the same private-index capture transaction and write only to an agent-owned
local ref.

For automation, make the identity and paths explicit:

```text
wip --json init \
  --mode shared \
  --lane <task-slug> \
  --agent <agent-id> \
  --session <session-id> \
  --path <owned-path> \
  --non-interactive \
  --no-install
```

Repeat `--path` for each disjoint claim. If `PATH_LEASE_CONFLICT` occurs, do
not override it. Narrow the claim or wait for the owner to release it.

Load the saved identity in an interactive shell:

```text
eval "$(wip env --lane <task-slug>)"
```

Pass `--lane`, `--agent`, and `--session` explicitly when shell environment
changes are not suitable.

## Stage and capture exact paths

Stage each owned path explicitly:

```text
git add -- <owned-path>
```

Inspect the staged names and diff before capture. Foreign staged entries may
remain in the source index.

Use split commits by default. Interactive work can run `wip commit` and accept
or refine the proposed groups. Automation must use a reviewed JSON plan:

```text
wip --json commit --plan <plan.json>
```

Each group needs a distinct Conventional Commit message and an exact file
list. Add bounded verify commands to the group that owns the affected code.
Use `--single --message` only when one commit is a deliberate, coherent unit.
Read [references/automation.md](references/automation.md) before an automated
capture.

For long work, run `wip renew` before the 15-minute lease expires. Use
`wip claim --path <path>` before staging a newly owned path.

## Verify the result

Treat a capture as complete only when the JSON result reports all of these
facts:

- `ok` is `true`.
- `ref_updated` is `true`.
- `gate_outcome` is `passed`.
- `intent_state` is `complete`.

Also confirm that the reported groups, paths, messages, old commit, new
commit, and lane ref match the reviewed plan. A successful capture leaves the
captured entries staged in the source index by design.

If a failed result reports `ref_updated: true`, preserve its exact `plan_id`
and `plan_digest`. Run `wip reconcile` with both values. Do not edit an intent,
guess a digest, or rerun the plan until reconciliation proves that the ref did
not move.

## Finish without landing

Run `wip status`, record the lane ref and new commit, and then run:

```text
wip release
```

Release removes the active coordination claim and preserves the lane ref and
commits. It does not land, merge, or push them. Hand the exact ref and commit
to the repository's separate review and landing process.
