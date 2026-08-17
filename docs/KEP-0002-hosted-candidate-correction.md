# KEP-0002: Hosted candidate correction receipt

Status: Accepted

Implementation: Verified on 2026-08-17

Decision owner: Repository owner

Last updated: 2026-08-17

Target release: Before `v0.1.0-beta.1`

## Summary

Add a fail-closed receipt for a candidate that changes after the first public
push. Keep the pre-first-push receipt immutable.

The correction receipt must bind the verifier, old candidate, new candidate,
local checks, remote refs, pull request, and hosted runs. It must bind the final
tree.

This KEP does not authorize a tag or a pull-request merge.

## Context

The first publication used an absent repository as a privacy and identity
gate. That fact cannot stay true after repository creation.

Hosted Windows checks then found three host assumptions. The corrected
candidate passed every required hosted check before `main` moved.

The original receipt still proves the pre-first-push boundary. It does not bind
the corrected final commit.

## Decision

Use a second receipt type for post-first-push corrections. Require its
`pre-main` phase before a corrected candidate moves to `main`.

Use the `finalized` phase only to verify an observed bootstrap sequence. This
phase does not claim that the receipt existed before `main` moved.

## Required sequence

If the first hosted candidate fails, use this sequence:

1. Keep `main` at the reviewed bootstrap.
2. Keep the verification pull request open.
3. Fix the finding on the candidate branch.
4. Run every local release gate on the corrected commit.
5. Scan the complete history and worktree for secrets.
6. Check the complete author and committer identity set.
7. Fast-forward only the candidate branch.
8. Run every required hosted check on the corrected commit.
9. Create the private correction receipt.
10. Close the verification pull request without merging it.
11. Fast-forward `main` only to the receipt's candidate commit.
12. Check the remote commit and tree.

Do not force-push a candidate branch. If a fast-forward is not possible, create
a new branch.

The implemented command also supports a finalized verification sequence:

1. Keep the closed pull request and every hosted run.
2. Check that the pull request closed without a merge.
3. Check that remote `main` and the candidate ref equal the corrected commit.
4. Run the complete local and hosted evidence command.
5. Record that the receipt used the `finalized` phase.

## Receipt contract

The command must write one new private JSON file. It must not overwrite a
file or write inside the candidate or verifier checkout.

The receipt must bind:

- the schema version and generation time.
- the target repository and visibility.
- the reviewed bootstrap commit and tree.
- the first receipt digest.
- the old candidate commit and tree.
- the new candidate commit and tree.
- the complete merge-free correction range.
- the verifier commit, tree, command digest, and schema digest.
- the complete reviewed delta path list.
- the local object and secret-scan results.
- the public author and committer email result.
- a digest and count for every author and committer name and email.
- the remote candidate and `main` refs.
- the pull-request number, state, head, base, and merge result.
- each required run ID, event, head commit, status, and conclusion.
- each required check context and GitHub App integration ID.

The receipt must exclude tokens, identity values, local paths, private reports,
and raw security logs.

## Command behavior

The Go command is `scripts/publication-correction`. The command accepts these
explicit values:

- candidate and verifier checkouts.
- phase, bootstrap, old candidate, and new candidate.
- candidate ref, pull request, ruleset, and hosted runs.
- path manifest, first receipt, and output.

The command must use bounded timeouts for GitHub, Git, and secret-scan
commands. It must reject incomplete dependency injection in tests.

The command must stop unless:

- the candidate checkout is clean and uses complete history.
- the verifier checkout is clean and uses complete history.
- the verifier command and schema are tracked at the recorded verifier commit.
- the new candidate is the checked-out commit.
- the old candidate is an ancestor of the new candidate.
- the correction range is merge-free.
- the complete bootstrap delta matches the path manifest.
- object checks and secret scans pass.
- all history author and committer emails match the public owner email.
- the target repository exists and is public.
- the remote candidate ref equals the new candidate.
- the pull request matches the selected phase and remains unmerged.
- every required hosted run succeeded on the new candidate.
- the ruleset has no bypass actor and protects deletion, force pushes, and
  linear history.
- the pull-request rule requires review, stale-review dismissal, last-push
  approval, resolved threads, and rebase-only merges.
- strict status checks apply to branch creation.
- every ruleset check passed through its required GitHub App integration.

The `pre-main` phase requires the open pull request and bootstrap `main`. The
`finalized` phase requires the closed pull request and corrected `main`.

## Failure behavior

If a check fails, return a typed error and write no receipt. Do not weaken the
check after repository creation.

Preserve the old receipt, candidate refs, pull request, and hosted runs after a
failure.

## Test plan

Test these cases:

- exact one-commit correction success.
- multiple linear correction commits.
- clean verifier commit, tree, command digest, and schema digest.
- dirty, wrong-module, or missing-artifact verifier checkout.
- moved candidate ref.
- changed path manifest.
- merge commit in the correction range.
- failed or pending hosted run.
- run for a different head commit.
- pull request with a different head.
- merged pull request.
- private or different target repository.
- missing ruleset protection or a ruleset bypass actor.
- failed provider-bound check or changed GitHub App integration.
- invalid or incomplete first receipt.
- changed public identity.
- secret-scan or object-check failure.
- existing output file.
- output path inside the checkout.
- open receipt directory on Unix.
- external-command timeout.
- deterministic redaction and schema validation.

## Alternatives

### Edit the first receipt

Rejected. Editing an immutable receipt destroys its evidence value.

### Rerun the pre-first-push command

Rejected. The target repository now exists, and the checkout now has a remote.

### Trust only the hosted check page

Rejected. A passing page does not bind the local delta, identities, or final
tree.

### Delete and recreate the repository

Rejected. The public history and hosted evidence already exist.

## Rollout gate

The command and test matrix passed before the beta tag. The finalized run bound
the correction from `ed3f1fadfbc74eb0aa41ef8b90e41f403213d33d` to
`206fa8b6a1dde1d97081133e4d447c0881849922`.

Verifier commit `6f36147f24f614cff0c7010533d864f8d9ad7628` produced the
source-bound private receipt. Its digest is
`sha256:77db44ba2bfa6f007186ace931f38444521d8a29cf48bd945e07a801eda36a9a`.
Only the redacted result belongs in public evidence.

## Decision log

| Date | Decision |
| --- | --- |
| 2026-08-17 | Propose a second immutable receipt for hosted candidate corrections. |
| 2026-08-17 | The repository owner accepted KEP-0002. |
| 2026-08-17 | Implement both phases and record the finalized correction receipt. |
| 2026-08-17 | Supersede the provisional receipt with source-bound verifier and complete ruleset evidence. |
