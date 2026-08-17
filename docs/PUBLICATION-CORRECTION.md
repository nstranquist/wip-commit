# Hosted candidate correction receipt

When hosted checks require a candidate change after the first public push,
use this procedure.

The command reads local and GitHub evidence. It writes one new private receipt.
It does not change a repository, ref, pull request, run, tag, or release.

This procedure does not authorize a tag or a pull-request merge.

## Receipt phases

Before `main` moves, use `--phase pre-main`. This phase requires these states:

- Remote `main` equals the reviewed bootstrap.
- The verification pull request is open and unmerged.
- The run list contains a successful candidate push.
- The run list contains one successful candidate pull-request run.

Use `--phase finalized` to verify a completed bootstrap sequence. This phase
requires these states:

- Remote `main` equals the corrected candidate.
- The verification pull request is closed and unmerged.
- The run list contains a successful candidate push.
- The run list contains one successful candidate pull-request run.
- The run list contains a successful `main` push.

The finalized phase records observed history. It does not claim that the
receipt existed before `main` moved.

## Evidence contract

The command verifies these local facts:

- The verifier checkout is clean, complete, and free of replacement refs.
- The receipt binds the verifier commit, tree, command digest, and schema digest.
- The candidate checkout is clean and uses complete history.
- The checked-out commit equals `--candidate`.
- No Git replacement ref changes the history.
- `git fsck --strict --no-dangling` passes.
- The old candidate is an ancestor of the new candidate.
- Every correction commit has one exact parent.
- The complete bootstrap delta matches `--paths-file`.
- Full-history and worktree secret scans pass.
- Every author and committer email matches the public owner email.

The receipt binds a digest of every distinct author and committer name and
email. It does not include those values.

GitHub display names are not identity credentials. The receipt records the
display-name match as a separate Boolean result.

The command verifies these hosted facts:

- The authenticated GitHub viewer is the repository owner.
- The target repository is public and uses `main` as its default branch.
- The remote candidate and `main` refs match the selected phase.
- The pull request has the required head, base, state, and merge result.
- Every declared hosted run completed successfully on the candidate commit.
- The selected ruleset has no bypass actor and protects the default branch.
- The ruleset blocks deletion and force pushes, and it requires linear history.
- Pull requests require review, last-push approval, and resolved review threads.
- The ruleset dismisses stale reviews and permits rebase merges only.
- Strict required checks apply to branch creation.
- Every ruleset check passed in the declared pull-request run.
- Every check came from the ruleset's GitHub App integration.

## Private output preparation

Create a private directory outside the candidate and verifier checkouts. On
Unix, set mode `0700` on the directory.

```text
mkdir -p ../wip-commit-private-receipts
chmod 700 ../wip-commit-private-receipts
```

Make sure that the output file does not exist. The command never overwrites a
file.

## Finalized verification command

Run the command source from a clean, reviewed verifier checkout. Set
`--repo-dir` to a clean checkout of the exact candidate.

```text
go run ./scripts/publication-correction \
  --verifier-dir . \
  --repo-dir ../wip-commit-correction-source \
  --target nstranquist/wip-commit \
  --phase finalized \
  --bootstrap b276204385636c5a8ac338491565bd4894255217 \
  --old-candidate ed3f1fadfbc74eb0aa41ef8b90e41f403213d33d \
  --candidate 206fa8b6a1dde1d97081133e4d447c0881849922 \
  --candidate-ref candidate/v0.1.0-beta.1 \
  --first-receipt ../wip-commit-private-receipts/pre-first-push-ed3f1fa-authorized.json \
  --paths-file docs/PUBLICATION-HANDOFF.paths \
  --pull-request 1 \
  --ruleset 20926881 \
  --run candidate-push:push:candidate/v0.1.0-beta.1:31996054315 \
  --run verification-pr:pull_request:candidate/v0.1.0-beta.1:31996057770 \
  --run main-push:push:main:31996220707 \
  --run dependency-graph:dynamic:main:31996222126 \
  --out ../wip-commit-private-receipts/hosted-correction-206fa8b-v2.json
```

