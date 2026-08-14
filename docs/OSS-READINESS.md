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
- Shared-checkout and linked-worktree modes.
- Exact staged-subset and all-or-nothing split capture.
- Durable crash recovery.
- Strict JSON and Conventional Commit policy.
- Threat model, architecture, error guide, security policy, and contribution
  guide.
- Linux, macOS, and Windows CI definition.
- Race-test CI definition.
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
- Fail-closed tests for unsupported state directories and record schemas.

## Required before public beta publication

1. Approve the repository name, module path, and capture-only boundary.
2. Create the public repository and add a remote. This local checkout currently
   has no remote.
3. Run the prepared CI on hosted Linux, macOS, and Windows workers. Local cross
   compilation is not a substitute for Windows runtime tests.
4. Require pull-request review and passing CI on the default branch.
5. Enable private vulnerability reporting and dependency alerts.
6. Ask at least one independent user to test shared and worktree flows.
7. Run a clean public-module installation smoke test.
8. Create and push `v0.1.0-beta.1` only after every tracked public-beta gate
   passes. Check that the hosted release attestation succeeds.

## Required before a stable release

- Independent security review of locks, hook execution, state recovery, and
  Windows behavior.
- Compatibility tests across supported Git versions and filesystems.
- A documented landing workflow or an explicit capture-only decision.
- A tested migration command before the first state-schema change.
- Measured use in real concurrent agent sessions.
- No unresolved data-loss or foreign-staged-content defects through a public
  beta period.

## Publication boundary

A remote, a history push, repository settings, and a published release are
external changes. They require the repository owner's explicit authorization.
Preparing this checkout does not perform those actions.
