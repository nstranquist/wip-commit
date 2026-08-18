# Dogfood evidence

## 2026-08-18 atomic finalization and schema review

This exercise used a source-current binary in the isolated
`feat/agent-facing-capture-v2` linked worktree. It tested the review corrections
against real repository state.

Evidence scope: local maintainer evidence. This exercise does not prove hosted
CI, branch landing, a public release, or independent adoption.

### Review corrections

The final guided capture now keeps one lane lock from handoff validation through
lane release. The active handoff command also selects and validates the intent,
renews the lease, and builds status under one lane lock. Completed intents still
validate every Git parent, tree, message, path set, scope, and final tree.

Additional corrections bind `wip env` to the lane worktree before renewal and
recognize every supported Boolean spelling of `--observe`. Initialization now
requires an interactive worktree destination and reports where to continue
after create or reuse.

Deterministic contention tests paused after handoff validation. A competing
lane lock returned `LOCK_TIMEOUT` for both active handoff and final capture.
Wrong-worktree and observe tests proved that rejected or observing calls did
not change lease timestamps.

The released-handoff regression compared the lane lock before and after the
command. The bytes were identical. Unit tests also proved that released
handoff locking does not create a missing lock file or parent directory. The
tests reject directories, symlinks, and replaced lock paths. They also prove
that the read-only acquisition still waits for an existing owner.

Real plan, status, legacy-plan, and handoff command data passed the published
Draft 2020-12 schemas. A deliberately incomplete handoff with an unknown field
was rejected.

### Race-gate correction

The first required three-count race run found a timing defect in the existing
heartbeat test. The test slept near lease expiry. Race instrumentation
sometimes moved execution past the deadline. Its failure path also left the
test heartbeat running.

The first correction polled the lease for a new timestamp. A later race run
correctly hit the fail-closed stable-file check during atomic lease replacement.
The final test uses an internal observer that fires only after a completed
background refresh. It does not add a retry or weaken the production reader.

Ten targeted heartbeat race repetitions passed. After the released-handoff
lock correction, ten file-lock repetitions and five CLI contention repetitions
also passed. The final `go test -race -count=3 ./...` run passed all packages.
The CLI package completed in 157.817 seconds.

The final local gate matrix also passed `go test ./...`, `go vet ./...`,
`actionlint`, `golangci-lint run ./...`, `go mod verify`, and
`govulncheck@v1.7.0 ./...`. The vulnerability scan found no reachable known
vulnerability.

### Reviewed repository capture

The source `HEAD` stayed at
`1194d4564c1009f81d486000734756e787956ed1`. Status reported 21 exact capture
candidates and no foreign staged paths. The reviewed three-group plan ran a
focused gate for the boundary change and full repository tests for the schema
and documentation groups.

The private chain was:

1. `f47cedc0b69c46933c2872f552aa5f84f78d0177` — `fix(capture): harden agent workflow boundaries`
2. `8d3764881ebc60eefa0e96845cabd8194379a4da` — `test(schema): validate published command contracts`
3. `a6f7dee2049672baa78d61cc149e8f983ce3e033` — `docs(capture): explain atomic agent handoff workflow`

Receipt evidence:

- Lane: `refs/heads/wip/codex/agent-facing-v2-atomic-final`.
- Plan ID: `plan-185477327d1cc03e1e04037e`.
- Capture intent digest:
  `sha256:e7edfba90386fb2a2c8b9c34f2e7844a92c92843d15b1ef8ef5f1607e412c873`.
- Reviewed plan digest:
  `sha256:5f339fd8cfcbe621ab170067dfcea023a4d5e6e61827e9ae3d1de5f785c26e41`.
- Source-index digest:
  `sha256:028fe1582dfea7c4609f05ca2804dd4d80ccc1dd0d973a789f3dc636b2488a39`.
- Final tree: `f3223a96009d9fed401c33bb6fd7745aec60bc61`.
- Every group-specific verification command passed.
- The handoff set `capture_only` to true and `landing_authorized` to false.

The final reviewed capture added this evidence file and used `--final`:

- Commit: `e356d9f489ec2ff256dfef424a762a867defe858`.
- Plan ID: `plan-1c61a7a787b31eb9d3b35560`.
- Capture intent digest:
  `sha256:4fbd19cc3d180cd223dd912508002db964b2d5f849b7ced4cbac909f8a599ca1`.
- Reviewed plan digest:
  `sha256:cd600afae3cd847d34b42792e39d942cecb708ca361232865689bc4b369d93ec`.
- Final tree: `d285e5e57bf9491d2067222a7e38fa280e9b8d3c`.
- `lane_released` was true, and the final context had no lease expiry.

The source `HEAD` remained unchanged. The lane ref and all four private commits
remain available. No command landed, merged, pushed, or deleted the lane ref.

## 2026-08-18 reviewed guided capture

This exercise used a source-current binary in a linked worktree. It captured
the complete agent-facing workflow with the new version 2 plan and handoff
contracts.

Evidence scope: local maintainer evidence. This exercise does not prove hosted
CI, publication, release, landing, or independent adoption.

