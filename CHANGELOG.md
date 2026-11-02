# Changelog

All notable changes use [Keep a Changelog](https://keepachangelog.com/) and
Semantic Versioning.

## [Unreleased]

Planned first prerelease: `v0.2.0-beta.1`.

### Added

- A machine-readable `wip.command/3.0.0` capability contract and a digest-bound
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
- Fail-closed, read-only pre-first-push receipt. It binds the reviewed
  bootstrap, linear split-commit range, complete path delta, public author and
  committer identities, object integrity, owner approval, and secret scans.
- Human-only publication handoff for the first hosted pull request, exact CI
  check discovery, direct bootstrap fast-forward, and protected-branch setup.
- Fail-closed hosted-correction receipts that bind exact verifier source, local
  history, remote refs, pull-request state, no-bypass rulesets, and checks.
- Shared-checkout and detached linked-worktree lanes.
- Exact `staged_index` and `worktree_snapshot` capture through private source
  snapshots and private Git indexes.
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
- One version 3 draft/reviewed plan schema with source mode, untracked policy,
  source digest, path, lease, lane, and message-policy bindings.
- `wip capture` with a digest-bound version 2 cumulative capture-only handoff
  and optional post-handoff lane release.
- A read-only `wip plan --observe` path that does not renew leases, change
  repository state, or write an output file.
- A digest-bound coordination migration with shadow comparison, one writer
  cutover, forward recovery, rollback before source movement, and a sealed
  legacy archive.
- A manifest-bound registry lock that is published before migrated lane and
  lease records become readable.
- Immutable `review/v1/handoffs` and `review/v1/plans` storage roots for the
  separate review and landing authority.
- Named `conventional-v1` and `safety-only-v1` message policies.
- Automatic complete-set lease renewal on ordinary authenticated lane use.
- Runtime validation of real command data against the published capture JSON
  Schemas.
- Version 1 profile and intent compatibility without immutable digest changes.
- Capture-intent schema version 3 with read compatibility for versions 1 and 2.
- Migration status schema version 1.1.0 with separate historical cutover and
  current live-store manifest digests.
- A canonical copied-hook environment with capture, phase, target-ref, commit,
  and source-read-only markers. Caller values cannot override these markers.
- Reviewed verifier materialization scopes with pre-checkout entry and byte
  limits, literal Git pathspecs, and NUL-delimited checkout input.

### Changed

- `wip commit --dry-run` now uses a fail-closed no-exec contract. It snapshots
  hooks and constructs the candidate structure without executing repository hooks,
  verification commands, publication callbacks, lease renewal, or repository
  Git writes.
  Result schema 3 reports skipped gates and exact execution counts.
- Source builds now report `0.2.0-dev`. Release builds receive both SemVer and
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
- A worktree initialization retry can start inside its existing linked
  worktree. It resolves and reports the primary checkout as the recovery
  anchor.
- Handoff validation, active renewal, and final guided release now keep one
  continuous lane lock. Completed receipts always revalidate Git objects.
- Handoff output now validates the unique complete lane chain from base to tip.
  A requested plan ID must name the tip segment.
- Worktree-source planning now uses a temporary index and object directory. It
  leaves the caller index and repository object database unchanged.
- Released-lane handoff locks existing state without rewriting lock metadata
  or creating missing lock paths.
- Commit-message naming checks now run as lane policy before hooks. The capture
  kernel keeps only message byte, size, and subject safety checks.
- The minimum build toolchain is Go 1.25.12. This patched floor excludes
  GO-2026-4602, GO-2026-4864, and GO-2026-4970 from reachable standard-library
  paths found by the release vulnerability scan.
- Completed migration status now accepts valid post-cutover writes in the two
  live data roots. It keeps exact checks for incomplete migrations and the
  static migration envelope.

### Known limitations

- Coordination protects cooperating local processes. External Git commands can
  ignore leases.
- Windows process-descendant cleanup is best-effort after a command timeout.
- The beta captures local agent refs. It does not land, merge, or push them.

[Unreleased]: https://github.com/nstranquist/wip-commit/commits/main
