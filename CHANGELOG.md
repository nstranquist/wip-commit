# Changelog

All notable changes use [Keep a Changelog](https://keepachangelog.com/) and
Semantic Versioning.

## [Unreleased]

Planned first prerelease: `v0.1.0-beta.1`.

### Added

- A machine-readable `wip.command/2.0.0` capability contract and a digest-bound
  portable-skill contract.
- Preview/apply upgrades for the state marker, installed binary, and portable
  skill. Each apply requires the exact reviewed byte digest.
- A version 2 coordination-domain marker with a minimum reader protocol and
  required reader capabilities.
- Portable `wip-commit` agent skill with a public-only safety contract.
- Deterministic six-target release builder, checksums, receipt, and attested
  prerelease workflow.
- Pinned golangci-lint job for hosted pull-request and branch checks.
- State compatibility policy and explicit upgrade and downgrade procedures.
- Fail-closed, read-only pre-first-push receipt that binds the reviewed
  bootstrap, linear split-commit range, complete path delta, public author and
  committer identities, object integrity, owner approval, and secret scans.
- Human-only publication handoff for the first hosted pull request, exact CI
  check discovery, direct bootstrap fast-forward, and protected-branch setup.
- Fail-closed hosted-correction receipts that bind exact verifier source, local
  history, remote refs, pull-request state, no-bypass rulesets, and checks.
- Shared-checkout and detached linked-worktree lanes.
- Exact staged-subset capture through private Git indexes.
- All-or-nothing split plans with one final ref compare-and-swap.
- Conventional Commit validation, path leases, immutable hooks, bounded
  verification, durable intents, and exact reconciliation.
- Interactive and non-interactive `wip init` onboarding.
- Idempotent lane profiles and opt-in, no-overwrite binary installation.
- Strict bounded state readers, portable Unicode path identity, and Windows
  atomic replacement and file locks.
- NUL-delimited delete/add accounting for renamed paths.
- Operational status with capture-candidate, captured-staged, foreign-staged,
  empty-lease, expiry, ref-health, policy, and latest-intent fields, plus a
  strict public JSON Schema.
- One version 2 draft/reviewed plan schema with source, index, lease, lane, and
  message-policy bindings.
- `wip capture` with typed capture-only handoff output and optional post-handoff
  lane release.
- Named `conventional-v1` and `safety-only-v1` message policies.
- Automatic complete-set lease renewal on ordinary authenticated lane use.
- Runtime validation of real command data against the published capture JSON
  Schemas.
- Version 1 profile and intent compatibility without immutable digest changes.

### Changed

- Source builds now report `0.1.0-dev`; release builds receive both SemVer and
  exact tag identity from the deterministic release builder.
- A zero lock wait now fails after one acquisition attempt. Internal
  serialization uses an explicit bounded wait.
- Operational lane and lease reads now use the same registry fence as atomic
  record replacement. This prevents Windows sharing violations during capture.
- Unsupported state-directory, lane, lease, intent, and profile schemas now
  fail with `MIGRATION_REQUIRED` before the command changes state.
- Interactive initialization now recommends one linked worktree per task.
- Worktree initialization now requires a destination and reports where to
  continue. `env` rejects another checkout before lease renewal.
- Handoff validation, active renewal, and final guided release now keep one
  continuous lane lock. Completed receipts always revalidate Git objects.
- Released-lane handoff locks existing state without rewriting lock metadata
  or creating missing lock paths.
- Commit-message naming checks now run as lane policy before hooks. The capture
  kernel keeps only message byte, size, and subject safety checks.
- The minimum build toolchain is Go 1.25.12. This patched floor excludes
  GO-2026-4602, GO-2026-4864, and GO-2026-4970 from reachable standard-library
  paths found by the release vulnerability scan.

### Known limitations

- Coordination protects cooperating local processes. External Git commands can
  ignore leases.
- Windows process-descendant cleanup is best-effort after a command timeout.
- The beta captures local agent refs. It does not land, merge, or push them.

[Unreleased]: https://github.com/nstranquist/wip-commit/commits/main
