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
5. Run `wip --json version`. Record its exact revision and source state. Do not
   use an unstamped binary with existing coordination state. If the command is
   absent or has no revision, build or install a source-stamped binary by using
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

Use the recommended `worktree` mode for one agent per task. Use `shared` mode
only when lanes must use the same checkout and index. Both modes use the same
private-index capture transaction.

For automation, make the identity and paths explicit:

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

Repeat `--path` for each disjoint claim. If `PATH_LEASE_CONFLICT` occurs, do
not override it. Narrow the claim or wait for the owner to release it.

Initialization can create a linked worktree. Continue in the exact worktree
reported by the command. Run `wip env` and all later lane commands there. A
call from another checkout fails before lease renewal.

Load the saved identity in an interactive shell:

```text
eval "$(wip env --lane <task-slug>)"
```

Run `wip env` from the checkout recorded for the lane. A worktree-bound lane
must return `WORKTREE_MISMATCH` from any other checkout without renewing its
lease.

When shell environment changes are not suitable, pass `--lane`, `--agent`, and
`--session` explicitly.

## Stage and capture exact paths

Stage each owned path explicitly:

```text
git add -- <owned-path>
```

Inspect the staged names and diff before capture. Foreign staged entries can
remain in the source index.

Run the operational status before each plan:

```text
wip --json status
wip --json plan --out <plan.json>
```

Review each group for intent and dependency closure. Add a message to every
group. Then set the top-level `state` to `reviewed`.

Capture the reviewed plan:

```text
wip --json capture --plan <plan.json>
```

Each group needs an exact file list. The lane profile selects the message
policy. Add bounded `verify` commands to the group that owns the code.

Use `wip commit --single --message` only for a deliberate one-commit
compatibility capture. Do not use it in the guided automation path.
Read [references/automation.md](references/automation.md) before an automated
capture.

Authenticated `status`, `env`, `claim`, `plan`, `commit`, `capture`, and
`handoff` renew the complete active lease set. A released-lane handoff remains
a pure read. Use `--observe` for a status or environment read that must not
renew. Use `wip claim --path <path>` before staging a newly owned path.

## Check the result

For a complete guided capture, require all of these facts in the JSON result:

- `ok` is `true`.
- `data.capture.ref_updated` is `true`.
- `data.capture.gate_outcome` is `passed`.
- `data.capture.intent_state` is `complete`.
- `data.handoff.capture_only` is `true`.
- `data.handoff.landing_authorized` is `false`.
- `data.handoff.reviewed_plan_digest` is non-empty.

Check that the handoff ref, tip, tree, messages, and paths match the reviewed
plan. A successful capture leaves captured entries staged by design.

If a failed result reports `ref_updated: true`, preserve its exact `plan_id`
and `plan_digest`. Run `wip reconcile` with both values. Do not edit an intent
or guess a digest. Do not rerun the plan until reconciliation proves that the
ref did not move.

## Run the product benchmark

If the task changes a safety contract, run the benchmark from the repository
root:

```text
go run ./skills/wip-commit/benchmark/regression.go
```

Require all 16 checks and the reviewed baseline comparison to pass. The runner
uses disposable repositories and makes no network or AI calls.

Run the weakened-candidate proof after benchmark or harness changes:

```text
go run ./skills/wip-commit/benchmark/regression.go --prove-weakened
```

Do not change `skills/wip-commit/benchmark/baseline.json` to admit a failure.
The runner writes mutable reports outside the repository.

## Finish without landing

Run `wip --json handoff` to validate the complete capture receipt. Then run:

```text
wip release
```

Release removes the active coordination claim and preserves the lane ref and
commits. It does not land or merge them. Give the typed handoff to the
repository's separate review and landing process.

For the last capture, `wip --json capture --final --plan <plan.json>` performs
the validated handoff and lane release under one lane lock. It does not
authorize landing.

Run `wip doctor` before you archive old state. Use `wip archive` without
`--apply` first. Reuse the exact cutoff and plan digest only after review, and
repeat every `--lane` filter from the preview. JSON previews return the sorted
filters as `lane_filters`. If apply returns a prepared receipt, use its exact
ID with `wip archive --resume`.
