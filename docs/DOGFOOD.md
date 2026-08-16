# Dogfood evidence

## 2026-08-16 self-hosted shared capture

This exercise used a source-current `wip` binary on the real dirty
`wip-commit` checkout. It used shared mode and an exact staged split plan.

Evidence scope: local maintainer evidence. This exercise does not replace
hosted CI, an independent user exercise, or an independent security review.

### Initialization

- Source `HEAD`: `c6f104f06dc9fbcbdb3b954c8b53d563db2d5a06`.
- Lane: `refs/heads/wip/codex/oss-source-dogfood`.
- Initialization intent: `init-64f1850d3bf9c06fb9d818a2`.
- The dry-run left the complete state-file digest unchanged.
- The applied initialization left `HEAD` and the index tree unchanged.
- `wip doctor` reported a healthy store and no pending recovery work.

### Safe failure under host pressure

The first capture dry-run ran the complete internal test set. Disposable Git
tests reached the host storage limit and returned `no space left on device`.

`wip` returned `VERIFY_FAILED`. It did not create a commit chain or move the
lane ref. It preserved `HEAD` and the complete staged tree.

### Safe failure for an incomplete split

The next plan placed the CLI changes before the new embedded skill package.
The CLI compile check could not resolve that package in the candidate tree.

`wip` returned `VERIFY_FAILED`. It did not move the lane ref or source index.
The maintainer moved the skill group before the CLI group.

This result confirms that split review must include dependency closure. A
component boundary is a proposal, not proof that a group can stand alone.

### Incremental planner correction

After the applied capture, the source index still contained every captured
entry. The first incremental `wip plan` proposed those entries again because it
compared the index with checked-out `main`.

The planner now compares the source index with the lane's current commit. It
proposes only uncaptured staged changes. The capture engine remains the final
authority and still rejects an empty group.

### Verification environment correction

The first incremental capture check inherited the host lane identity. Candidate
tests then tried to resolve that real lane inside their disposable repositories.

The command returned `VERIFY_FAILED` and preserved the source index and lane
ref. Verification commands now remove the host WIP identity and
`GIT_INDEX_FILE`. They receive only the current `WIP_CANDIDATE_TREE` value.
`TestVerificationEnvironmentDoesNotInheritCaptureIdentity` protects this rule.

### Dry-run evidence correction

Dogfood review found that later dry-run groups reported the real base commit as
their parent. Those commit objects did not exist, so that evidence was
misleading.

The implementation now reports the real parent only for the first group. A
later dry-run group reports an empty parent. Ordered groups define the planned
dependency chain. `TestDryRunDoesNotInventSplitParentCommits` protects this
contract.

### Applied split result

The reviewed plan produced this linear chain:

1. `853b1fe6e9af1c5cbb38cf3575f84c1f2023bd30` — `fix(core): harden concurrent capture recovery`
2. `0b35f72ea5371f3c48436f1392e49e748bf16859` — `feat(skill): default automation to reviewed split capture`
3. `fc30910b6dc1cb73946bd6ee19120f0fbd6a262b` — `feat(cli): add resumable setup and state maintenance`
4. `796e9420981095c23d8f78ea417886e509f6a155` — `docs(oss): codify public beta operating practices`

Receipt evidence:

- Plan ID: `plan-146eb28105eccd3b0a67f90f`.
- Plan digest: `sha256:469d73e8277b3633ddf51cbb81289e6f250c810c2824af0306609273b3c72ede`.
- Final tree: `7ca5ab1461459abe16e8a82ac93699d116dba451`.
- Changed-path counts by group: 17, 3, 10, and 16.
- Each commit parent was the preceding commit.
- The final commit tree equaled the complete staged tree.
- The checked-out `main` ref remained at the source `HEAD`.
- The Git index file digest stayed byte-for-byte equal during duplicate-capture
  testing.
- An exact repeat returned `EMPTY_COMMIT_GROUP` and did not move the lane ref.

### Applied corrective split

The source-current binary then captured the three corrections as a second
reviewed split:

1. `5eb4678b5330d97fc654a9b057e89f42e0981c95` — `fix(verify): isolate candidate capture identity`
2. `fd908b90c63e67ab0aa3da3fda7cad41ee2b2037` — `fix(plan): omit already captured staged paths`
3. `4593fab114b4bfae55613f31e77188e28612eccb` — `docs(dogfood): record self-hosted split evidence`

The second plan ID was `plan-c554ab6722e5a5628e098b4e`. Its digest was
`sha256:1b80f4e268899a203620a1414b741ad83c95c2e41a23daf859f20f65ead1246a`.
The complete seven-commit chain was linear. Its final tree was
`7a892f4f57a93c0df2e808c3b5c055145b1360fc`, equal to the complete staged
tree at capture time. `HEAD` and the complete index-file digest stayed
unchanged. A later `wip plan` returned `EMPTY_SELECTION`.

