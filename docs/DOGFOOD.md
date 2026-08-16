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

### Finding

The program fulfilled the shared-checkout capture contract during this
exercise. It also exposed an important product rule: safety checks can reject a
poor split, but a maintainer must still review semantic intent and dependency
closure.
