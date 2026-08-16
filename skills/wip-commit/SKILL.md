---
name: wip-commit
description: Safely capture one agent's exact staged paths into an isolated local Git ref. Use for concurrent agents in one repository or checkout. Use for split Conventional Commits and linked worktrees.
---

# WIP Commit

Capture only the paths that belong to the current task. Keep the checked-out
branch, source `HEAD`, complete source index, and other agents' staged entries
unchanged.

## Inspect before changing state

1. Read the repository's agent instructions.
2. Run `git status --short --branch` and
   `git diff --cached --no-renames --name-only -z`. Parse the second command as
   NUL-delimited paths. Do not split its output on whitespace or newlines.
3. If a merge, rebase, cherry-pick, or revert is in progress, stop. Ask the
   operator to finish or cancel that Git operation.
4. Identify the exact paths owned by this task. Do not claim or stage a broad
   directory. When another agent can change a child path, use a narrower claim.
5. Run `wip version`. If the command is absent, build or install it by using
   this repository's documented method.
6. Run `wip --json doctor`. If it reports another coordination domain, stop.
   Preserve both state roots and select one domain before setup.

Never stash, reset, restore, clean, merge, push, or force-update a ref. Do not
delete another lane, worktree, lock, profile, intent, or receipt.

## Initialize one lane

For an interactive setup, run:

```text
wip init
```

The wizard can install the binary and this embedded portable skill. After it
validates and bootstraps the coordination domain, it writes a durable
initialization intent before worktree, lane, lease, profile, binary, or skill
changes. If setup stops, use the reported resume command. It pins the absolute
repository and worktree paths and the exact base commit. A matching partial
skill installation is resumable.

When agents edit the same checkout, accept the recommended `shared` mode. When
the task has a dedicated linked worktree, use `worktree` mode. Both modes use
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
  --no-install \
  --no-install-skill
```

Repeat `--path` for each disjoint claim. If `PATH_LEASE_CONFLICT` occurs, do
not override it. Narrow the claim or wait for the owner to release it.

Load the saved identity in an interactive shell:

```text
eval "$(wip env --lane <task-slug>)"
```

When shell environment changes are not suitable, pass `--lane`, `--agent`, and
`--session` explicitly.

## Stage and capture exact paths

Stage each owned path explicitly:

```text
git add -- <owned-path>
```

Inspect the staged names and diff before capture. Foreign staged entries can
remain in the source index.

Run `wip --json plan` for a read-only component split proposal. Review each
group for semantic intent and dependency closure. The proposed prefix is a
naming hint, not a commit message.

Use split commits by default. Interactive work can run `wip commit` and accept
or refine the proposed groups. Automation must use a reviewed JSON plan:

```text
wip --json commit --plan <plan.json>
```

Each group needs a distinct Conventional Commit message and an exact file
list. Add bounded commands in `verify` to the group that owns the affected code.
When one commit is a deliberate, coherent unit, use `--single --message`.
Read [references/automation.md](references/automation.md) before an automated
capture.

For long editing work, run `wip renew` before the 15-minute lease expires. Use
`wip claim --path <path>` before staging a newly owned path. `wip commit`
snapshots and renews the lane's complete active lease set during long hooks and
verification commands.

## Check the result

For a complete capture, require all of these facts in the JSON result:

- `ok` is `true`.
- `ref_updated` is `true`.
- `gate_outcome` is `passed`.
- `intent_state` is `complete`.

Also check that the reported groups, paths, messages, old commit, new
commit, and lane ref match the reviewed plan. A successful capture leaves the
captured entries staged in the source index by design.

If a failed result reports `ref_updated: true`, preserve its exact `plan_id`
and `plan_digest`. Run `wip reconcile` with both values. Do not edit an intent
or guess a digest. Do not rerun the plan until reconciliation proves that the
ref did not move.

## Finish without landing

Run `wip status`, record the lane ref and new commit, and then run:

```text
wip release
```

Release removes the active coordination claim and preserves the lane ref and
commits. It does not land, merge, or push them. Hand the exact ref and commit
to the repository's separate review and landing process.

Run `wip doctor` before you archive old state. Use `wip archive` without
`--apply` first. Reuse the exact cutoff and plan digest only after review. If
apply returns a prepared receipt, use its exact ID with `wip archive --resume`.
