# Changelog

All notable changes use [Keep a Changelog](https://keepachangelog.com/) and
Semantic Versioning.

## [Unreleased]

Planned first prerelease: `v0.1.0-beta.1`.

### Added

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

### Changed

- Operational lane and lease reads now use the same registry fence as atomic
  record replacement. This prevents Windows sharing violations during capture.
- Unsupported state-directory, lane, lease, intent, and profile schemas now
  fail with `MIGRATION_REQUIRED` before the command changes state.
- The minimum build toolchain is Go 1.25.12. This patched floor excludes
  GO-2026-4602, GO-2026-4864, and GO-2026-4970 from reachable standard-library
  paths found by the release vulnerability scan.

### Known limitations

- Coordination protects cooperating local processes. External Git commands can
  ignore leases.
- Windows process-descendant cleanup is best-effort after a command timeout.
- The beta captures local agent refs. It does not land, merge, or push them.

[Unreleased]: https://github.com/nstranquist/wip-commit/commits/main
