# KEP-0002: Hosted candidate correction receipt

Status: Proposed

Decision owner: Repository owner

Last updated: 2026-08-17

Target release: Before `v0.1.0-beta.1`

## Summary

Add a fail-closed receipt for a candidate that changes after the first public
push. Keep the pre-first-push receipt immutable.

The correction receipt must bind the old candidate, new candidate, local
checks, remote refs, pull request, and hosted runs. It must bind the final tree.

This KEP does not authorize a tag or a pull-request merge.

## Context

The first publication used an absent repository as a privacy and identity
gate. That fact cannot stay true after repository creation.

Hosted Windows checks then found three host assumptions. The corrected
candidate passed every required hosted check before `main` moved.

The original receipt still proves the pre-first-push boundary. It does not bind
the corrected final commit.

## Decision request

Approve a second receipt type for post-first-push corrections. Require that
receipt before a corrected candidate moves to `main`.

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

## Receipt contract

The command must write one new private JSON file. It must not overwrite a
file or write inside the source checkout.

The receipt must bind:

- the schema version and generation time.
- the target repository and visibility.
- the reviewed bootstrap commit and tree.
- the first receipt digest.
- the old candidate commit and tree.
- the new candidate commit and tree.
- the complete merge-free correction range.
- the complete reviewed delta path list.
- the local object and secret-scan results.
- the public author and committer identity result.
- the remote candidate and `main` refs.
- the pull-request number, state, head, base, and merge result.
- each required run ID, event, head commit, status, and conclusion.
- each required check context and GitHub App integration ID.

The receipt must exclude tokens, email addresses, local paths, private reports,
and raw security logs.

## Command behavior

Add a Go command under `scripts/`. The command must accept explicit repository,
bootstrap, old candidate, candidate ref, and pull request values. It must also
accept explicit run, path manifest, and output values.

The command must use bounded timeouts for GitHub, Git, and secret-scan
commands. It must reject incomplete dependency injection in tests.

The command must stop unless:

- the checkout is clean.
- the new candidate is the checked-out commit.
- the old candidate is an ancestor of the new candidate.
- the correction range is merge-free.
- the complete bootstrap delta matches the path manifest.
- object checks and secret scans pass.
- all history identities match the public owner identity.
- the target repository exists and is public.
- the remote candidate ref equals the new candidate.
- the pull request is open, unmerged, and points to the new candidate.
- every required hosted run succeeded on the new candidate.

The first version must create the receipt before `main` moves. A separate mode
can check `main` after the fast-forward.

## Failure behavior

If a check fails, return a typed error and write no receipt. Do not weaken the
check after repository creation.

Preserve the old receipt, candidate refs, pull request, and hosted runs after a
failure.

## Test plan

Test these cases:

- exact one-commit correction success.
- multiple linear correction commits.
- moved candidate ref.
- changed path manifest.
- merge commit in the correction range.
- failed or pending hosted run.
- run for a different head commit.
- pull request with a different head.
- merged pull request.
- private or different target repository.
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

Implement and test the command before the beta tag. Generate a private receipt
for the correction from `ed3f1fadfbc74eb0aa41ef8b90e41f403213d33d` to
`206fa8b6a1dde1d97081133e4d447c0881849922`.

Record only the redacted result in public evidence.

## Decision log

| Date | Decision |
| --- | --- |
| 2026-08-17 | Propose a second immutable receipt for hosted candidate corrections. |