### Released-state maintenance

Release preserved the lane ref at
`4593fab114b4bfae55613f31e77188e28612eccb`. It left `HEAD` and the complete
index-file digest unchanged. `wip status` then returned `LANE_NOT_ACTIVE`, as
required for a released lane.

An archive preview left the complete state-file digest unchanged. The applied
archive receipt was `archive-74f4b31a69187b337ed50e88`, with digest
`sha256:74f4b31a69187b337ed50e885ae5c5af12a4f44549b9693af8a1824449b58ca2`.
It moved only the released lane, lease, and profile records. It preserved the
lane ref and commits. Restoring that exact receipt returned its state to
`restored`, put the same three records back, and again preserved the ref.
`wip doctor` remained healthy after apply and restore, with no pending work or
findings.

### Finding

The program fulfilled the shared-checkout capture contract during this
exercise. It also exposed an important product rule: safety checks can reject a
poor split, but a maintainer must still review semantic intent and dependency
closure.

## 2026-08-16 isolated candidate and beta exercise

This exercise used a source-current `v0.1.0-beta.1` binary. It captured the
reviewed OSS changes and then ran the public beta protocol in a new repository.

Evidence scope: local maintainer evidence. The clean-room receipt does not
replace the required non-author receipt, hosted CI, or hosted repository
settings.

### Reviewed candidate split

The dry-run and applied plan used the same five groups:

1. `67fcc41364ffe237f16d30e034fc699f60224555` — `test(cli): isolate ambient WIP identity`
2. `c073e29880d8faa347281800c7aadaef6397a901` — `test(engine): verify applied receipt evidence`
3. `32349513594cfe2fbf4b3b504ae349e2b5feb9f1` — `test(gitx): cover exact Git output helpers`
4. `9a4b03bafefe961bc89a87b2629cc8ac72d06af5` — `test(release): reject unsafe source states`
5. `e3efaf27f75389f5440ef8211dadd707848f9714` — `docs(oss): define beta evidence and landing gates`

The plan ID was `plan-a432b3b1ad844c6b944c99ad`. Its digest was
`sha256:84c3e5bca6d1cdd4ba5d0fac47e81bcca01b05fe668960c249f9a0416e9a328c`.
The final tree was `401897e80bfd5542e63d19386263a593d80f47d2`.

The detached source `HEAD` stayed at
`7248dff5fc973dff8aa3ccf85010f7125633552f`. The complete source index file
kept the same SHA-256 digest. The final lane tree equaled the staged tree. Two
reconciliation runs returned the same already-complete receipt. An exact
repeat returned `EMPTY_COMMIT_GROUP` without moving the lane ref.

### Test-harness correction

The first complete test run inherited the maintainer's active `WIP_LANE`,
`WIP_AGENT`, and `WIP_SESSION`. Eleven in-process CLI tests then selected the
wrong identity and returned `LANE_NOT_ACTIVE`.

The CLI test helper now clears ambient identity for disposable fixtures. A
separate test preserves coverage of the intentional environment defaults. The
complete CLI suite then passed with a deliberately foreign host identity.

### Protocol corrections

Review found that the first receipt schema did not require six distinct
scenario IDs. It could accept repeated IDs with different result objects. The
schema now requires each mandatory ID exactly once and permits each optional
installation ID at most once. A passing receipt also requires every scenario
to pass, both source states to stay preserved, and no unexpected ref update.

The clean-room run found that overlapping claims return
`PATH_LEASE_CONFLICT`, not `LEASE_CONFLICT`. The protocol and schema now use
the implemented typed code. The run also confirmed that a failed verification
preserves the later staged change. The duplicate-refusal procedure therefore
restores the prior staged snapshot explicitly before it checks for an empty
group.

### Clean-room result

All six mandatory scenarios passed in a disposable repository with no remote
and repository-local test identity:

- shared two-commit split capture.
- overlapping path refusal.
- failed candidate verification isolation.
- duplicate group refusal.
- linked-worktree two-commit split capture.
- idempotent receipt reconciliation.

Binary and portable-skill installation also passed in new temporary
directories. The installed binary reported `0.1.0-beta.1`, and both installed
skill files were byte-equal to the source bundle.

The redacted maintainer receipt is
[BETA-EXERCISE.maintainer-2026-08-16.json](BETA-EXERCISE.maintainer-2026-08-16.json).
It validates the protocol without closing the independent beta gate.
