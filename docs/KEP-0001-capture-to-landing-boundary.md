# KEP-0001: Capture-to-landing boundary

Status: Proposed

Decision owner: Repository owner

Last updated: 2026-08-16

Target release: Post-beta

## Summary

Keep `v0.1.0-beta.1` capture-only. The public `wip` command must not update a
normal branch, merge commits, push refs, or resolve conflicts.

Treat landing as a separate transaction. A future landing command must use an
exact reviewed plan, a target lock, a backup ref, and one target-ref
compare-and-swap.

This KEP defines that future boundary. It does not authorize implementation or
public release.

## Context

Capture and landing protect different assets.

Capture protects the source checkout, source index, selected paths, lane ref,
commit chain, and recovery intent. It can run for disjoint lanes in parallel.

Landing protects a shared target branch and its review policy. It must order
competing sources and stop when the target changes.

A single command cannot safely infer both ownership decisions. A checked-out
target also needs worktree-aware coordination that standalone capture does not
provide.

The existing beta therefore publishes only an agent-owned local ref:

```text
refs/heads/wip/<agent>/<lane>
```

## Decision request

Approve these two decisions separately:

1. Publish the beta with capture-only scope.
2. Defer a standalone landing command until independent beta evidence exists.

The second decision does not block internal landing adapters. An adapter must
preserve every control in this KEP.

## Beta contract

The beta must:

- capture only to an agent-owned ref.
- preserve the source `HEAD`, worktree, and complete index.
- keep the source ref after release or abort.
- return typed capture and recovery evidence.
- state that review and landing are separate operations.

The beta must not:

- update `main` or another normal branch.
- merge, rebase, cherry-pick, or resolve conflicts.
- push a ref or call a hosting API.
- delete a source or backup ref.
- infer approval from a passing local gate.

## Future landing transaction

This section defines a possible post-beta command. The command does not exist.

### Proposed interface

Preview one landing:

```text
wip land plan \
  --source-ref refs/heads/wip/agent/lane \
  --target-ref refs/heads/main \
  --expected-source <commit> \
  --expected-target <commit>
```

Apply only the reviewed plan:

```text
wip land apply \
  --plan-id <plan-id> \
  --plan-digest <sha256> \
  --yes
```

The apply command must not accept free-form source or target overrides.

### Required evidence

The plan must bind:

- the canonical Git common directory.
- the source ref and exact source commit.
- the target ref and exact target commit.
- the source capture plan ID and digest.
- every source commit, parent, tree, and message.
- the ancestry result.
- the target worktree state.
- the required gate receipts.
- the backup ref name and old target commit.
- the landing policy version.

### Preconditions

The transaction must stop unless all conditions pass:

1. The source ref matches the expected source commit.
2. The source capture intent is complete and valid.
3. The target ref matches the expected target commit.
4. The source commit is a descendant of the target commit.
5. The target has no pending landing transaction.
6. Every required gate receipt matches the source commit.
7. The repository has no unsupported state version.
8. The target branch is not checked out in any worktree.

The first standalone version must support fast-forward landing only.

### Publication

The transaction must use this order:

1. Acquire the target landing lock.
2. Recheck the complete reviewed plan.
3. Create the backup ref with an expected-empty compare-and-swap.
4. Write a durable `prepared` landing intent.
5. Update the target ref with the expected old commit.
6. Mark the intent `applied`.
7. Record the new target state.
8. Mark the intent `complete`.

The source ref and backup ref must remain after publication.

### Checked-out targets

The standalone command must return `TARGET_CHECKED_OUT` when any worktree uses
the target branch. It must not update that ref directly.

A worktree-aware adapter can implement a separate stack transaction. That
adapter must bind the worktree, source, target, gates, ancestry, backup, intent,
and exact target compare-and-swap.

### Recovery

The landing intent must support these states:

```text
prepared -> applied -> complete
```

Recovery must use the original plan ID and digest. It must never reconstruct a
plan from the current worktree.

| Intent state | Target state | Required action |
| --- | --- | --- |
| `prepared` | old target | Run a new preview from current refs. |
| `prepared` | exact new target | Reconcile the original intent. |
| `applied` | exact new target | Reconcile the original intent. |
| `complete` | exact new target | Return the existing receipt. |
| any state | another target | Stop with `REF_MOVED`. |

## Security and privacy

Landing must not send telemetry or repository data to a network service.
Receipts must not contain credentials, file contents, remote URLs, usernames,
or absolute worktree paths.

Hooks and gate commands remain trusted repository code. A landing command must
not add another hook execution surface.

## Non-goals

This KEP does not define:

- pull-request creation or merge.
- remote push.
- conflict resolution.
- non-fast-forward landing.
- source-ref deletion.
- automatic gate selection.
- rollback by force update.

## Alternatives

### Land during capture

Rejected. Parallel capture must not serialize on one checked-out branch.

### Use `git merge` or `git cherry-pick`

Rejected. These commands use worktree and index state that the capture receipt
does not own.

### Update a checked-out target ref directly

Rejected. The target worktree and index can become inconsistent with its ref.

### Keep landing outside this project forever

Valid for the beta. Revisit this option after independent users report their
handoff and reconciliation needs.

## Test plan for a future command

A landing implementation must test:

- exact fast-forward success.
- moved source and moved target refusal.
- non-fast-forward refusal.
- checked-out target refusal.
- concurrent plans for one target.
- backup-ref collision.
- gate-receipt mismatch.
- interrupted intent recovery at each state.
- idempotent reconciliation.
- source and backup ref preservation.
- inherited Git environment isolation.
- unusual valid ref names.
- Linux, macOS, and Windows runtime behavior.

## Rollout gates

Do not implement the command before these gates pass:

1. Complete one independent beta exercise for both capture modes.
2. Record at least three real capture-to-integration handoffs.
3. Approve the landing policy and JSON receipt contract.
4. Complete an independent security review of target locking and recovery.
5. Define the relationship with worktree-aware stack landing.

## Decision log

| Date | Decision |
| --- | --- |
| 2026-08-16 | Recommend capture-only scope for the first public beta. |
| 2026-08-16 | Require a separate KEP before standalone landing implementation. |

## Open decisions

- Approve the capture-only public beta boundary.
- Select the owner of the future target-lock namespace.
- Select the gate receipt types that landing must require.
- Decide whether standalone landing remains deferred after the beta period.
