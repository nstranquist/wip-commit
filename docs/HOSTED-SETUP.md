# Hosted repository setup

## Authority boundary

Do not create the public repository until the owner approves the name, module
path, and capture-only boundary.

Do not create a release tag until every public-beta gate passes.

This runbook prepares the settings. It does not authorize a remote, push,
ruleset, security setting, or release.

## Source of truth

Use the current GitHub documentation when this runbook executes:

- [Managing repository rulesets](https://docs.github.com/en/repositories/configuring-branches-and-merges-in-your-repository/managing-rulesets)
- [Configuring private vulnerability reporting](https://docs.github.com/en/code-security/how-tos/report-and-fix-vulnerabilities/configure-vulnerability-reporting/configure-for-a-repository)
- [Configuring Dependabot alerts](https://docs.github.com/en/code-security/how-tos/secure-your-supply-chain/secure-your-dependencies/configure-dependabot-alerts)
- [Enabling secret scanning](https://docs.github.com/en/code-security/how-tos/secure-your-secrets/detect-secret-leaks/enable-secret-scanning)
- [Artifact attestations](https://docs.github.com/en/actions/concepts/security/artifact-attestations)

Review those pages again before the first push. GitHub settings and plan
availability can change.

## Repository creation

1. Confirm the approved owner and repository name.
2. Confirm `github.com/nstranquist/wip-commit` as the Go module path.
3. Create an empty public repository without generated files.
4. Add the remote to a clean candidate checkout.
5. Push only the approved candidate history.
6. Set `main` as the default branch.
7. Verify that the public tree equals the approved candidate tree.

Do not force-push or replace imported history.

## Main branch ruleset

Create one active branch ruleset for `main`. Do not grant a routine bypass.

Require:

- pull requests before updates.
- at least one approval.
- dismissal of stale approvals after new commits.
- resolution of review conversations.
- required status checks.
- linear history.
- blocked force pushes.
- blocked branch deletion.

Run the first pull request before you select status-check names. Use the exact
check names that GitHub reports for:

- Linux, macOS, and Windows tests.
- the race job.
- golangci-lint.
- dependency review.

Set the ruleset to active only after its target and required checks are exact.

## Security settings

Enable and verify:

- the dependency graph.
- Dependabot alerts.
- Dependabot security updates.
- secret scanning for the public repository.
- push protection when the repository plan provides it.
- private vulnerability reporting.
- security-alert notifications for the maintainer.

The repository already contains `SECURITY.md`. Verify that GitHub displays it
on the repository security page.

## Actions settings

Use read-only workflow permissions by default. Grant write permissions only in
the release job that needs them.

Keep every third-party action pinned to a complete commit ID. Verify that the
workflow does not persist checkout credentials.

Require approval before an untrusted fork workflow receives protected access.

## Release verification

After the approved prerelease workflow runs:

1. Download every archive, checksum file, and release receipt.
2. Verify `checksums.txt` in a clean directory.
3. Run `gh attestation verify` for each release archive.
4. Verify that the attestation names the approved repository and tag commit.
5. Install the tagged module in a clean environment.
6. Run `wip version`.
7. Record the workflow URL and redacted verification receipt.

An attestation has value only after a consumer verifies it.

## Evidence to retain

Keep these public or redacted records:

- owner approval for the name and module path.
- candidate commit and tree IDs.
- ruleset URL or exported settings.
- hosted job URLs for all required platforms.
- enabled security-setting receipt.
- private reporting test receipt.
- release checksums and attestations.
- clean installation receipt.
- unresolved gaps and their owners.

Do not store access tokens, notification addresses, private reports, or raw
security logs in the repository.
