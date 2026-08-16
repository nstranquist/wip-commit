# OSS readiness

## Current decision

The project is a strong candidate for its own public repository. It is locally
publish-ready for a `v0.1.0-beta.1` candidate. It is not publicly launched, and
the local evidence does not support a stable `v1.0.0` claim.

The detailed release decision and current evidence are in
[OSS-PUBLIC-BETA.md](OSS-PUBLIC-BETA.md) and
[OSS-PUBLIC-BETA.requirements.yaml](OSS-PUBLIC-BETA.requirements.yaml). The plan
is proposed. It does not authorize publication.

## Ready in this checkout

- MIT license.
- SemVer beta version and changelog.
- Standalone Go module and `wip` binary.
- Interactive and non-interactive initialization.
- Durable initialization receipts, complete no-overwrite first writes, and
  resumable embedded-skill installation.
- Writer-side byte bounds that prevent unreadable durable records.
- Shared-checkout and linked-worktree modes.
- One coordination-domain interlock for standalone and legacy installations.
- Explicit initialization ownership; probes and other lane commands cannot
  claim an uninitialized repository.
- Exact staged-subset and all-or-nothing split capture.
- Read-only component split proposals and split-plan automation defaults.
- Automatic lease heartbeat and an exact final publication fence.
- Random no-clobber lease creation and fail-closed partial-release recovery.
- Exact init-claim repair and complete-registry renewal and release checks.
- Durable crash recovery.
- Bounded read-only state diagnosis.
- Exact-plan, receipt-based archive, resume, and restore operations that
  preserve refs and commits.
- Strict JSON and Conventional Commit policy.
- Threat model, architecture, error guide, security policy, and contribution
  guide.
- Honest single-maintainer governance, succession expectations, and beta
  support boundaries.
- A maintained official-source register and an open source practice guide with
  explicit review triggers.
- A self-hosted shared-checkout split receipt with verified failure isolation,
  ancestry, final-tree equality, index preservation, and duplicate refusal.
- Linux, macOS, and Windows CI definition.
- Race-test CI definition.
- Pinned golangci-lint CI definition.
- Dependency-update settings.
- Issue and pull-request templates.
- Local unit, integration, race, vet, and cross-build commands.
- Full-history and worktree secret scans.
- Simultaneous disjoint shared-lane capture test.
- End-to-end linked-worktree capture test.
- Portable public agent skill and private-dependency regression test.
- Deterministic six-target archives, checksums, and release receipt.
- Tag-only artifact-attestation workflow with immutable action references.
- State compatibility, upgrade, and downgrade policy.
- A proposed capture-to-landing KEP that keeps the beta capture-only and
  defines fail-closed requirements for any future landing command.
- A redacted independent beta exercise protocol and machine-checkable receipt
  schema.
- A human-gated hosted-repository setup and evidence runbook.
- Fail-closed tests for unsupported state directories and record schemas.
- Symlink-escape and dual-domain creation-race tests.
- Concurrent first-use directory and no-clobber publication tests.
- Archive record-binding, partial-move, and receipt-free retry tests.
- Failure-boundary tests for oversized records, Git inspection, late
  heartbeats, orphaned lease links, and legacy preview state claims.
- Canonical Git repository and object-store binding despite inherited routing
  environment variables.

## Required before public beta publication

1. Approve the repository name, module path, and capture-only boundary.
2. Create the public repository and add a remote. This local checkout currently
   has no remote.
3. Run the prepared CI on hosted Linux, macOS, and Windows workers. Local cross
   compilation is not a substitute for Windows runtime tests.
4. Follow [HOSTED-SETUP.md](HOSTED-SETUP.md) to require pull-request review and
   the observed passing CI checks on the default branch.
5. Enable private vulnerability reporting, dependency alerts, and secret
   scanning as described in the hosted setup runbook.
6. Approve a Code of Conduct and configure confidential conduct reporting
   before soliciting public contributions.
7. Ask at least one independent user to follow
   [BETA-EXERCISE.md](BETA-EXERCISE.md) for shared and worktree flows. Retain a
   redacted receipt that passes [BETA-EXERCISE.schema.json](BETA-EXERCISE.schema.json).
8. Run a clean public-module installation smoke test.
9. Create and push `v0.1.0-beta.1` only after every tracked public-beta gate
   passes. Check that the hosted release attestation succeeds.

## Required before a stable release

- Independent security review of locks, hook execution, state recovery, and
  Windows behavior.
- Compatibility tests across supported Git versions and filesystems.
- A documented landing workflow or an explicit capture-only decision.
- A tested migration command before the first state-schema change.
- Measured use in real concurrent agent sessions.
- A reviewed OpenSSF posture, an SBOM decision, and verified consumer-side
  provenance.
- Project-health evidence and a tested backup-administrator, transfer, or
  archival path.
- No unresolved data-loss or foreign-staged-content defects through a public
  beta period.

## Publication boundary

A remote, a history push, repository settings, and a published release are
external changes. They require the repository owner's explicit authorization.
Preparing this checkout does not perform those actions.
