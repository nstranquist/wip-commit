# First publication handoff

Status: Executed on 2026-08-17. Do not run this handoff again.

Target: `github.com/nstranquist/wip-commit`

Reviewed bootstrap: `b276204385636c5a8ac338491565bd4894255217`

Owner approval: `owner-session-2026-08-16`

Final public `main`: `206fa8b6a1dde1d97081133e4d447c0881849922`

Public evidence: [PUBLICATION-EVIDENCE.md](PUBLICATION-EVIDENCE.md)

## Execution note

The pre-first-push receipt bound
`ed3f1fadfbc74eb0aa41ef8b90e41f403213d33d` and its 17-path manifest. The first
hosted run found three Windows host assumptions and a disabled dependency
graph.

Commit `206fa8b6a1dde1d97081133e4d447c0881849922` fixed the Windows findings and
expanded the complete bootstrap delta to 20 reviewed paths. The corrected
commit passed every required hosted check.

Pull request 1 closed without a merge commit. Then `main` moved by a direct
fast-forward.

The pre-first-push command cannot validate this correction because the target
now exists. The `finalized` procedure in
[PUBLICATION-CORRECTION.md](PUBLICATION-CORRECTION.md) verified the completed
sequence and produced the required private correction receipt.

## Boundary

The owner approved the repository name, module path, and capture-only public
beta scope. The owner has not approved a release tag.

The repository instructions prohibited an agent push without an owner
override. The owner gave one-time approval for this exact handoff. That
approval did not authorize a pull-request merge or tag.

The preflight command is read-only except for one new private receipt outside
the checkout. It does not create a repository, remote, tag, branch, or Git ref,
and it does not push.

## Preconditions

Use the final clean publication candidate. It must satisfy all these checks:

- The reviewed bootstrap exists in its history.
- Every candidate commit after the bootstrap forms one merge-free linear
  series.
- The complete candidate delta equals
  [PUBLICATION-HANDOFF.paths](PUBLICATION-HANDOFF.paths).
- The checkout has no remote and no tag.
- The target GitHub repository does not exist.
- `gh` is authenticated as `nstranquist`.
- Every author and committer email in the complete history matches the email
  already visible on the owner's public GitHub profile. The receipt does not
  store the email.
- The repository is not shallow and has no replacement refs.
- `git fsck --strict --no-dangling` passes.
- `gitleaks` passes for the complete history and worktree.

Create a private directory beside the checkout. On Unix, the preflight rejects
a directory that grants group or other permissions. On Windows, use a private
ACL for the current account. Make sure the receipt file does not exist. Then
run:

```text
mkdir -p ../wip-commit-private-receipts
chmod 700 ../wip-commit-private-receipts
go run ./scripts/publication-handoff \
  --repo-dir . \
  --target nstranquist/wip-commit \
  --bootstrap b276204385636c5a8ac338491565bd4894255217 \
  --paths-file docs/PUBLICATION-HANDOFF.paths \
  --out ../wip-commit-private-receipts/pre-first-push.json
```

Validate the receipt against
[PUBLICATION-HANDOFF.schema.json](PUBLICATION-HANDOFF.schema.json). Retain it
outside the repository. Do not publish the receipt because its timestamp and
commit metadata are operational evidence.

The following validation command requires the Python `jsonschema` package:

```text
python3 -c 'import json,jsonschema; schema=json.load(open("docs/PUBLICATION-HANDOFF.schema.json")); receipt=json.load(open("../wip-commit-private-receipts/pre-first-push.json")); jsonschema.Draft202012Validator.check_schema(schema); jsonschema.Draft202012Validator(schema,format_checker=jsonschema.FormatChecker()).validate(receipt)'
```

Record these values from the receipt before any external mutation:

- `candidate.commit`
- `candidate.tree`
- `candidate.commit_count`
- `bootstrap.commit`
- `bootstrap.tree`
- the complete `delta_paths` list

Stop if any preflight check fails. Do not weaken a check or edit the receipt.
Fix the candidate, capture a new reviewed commit series, and rerun the preflight
to a new output path.

## Human-only first push

This section is an execution record. Do not run these commands again.

In this section, `<candidate-commit>` means the exact `candidate.commit` from
the validated receipt. A human repository owner must run these commands.

1. Create an empty public repository. Do not add a README, license, or
   `.gitignore` from GitHub.

   ```text
   gh repo create nstranquist/wip-commit \
     --public \
     --description "Guarded split-commit capture for parallel agents." \
     --disable-wiki
   ```

2. Set the default GitHub Actions token permission before the first workflow
   run.

   ```text
   gh api --method PUT \
     repos/nstranquist/wip-commit/actions/permissions/workflow \
     -f default_workflow_permissions=read \
     -F can_approve_pull_request_reviews=false
   ```

3. Add the remote and push only the reviewed bootstrap to `main`.

   ```text
   git remote add origin https://github.com/nstranquist/wip-commit.git
   git push origin \
     b276204385636c5a8ac338491565bd4894255217:refs/heads/main
   ```

4. Push the exact candidate to a temporary candidate branch.

   ```text
   git push origin \
     <candidate-commit>:refs/heads/candidate/v0.1.0-beta.1
   ```

5. Open a pull request only to run the exact candidate through hosted pull
   request checks. Do not merge it.

   ```text
   gh pr create \
     --repo nstranquist/wip-commit \
     --base main \
     --head nstranquist:candidate/v0.1.0-beta.1 \
     --title "chore(release): verify public beta candidate" \
     --body "Bootstrap-only verification PR. Run hosted checks. Do not merge."
   ```

6. Wait for every job. Record the pull request URL, workflow URLs, and exact
   check names. The required set includes the three operating-system tests,
   race test, lint test, and dependency review. Do not infer a required check
   name from the workflow source.

7. Close the pull request without merging it. Then fast-forward `main` directly
   to the exact candidate.

   ```text
   gh pr close <pull-request-number> \
     --repo nstranquist/wip-commit \
     --comment "Hosted candidate checks recorded. Closing without merge."
   git push origin <candidate-commit>:refs/heads/main
   ```

8. Verify that remote `main` equals the receipt's candidate commit. Use the
   GitHub commit API to verify that its tree equals `candidate.tree`.

   ```text
   git ls-remote origin refs/heads/main
   gh api repos/nstranquist/wip-commit/git/commits/<candidate-commit> \
     --jq .tree.sha
   ```

Stop if either identity differs. Do not force-push.

## Hosted controls

After the exact fast-forward, follow [HOSTED-SETUP.md](HOSTED-SETUP.md):

1. Enable and verify the security settings.
2. Activate a `main` ruleset with the exact observed check names.
3. Require one approval and conversation resolution.
4. Block force pushes and branch deletion.
5. Retain redacted URLs or exported settings as evidence.

The first direct fast-forward is a one-time bootstrap action before the ruleset
exists. All later changes must follow the active protected-branch policy.

## Gates that remain after the handoff

Do not create `v0.1.0-beta.1` until the requirement tracker has evidence for:

- hosted Linux, macOS, and Windows execution on the final tag candidate.
- repository rules and security settings.
- a non-author beta exercise.
- confidential conduct reporting and an approved Code of Conduct.
- a clean public-module installation.
- explicit owner approval of the tag.

Any source edit after hosted verification creates a new candidate. Repeat the
applicable local and hosted gates on that exact commit.
