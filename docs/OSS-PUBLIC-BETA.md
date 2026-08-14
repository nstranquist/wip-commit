# OSS public beta plan

Plan ID: `wip-oss-public-beta`

Status: Proposed

Target: `v0.1.0-beta.1`

Owner: Repository owner

Last review: 2026-08-14

Approval: Not yet recorded

Local preparation: Complete on 2026-08-14

## Decision request

Approve a capture-only public beta with these boundaries:

- Keep the repository name `wip-commit`.
- Keep the module path `github.com/nstranquist/wip-commit`.
- Publish the `wip` command and a portable agent skill from this repository.
- Support shared checkouts and linked worktrees through the same capture
  transaction.
- Keep branch landing, merging, pushing, and remote coordination out of scope.
- Use `v0.1.0-beta.1` only after all public-beta gates pass.

The owner must accept this proposal before remote creation, first push, or tag
creation. Those actions can disclose history or publish an immutable Go module
version.

## Why this plan exists

The owning integration KEP controls the internal workflow. This plan controls
the standalone repository and its public release.

The detailed state is in
[OSS-PUBLIC-BETA.requirements.yaml](OSS-PUBLIC-BETA.requirements.yaml). The file
uses the JSON subset of YAML. The root Go test enforces its structure without
adding a YAML dependency.

Use these status values:

- `verified`: Local evidence passed on the recorded source.
- `prepared`: The repository artifact exists, but hosted evidence is absent.
- `planned`: Owner-controlled implementation work remains.
- `human-gated`: An owner decision or external mutation is required.
- `external-evidence`: A hosted system or independent person must supply proof.
- `deferred`: The item is outside the public-beta gate.

Do not convert a human gate or external-evidence gate into a failed
implementation claim.

## Local preparation result

The owner-controlled repository work is complete. The checkout now contains:

- a portable public skill with a regression test for private dependencies and
  unsafe commands.
- a deterministic six-target builder with archive, checksum, receipt, and
  host-binary tests.
- a tag-only prerelease workflow with immutable action references and GitHub
  artifact attestation.
- an explicit state compatibility policy and typed fail-closed errors for
  unsupported state.
- failure-boundary tests for state-directory, lane, lease, intent, and profile
  versions.

Two clean six-target rehearsals produced equal directory trees. Every recorded
archive checksum passed, and the native archive reported the target version.
This local proof does not satisfy hosted CI, attestation, independent testing,
public installation, owner approval, or publication gates.

## Product boundary

The public beta captures exact staged subsets into agent-owned local refs. It
does not land those refs onto a shared branch.

`wip init` is the onboarding entry point. It configures a shared lane or a
linked-worktree lane. It can install the current binary only after explicit
consent, and it does not overwrite different bytes.

Both modes use path leases, private indexes, bounded hooks, durable intents,
and an exact ref compare-and-swap. Worktree mode adds worktree ownership. It
does not use a different publication algorithm.

The portable agent skill must use only public `wip` commands and standard Git
inspection. It must not require private helpers, private paths, or an internal
catalog command. The integrated skill can add private policy around the public
command.

## Safety acceptance

The public beta must retain these properties:

1. A successful capture leaves the source `HEAD` unchanged.
2. A successful capture leaves the complete source index unchanged.
3. An unselected staged path cannot enter an agent commit.
4. A failed group, hook, gate, or ref race publishes none of a split chain.
5. Reconciliation accepts only the original immutable plan evidence.
6. Two disjoint shared lanes can capture at the same time.
7. A linked-worktree capture leaves both checkout heads and indexes unchanged.
8. Release and abort preserve the local agent ref.
9. No command lands, merges, pushes, stashes, resets, or cleans work.

The threat model remains part of the release contract. Cooperating processes
must honor leases. Hooks and `verify` commands remain trusted repository code.

## Release sequence

### Phase 1: accept the public contract

1. Approve the repository name and module path.
2. Accept or revise the capture-only boundary.
3. Accept the standalone skill ownership boundary.
4. Record the approval in this document and the requirement tracker.

### Phase 2: finish local public artifacts

1. Add the portable agent skill and its public-only regression tests.
2. Remove internal-only instructions from public contributor surfaces.
3. Add deterministic archives, checksums, and a provenance workflow.
4. Add a state-schema compatibility and migration policy.
5. Run the complete local gate with `GOWORK=off`.
6. Scan the complete history and worktree for secrets.

### Phase 3: create the hosted candidate

