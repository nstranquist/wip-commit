# OSS readiness

## Current decision

The project now has a public source repository. The exact source candidate has
passing hosted checks and active repository controls.

No public beta tag or GitHub release exists. The evidence does not support a
released, launched, adopted, or stable claim.

The detailed release decision and current evidence are in
[OSS-PUBLIC-BETA.md](OSS-PUBLIC-BETA.md) and
[OSS-PUBLIC-BETA.requirements.yaml](OSS-PUBLIC-BETA.requirements.yaml). Public
execution evidence is in [PUBLICATION-EVIDENCE.md](PUBLICATION-EVIDENCE.md).

The plan is accepted. The approval fixes the name, module path, and
capture-only scope. It does not authorize a release tag.

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
- Explicit initialization ownership. Probes and other lane commands cannot
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
- An exact minimum Go patch gate that checks reachable standard-library
  vulnerabilities before release builds.
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
- A fail-closed pre-first-push receipt and exact human publication handoff.
- Passing hosted Linux, macOS, Windows, race, lint, and dependency-review
  checks on the exact public source candidate.
- An active no-bypass `main` ruleset with rebase-only pull requests, review,
  provider-bound checks, linear history, and ref protections.
- Enabled dependency alerts, security updates, secret scanning, push
  protection, and private vulnerability reporting.
- A clean public-module installation smoke test for the exact untagged commit.
- An accepted hosted-correction KEP, source-bound fail-closed command, schema,
  test matrix, and finalized private receipt for the first hosted correction.
- Fail-closed tests for unsupported state directories and record schemas.
- Symlink-escape and dual-domain creation-race tests.
- Concurrent first-use directory and no-clobber publication tests.
- Archive record-binding, partial-move, and receipt-free retry tests.
- Failure-boundary tests for oversized records, Git inspection, late
  heartbeats, orphaned lease links, and legacy preview state claims.
- Canonical Git repository and object-store binding despite inherited routing
  environment variables.

## Required before the public beta tag

1. Verify the maintainer's personal security-alert notifications.
2. Approve a Code of Conduct and configure confidential conduct reporting.
3. Ask at least one independent user to follow
   [BETA-EXERCISE.md](BETA-EXERCISE.md) for shared and worktree flows. Retain a
   redacted receipt that passes [BETA-EXERCISE.schema.json](BETA-EXERCISE.schema.json).
4. Obtain explicit owner approval for `v0.1.0-beta.1`.
5. Create the signed tag only after every tracked public-beta gate passes.
6. Verify the hosted archives, checksums, and attestations.
7. Install the final tag through the public Go module path.

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

The source repository, history, and hosted settings are public. The active
ruleset requires reviewed pull requests for later `main` updates.

No tag, release, announcement, or adoption evidence exists. Do not infer those
states from public source availability.
