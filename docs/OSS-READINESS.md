# OSS readiness

## Current decision

The project is a strong candidate for its own public repository. The code and
local evidence support a `v0.1.0-beta.1` label. They do not yet support a stable
`v1.0.0` claim.

The detailed release decision and current evidence are in
[OSS-PUBLIC-BETA.md](OSS-PUBLIC-BETA.md) and
[OSS-PUBLIC-BETA.requirements.yaml](OSS-PUBLIC-BETA.requirements.yaml). The plan
is proposed. It does not authorize publication.

## Ready in this checkout

- MIT license
- SemVer beta version and changelog
- standalone Go module and `wip` binary
- interactive and non-interactive initialization
- shared-checkout and linked-worktree modes
- exact staged-subset and all-or-nothing split capture
- durable crash recovery
- strict JSON and Conventional Commit policy
- threat model, architecture, error guide, security policy, and contribution
  guide
- Linux, macOS, and Windows CI definition
- race-test CI definition
- dependency-update configuration
- issue and pull-request templates
- local unit, integration, race, vet, and cross-build commands
- full-history and worktree secret scans
- simultaneous disjoint shared-lane capture test
- end-to-end linked-worktree capture test

## Required before public beta publication

1. Confirm the repository name and the final module path.
2. Create the public repository and add a remote. This local checkout currently
   has no remote.
3. Run the prepared CI on hosted Linux, macOS, and Windows workers. Local cross
   compilation is not a substitute for Windows runtime tests.
4. Require pull-request review and passing CI on the default branch.
5. Enable private vulnerability reporting and dependency alerts.
6. Add the portable public agent skill without private helper dependencies.
7. Create signed or provenance-attested release archives and checksums.
8. Ask at least one independent user to test shared and worktree flows.
9. Run a clean public-module installation smoke test.
10. Tag `v0.1.0-beta.1` only after every tracked public-beta gate passes.

## Required before a stable release

- independent security review of locks, hook execution, state recovery, and
  Windows behavior;
- compatibility tests across supported Git versions and filesystems;
- a documented landing workflow or an explicit decision to remain capture-only;
- a deprecation and state-schema migration policy;
- measured use in real concurrent agent sessions;
- no unresolved data-loss or foreign-staged-content defects through a public
  beta period.

## Publication boundary

Creating a remote, pushing history, enabling repository settings, and publishing
a release are external changes. They require the repository owner's explicit
authorization. Preparing this checkout does not perform those actions.
