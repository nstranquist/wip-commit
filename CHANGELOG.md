# Changelog

All notable changes use [Keep a Changelog](https://keepachangelog.com/) and
Semantic Versioning.

## [Unreleased]

### Added

- Portable `wip-commit` agent skill with a public-only safety contract.
- Deterministic six-target release builder, checksums, receipt, and attested
  prerelease workflow.
- State compatibility policy and explicit upgrade and downgrade procedures.

### Changed

- Unsupported state-directory, lane, lease, intent, and profile schemas now
  fail with `MIGRATION_REQUIRED` before the command changes state.

## [0.1.0-beta.1] - 2026-08-14

### Added

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

### Known limitations

- Coordination protects cooperating local processes. External Git commands can
  ignore leases.
- Windows process-descendant cleanup is best-effort after a command timeout.
- The beta captures local agent refs. It does not land, merge, or push them.

[Unreleased]: https://github.com/nstranquist/wip-commit/compare/v0.1.0-beta.1...HEAD
[0.1.0-beta.1]: https://github.com/nstranquist/wip-commit/releases/tag/v0.1.0-beta.1
