# Independent beta exercise

## Purpose

This exercise collects external evidence for shared and worktree capture. It
does not test public hosting, branch landing, or hostile multi-tenant use.

Only a non-author can produce an `independent` receipt. A maintainer can run the
same exercise, but the receipt must use `tester_kind: maintainer`.

Use a disposable repository. Do not use a repository that contains private or
valuable work.

## Privacy rules

The receipt must not contain:

- repository or worktree paths.
- file contents.
- commit messages.
- remote URLs.
- usernames or email addresses.
- credentials or environment values.

Record only object IDs, result codes, booleans, platform facts, and a short
redacted note. Validate the receipt against
[BETA-EXERCISE.schema.json](BETA-EXERCISE.schema.json).

## Preparation

1. Check out the exact candidate commit.
2. Build `wip` from that clean checkout.
3. Record the candidate commit and tree IDs.
4. Create a new disposable Git repository.
5. Configure a test-only Git name and email in that repository.
6. Commit two small text files as the base commit.
7. Record the source `HEAD`, index-file digest, and Git common-directory digest
   before each scenario.

Do not add a remote. Do not use global Git configuration.

## Scenario 1: shared split

1. Run `wip init` in shared mode.
2. Claim both test files.
3. Change and stage both files.
4. Run `wip plan --source staged_index --untracked not_applicable --observe`.
5. Verify that the observed plan changed no repository bytes.
6. Run `wip plan --source staged_index --untracked not_applicable --out
   <plan>`.
7. Add two messages and set the plan state to `reviewed`.
8. Run `wip commit --plan <plan> --dry-run`.
9. Require result schema `3.0.0` and `gate_outcome: "skipped"`.
10. Require `execution.policy: "no-exec"` and zero executed counts.
11. Verify that repository, store, ref, lease, configuration, and index bytes did
    not change.
12. Add hook and verification markers. Verify that the dry-run created neither
    marker.
13. Run `wip capture --plan <plan>`.
14. Verify that the non-dry capture executed the configured gates.
15. Verify that the lane contains two linear commits.
16. Verify that source `HEAD` and the complete index stayed unchanged.
17. Verify the cumulative handoff digest and complete base-to-tip chain.
18. Verify that the handoff is capture-only and does not authorize landing.

The dry-run validates structure only. It does not prove that repository hooks
or verification commands pass.

Record scenario ID `shared-split`.

## Scenario 2: overlapping claim refusal

1. Keep the first shared lane active.
2. Create a second lane with another agent and session ID.
3. Claim one path that the first lane owns.
4. Verify that the command returns `PATH_LEASE_CONFLICT`.
5. Verify that the failed claim did not add the overlapping path, move either
   lane ref, or publish an unexpected lease.

Record scenario ID `overlap-refusal`.

## Scenario 3: verification failure

1. Stage a later change on one leased file.
2. Write a new version 3 `staged_index` draft plan.
3. Add `git rev-parse --is-inside-work-tree` as a verify command.
4. Set the plan state to `reviewed` and run `wip capture`.
5. Verify that the command returns `VERIFY_FAILED`.
6. Verify that source `HEAD`, the complete index, and the lane ref stayed
   unchanged.
7. Restore the exact staged bytes from Scenario 1 with explicit file writes and
   `git add`.
8. Verify that the complete index digest matches the digest recorded after
   Scenario 1.

Candidate directories do not contain Git metadata. This command must fail.

Record scenario ID `verify-failure`.

## Scenario 4: duplicate refusal

1. Run `wip plan --source staged_index --untracked not_applicable --out
   <new-plan>` without another staged change.
2. Verify that the command returns `EMPTY_SELECTION`.
3. Verify that the lane ref stayed unchanged.

Record scenario ID `duplicate-refusal`.

## Scenario 5: linked worktree split

1. Release the shared lane.
2. Run `wip init` with `--mode worktree` and `--create-worktree`.
3. Claim two files in that linked worktree.
4. Change both files in the linked worktree. Do not stage them.
5. Create a reviewed two-group version 3 plan with `--source
   worktree_snapshot --untracked forbid`.
6. Capture the reviewed plan.
7. Verify both checkout `HEAD` values.
8. Verify both complete indexes.
9. Verify that the new lane contains the complete split chain.
10. Verify the cumulative handoff digest and `landing_authorized: false`.

Record scenario ID `worktree-split`.

## Scenario 6: reconciliation

1. Read the applied plan ID and digest from the JSON capture result.
2. Run `wip reconcile` with both exact values.
3. Run the same command again.
4. Verify that both runs report the already-complete result.
5. Verify that no ref or index changed.

Record scenario ID `reconcile-idempotency`.

## Optional installation scenarios

Use new temporary installation directories. Test `wip init --install` and
`wip init --install-skill`. Verify the installed binary version and the
complete skill bundle.

Record `install-binary` and `install-skill` when those scenarios run.

## Result

Set `overall: passed` only when all six required scenarios pass. Use
`overall: blocked` when an environment gate prevents a required scenario.
Use `observed_code: OK` for a successful operation and the exact typed error
code for an expected refusal. A passing scenario must record
`unexpected_ref_update: false`.

Send the redacted receipt to the maintainer. Keep raw logs on the tester's
machine unless a failure needs a separate reviewed disclosure.