1. Create the public repository only after the owner authorizes it.
2. Push `main` without a release tag.
3. Enable private vulnerability reporting and security notifications.
4. Enable dependency alerts and secret scanning.
5. Protect `main` with pull requests, review, and required CI jobs.
6. Block force pushes and branch deletion.
7. Run Linux, macOS, and Windows jobs on hosted workers.

Hosted Windows execution is mandatory. A Windows cross-build does not exercise
Windows locks, process cleanup, or atomic replacement.

### Phase 4: prove the beta

1. Ask an independent tester to run shared and worktree setup.
2. Ask the tester to capture a split plan and reconcile an interrupted plan.
3. Record redacted results without repository paths or commit messages.
4. Fix every data-loss or foreign-content defect before release.
5. Repeat the hosted matrix after each release change.

### Phase 5: publish the beta

1. Create an approved, signed `v0.1.0-beta.1` tag.
2. Build release archives from that exact tag.
3. Publish checksums and provenance attestations with the archives.
4. Test one archive on each operating system.
5. Run the public `go install` command from a clean environment.
6. Request the module through the public Go proxy.
7. Change catalog and portfolio state only after public verification passes.

Never move or replace a published Go module tag. If a released version is
defective, publish a new beta tag.

## Local gate

Run this gate from the standalone repository:

```text
GOWORK=off go mod tidy -diff
GOWORK=off go mod verify
GOWORK=off go test -count=1 ./...
GOWORK=off go test -race -count=3 ./...
GOWORK=off go vet ./...
golangci-lint run ./...
GOWORK=off go run golang.org/x/vuln/cmd/govulncheck@v1.7.0 ./...
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 GOWORK=off go test -exec=/usr/bin/true ./...
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 GOWORK=off go test -exec=/usr/bin/true ./...
actionlint
GOWORK=off go run ./scripts/release --version v0.1.0-beta.1 --out <new-output-a>
GOWORK=off go run ./scripts/release --version v0.1.0-beta.1 --out <new-output-b>
diff -rq <new-output-a> <new-output-b>
gitleaks git --redact --exit-code 1 .
gitleaks dir --redact --exit-code 1 .
git diff --check
git fsck --strict --no-dangling
```

Review the pinned `govulncheck` version before each release audit. Record the
selected version in the evidence tracker.

## Release controls

The default-branch rule must require the operating-system jobs, race job, lint
job, dependency review, and one review. The rule must reject force pushes.

The release job must receive write permissions only in the tag workflow. Test
jobs retain read-only repository permissions. Pin every third-party action to a
reviewed commit.

The release must contain archives for these targets:

- Linux `amd64` and `arm64`.
- macOS `amd64` and `arm64`.
- Windows `amd64` and `arm64`.

Each archive must contain the binary, `LICENSE`, `README.md`, and
`THREAT-MODEL.md`. The release must include SHA-256 checksums and build
provenance. Follow [RELEASE.md](RELEASE.md) for the local rehearsal, approved
tag, hosted attestation, and verification sequence.

## Primary references

- Use GitHub [repository rulesets](https://docs.github.com/en/repositories/configuring-branches-and-merges-in-your-repository/managing-rulesets/available-rules-for-rulesets) for branch controls.
- Use GitHub [private vulnerability reporting](https://docs.github.com/en/code-security/how-tos/report-and-fix-vulnerabilities/configure-vulnerability-reporting/configure-for-a-repository) for confidential reports.
- Use GitHub [artifact attestations](https://docs.github.com/en/actions/concepts/security/artifact-attestations) for build provenance.
- Follow the Go [module publication sequence](https://go.dev/doc/modules/publishing) for the public tag.

## Evaluation and privacy

The command sends no product telemetry. Keep that default for the public beta.

Collect beta evidence through explicit, redacted reports. Record only the
`wip` version, operating system, Git version, command outcome, and error code.
Also record `ref_updated`, `intent_state`, and whether reconciliation was
required.

Do not collect repository names, paths, file contents, commit messages,
credentials, usernames, or remote addresses.

## Rollback and disclosure

If the configuration is wrong before the first push, delete the empty hosted
repository. Local history remains authoritative.

After the first push, treat the complete pushed history as disclosed. Removing
a secret from Git history does not revoke it. Revoke the credential first, then
publish repaired history under explicit owner direction.

After a tag is public, do not reuse that version. Mark the release as affected,
publish a fixed beta version, and document the upgrade.

## Stable release gate

`v1.0.0` requires a public beta period and real compatibility tests. It also
requires an independent security review and state migration policy. No open
data-loss or foreign-staged-content defect can remain.

Landing remains a separate product decision. If the public contract states the
boundary clearly, capture-only behavior can remain the stable scope.
