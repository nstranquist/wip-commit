# Governance

## Current model

`wip-commit` is in local beta preparation. The repository owner is the sole
maintainer and has final decision authority. This document does not claim that
the project has a maintainer team or an active contributor community.

## Decision process

Use public issues and pull requests for decisions that do not contain a
security report or private repository data. Use the private process in
[SECURITY.md](SECURITY.md) for security reports.

The maintainer evaluates a change in this order:

1. Protect user work, staged content, refs, and recovery evidence.
2. Preserve the documented compatibility and product boundaries.
3. Require tests and review evidence that match the risk.
4. Keep the command and agent workflow small and understandable.

A change to the state format, capture transaction, trust boundary, supported
platforms, or public scope needs a written proposal before implementation. The
proposal must record the decision, alternatives, compatibility effect, and
recovery effect. Minor fixes can use a normal issue or pull request.

Use [docs/OSS-PRACTICE-GUIDE.md](docs/OSS-PRACTICE-GUIDE.md) for the official
source review that applies to these decisions.

## Roles

- A user runs released software and reports outcomes.
- A contributor proposes a change or supplies evidence.
- A reviewer evaluates a proposed change but cannot publish it.
- A maintainer can approve changes and manage repository settings and releases.

Contribution does not grant commit, release, or security-administration
access. New access must use least privilege, multi-factor authentication, and
review by an existing maintainer.

## Release authority

Only a maintainer can approve a release. A release still needs every gate in
[docs/OSS-PUBLIC-BETA.md](docs/OSS-PUBLIC-BETA.md). A maintainer cannot waive a
data-loss, foreign-content, provenance, or compatibility failure to publish a
version.

## Succession and inactivity

Before a stable release, the project must name a backup administrator or
document a tested transfer or archival process. If the maintainer can no longer
support the project, the repository must say that it is unmaintained, archive
it, or transfer it. It must not continue to imply active support.

## Amendments

Change this policy through a reviewed pull request. State the reason for a role,
authority, or release-policy change in the pull request.