### Operational status and lease behavior

The source `HEAD` was
`206fa8b6a1dde1d97081133e4d447c0881849922`. The lane was
`refs/heads/wip/codex/agent-facing-capture-v2`.

An expired lane inspection returned `LEASE_EXPIRED` with a complete status
object. It did not renew an expired lease. After an explicit claim, ordinary
`status` and `plan` calls renewed the complete active path set.

After exact staging, status reported 46 capture candidates and no foreign
staged paths. It also reported three empty broad lease scopes. The status
object validated against `STATUS.schema.json`.

### Reviewed plan and safe failure

The generated draft proposed 12 component groups. Review consolidated them
into three dependency-closed groups:

1. `feat(capture): add reviewed agent workflow`
2. `docs(capture): define operational handoff contracts`
3. `feat(skill): teach reviewed capture workflow`

The first capture built the first private commit. The next group failed with
`DIFF_CHECK_FAILED` because `docs/STATUS.schema.json` had one extra blank line
at EOF. The lane ref stayed at its old value. The source `HEAD` and staged
entries also stayed unchanged.

After that byte was fixed, the old plan returned `STALE_PLAN` before hooks ran.
This result confirmed that the reviewed plan bound the selected index content.
A new generated draft received the same reviewed three-group structure.

### Applied result

The second capture produced this private linear chain:

1. `6a9e14d1f83bb3c04202e4536d94578e2ca89019` — `feat(capture): add reviewed agent workflow`
2. `19a4f1c1c6463c55bbe8bf3439691e5901cf040b` — `docs(capture): define operational handoff contracts`
3. `4c27c2a6f477a33ef257a4722fc03c47d7473f0e` — `feat(skill): teach reviewed capture workflow`

Receipt evidence:

- Plan ID: `plan-9b2932bca63b757a5ec475b2`.
- Capture intent digest:
  `sha256:8c48dad5804a213823c24447957735e61f3853bea710b278f9fcde4a833281b5`.
- Reviewed plan digest:
  `sha256:d24c6a65ade6962da016d4c75d6d8141a03dc9744d980a6359e6284c3ef898c7`.
- Final tree: `fda0fab63f375d37b1fb2fd4e5f424af8724fea0`.
- Changed-path counts: 21, 22, and 3.
- Each group-specific verification command passed.
- The source `HEAD` stayed unchanged.
- The complete `git ls-files --stage -z` digest stayed
  `cfb3075ba51c7b6358c5bd04c7c4abf4fd3f928815f06470dedc0b5466adc50a`.

The guided command emitted a handoff that validated against
`HANDOFF.schema.json`. The handoff bound the named message policy and reviewed
plan digest. It set `capture_only` to true and `landing_authorized` to false.

The later standalone handoff returned the same receipt. Status reported zero
capture candidates, 46 captured-but-still-staged paths, a complete intent, and
no foreign staged paths. The lane remained active at that checkpoint so this
evidence record could be added. A final one-file capture of this record
releases the lane only after handoff validation. No command lands, merges, or
pushes the private ref.

### Post-capture review correction

A later code review found a narrow interval between command validation and the
kernel's first index read. A parallel writer could replace selected entries
during that interval. Stable replacement content could then pass the kernel's
final reread.

The kernel now requires the reviewed source-index digest. It compares that
digest with its initial source snapshot before hooks run. The final reread
still detects changes during capture.

`TestRunRejectsReviewedIndexMismatchBeforeHooks` proves that a mismatch returns
`SOURCE_INDEX_MOVED` without running the repository hook or updating the ref.
This correction requires a new final capture and supersedes the earlier lane
tree as the release candidate.

The corrected candidate used lane
`refs/heads/wip/codex/agent-facing-capture-v2-final`. Its reviewed capture
produced this chain:

1. `7efa2bea4373b40b2089a6ff61b37778dcec7246` — `feat(capture): add reviewed agent workflow`
2. `5e853892f31e08e8dc73a7a4f0b4f3a6c63c4a29` — `docs(capture): define operational handoff contracts`
3. `eb9ac96215ec1033920a7569fb8961f09c3c2aed` — `feat(skill): teach reviewed capture workflow`

Corrected receipt evidence:

- Plan ID: `plan-16cafb8a9838ec64f7631b00`.
- Capture intent digest:
  `sha256:2a8922d1d1a0bd9356b4fc4a3bf7489f8d1fab7a27304777e37c4f0b0bcb084d`.
- Reviewed plan digest:
  `sha256:11d4605aa1795ab6a9b7154f7328205e2bb60abf380a84d209924389dfbda431`.
- Reviewed and kernel source-index digest:
  `sha256:cb2393c80e40ccd78ba612742625a09490e81f2843cf5e85c91c25a9ae479387`.
- Final tree: `1f6bb2999e707426c67d3942e83565fb4e17b0a4`.
- The source `HEAD` and complete staged-entry listing stayed unchanged.
- A standalone active-lane handoff renewed the complete lease set only after
  the receipt validated.

The lane stayed active for this evidence edit. A final reviewed capture adds
only this changed file and releases the lane after handoff validation.

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