Each `--run` value uses `LABEL:EVENT:BRANCH:ID`. Labels and run IDs must be
unique.

## Schema validation

Verify the private receipt against
[PUBLICATION-CORRECTION.schema.json](PUBLICATION-CORRECTION.schema.json).

```text
python3 -c 'import json,jsonschema; schema=json.load(open("docs/PUBLICATION-CORRECTION.schema.json")); receipt=json.load(open("../wip-commit-private-receipts/hosted-correction-206fa8b-v2.json")); jsonschema.Draft202012Validator.check_schema(schema); jsonschema.Draft202012Validator(schema,format_checker=jsonschema.FormatChecker()).validate(receipt)'
```

Keep the receipt outside the repository. Do not publish the receipt, email
addresses, local paths, private reports, tokens, or raw scan output.

## Failure recovery

If the command fails, preserve every input and fix the typed finding. Use a new
output path for the next complete run.

| Code | Required recovery |
| --- | --- |
| `VERIFIER_INVALID` | Use a clean, complete checkout that contains the tracked command and schema. |
| `CHECKOUT_NOT_CLEAN` | Use a clean checkout of the exact candidate. |
| `CANDIDATE_MISMATCH` | Check `HEAD` and all three explicit commit IDs. |
| `CORRECTION_NOT_LINEAR` | Use a merge-free descendant or create a new candidate branch. |
| `PATH_MANIFEST_MISMATCH` | Review the complete bootstrap delta and update the manifest through review. |
| `OBJECT_CHECK_FAILED` | Preserve the checkout and repair the Git object store. |
| `SECRET_SCAN_FAILED` | Remove the finding from the complete history and worktree. |
| `IDENTITY_MISMATCH` | Correct the public email or rebuild the candidate with the approved identity. |
| `REMOTE_REF_MISMATCH` | Check the candidate phase and remote refs. Do not force-push. |
| `PULL_REQUEST_MISMATCH` | Restore the required unmerged pull-request state or select the correct phase. |
| `HOSTED_RUN_FAILED` | Run every required workflow on the exact candidate. |
| `HOSTED_RUN_INCOMPLETE` | Add the missing candidate, pull-request, or `main` run. |
| `RULESET_MISMATCH` | Restore active default-branch protection before another run. |
| `REQUIRED_CHECK_FAILED` | Run the provider-bound check on the exact pull-request head. |
| `COMMAND_TIMEOUT` | Inspect the external service, then rerun the complete command. |
| `OUTPUT_UNSAFE` | Select a private `0700` directory outside both checkouts. |
| `OUTPUT_EXISTS` | Preserve the existing file and select a new output name. |

The command writes no receipt after a failed check.

## Recorded correction

The finalized run on 2026-08-17 bound these public facts:

- Old candidate: `ed3f1fadfbc74eb0aa41ef8b90e41f403213d33d`.
- New candidate: `206fa8b6a1dde1d97081133e4d447c0881849922`.
- New tree: `c0dd6638adcdc231840ad06406dc9f0caa38e45d`.
- Verifier commit: `6f36147f24f614cff0c7010533d864f8d9ad7628`.
- Verifier tree: `e9559e5fc7fa7083e471acb7d9e72e10b9c3110a`.
- Verifier command digest:
  `sha256:85a645b26c37a0033de22d3e3b6731d0d953e8ba0a2e7f634f1f8820f843087d`.
- Verifier schema digest:
  `sha256:f8873f01997118f7d6d57bfa9f95b68ce0febab64c79dbd727de9ddb0649c7ac`.
- Correction commits: `1`.
- Complete bootstrap delta paths: `20`.
- Correction delta paths: `5`.
- Hosted runs: `4`.
- Provider-bound checks: `6`.
- GitHub Actions integration: `15368`.
- The active ruleset had no bypass actor and matched every required control.
- Private receipt digest:
  `sha256:77db44ba2bfa6f007186ace931f38444521d8a29cf48bd945e07a801eda36a9a`.

The private receipt passed the Draft 2020-12 schema. It contains no email
address, local path, token, private report, or raw security log.
