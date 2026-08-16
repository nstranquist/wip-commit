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
7. Record the source `HEAD` and index-file digest before each scenario.

Do not add a remote. Do not use global Git configuration.

## Scenario 1: shared split

1. Run `wip init` in shared mode.
2. Claim both test files.
3. Change and stage both files.
4. Run `wip plan`.
5. Write a reviewed two-group plan.
6. Run `wip commit --plan <plan> --dry-run`.
7. Verify that the dry-run did not move the lane ref.
8. Run `wip commit --plan <plan>`.
9. Verify that the lane contains two linear commits.
10. Verify that source `HEAD` and the complete index stayed unchanged.

Record scenario ID `shared-split`.

## Scenario 2: overlapping claim refusal

1. Keep the first shared lane active.
2. Create a second lane with another agent and session ID.
3. Claim one path that the first lane owns.
4. Verify that the command returns `LEASE_CONFLICT`.
5. Verify that no second lease or unexpected ref was published.

Record scenario ID `overlap-refusal`.

## Scenario 3: verification failure

1. Stage a later change on one leased file.
2. Add `git rev-parse --is-inside-work-tree` as a verify command.
3. Run the capture.
4. Verify that the command returns `VERIFY_FAILED`.
5. Verify that source `HEAD`, the complete index, and the lane ref stayed
   unchanged.
6. Restore the exact staged bytes from Scenario 1 with explicit file writes and
   `git add`.
7. Verify that the complete index digest matches the digest recorded after
   Scenario 1.

Candidate directories do not contain Git metadata. This command must fail.

Record scenario ID `verify-failure`.

## Scenario 4: duplicate refusal

1. Create a new reviewed plan from the current lane ref and the restored staged
   snapshot.
2. Run that plan without another staged change.
3. Verify that the command returns `EMPTY_COMMIT_GROUP` or `EMPTY_SELECTION`.
4. Verify that the lane ref stayed unchanged.

Record scenario ID `duplicate-refusal`.

## Scenario 5: linked worktree split

1. Release the shared lane.
2. Run `wip init` with `--mode worktree` and `--create-worktree`.
3. Claim two files in that linked worktree.
4. Change and stage both files in the linked worktree.
5. Capture a reviewed two-group plan.
6. Verify both checkout `HEAD` values.
7. Verify both complete indexes.
8. Verify that the new lane contains the complete split chain.

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
