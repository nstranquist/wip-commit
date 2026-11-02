# Hosted source status

Observed on: 2026-08-18

Evidence scope: read-only GitHub API and remote-ref checks.

## Confirmed public state

The source repository is public at
[github.com/nstranquist/wip-commit](https://github.com/nstranquist/wip-commit).
Its default branch is `main`.

Live `main` was
`4cf003bebbfd9c6281578ea9ef1b679dae67078f` during this check. The latest
`main` CI run passed the Linux, macOS, Windows, race, and lint jobs. The
dependency-review job was skipped on the push event, as designed. A release
candidate must pass it on a pull-request event.

[CI run 32183872589](https://github.com/nstranquist/wip-commit/actions/runs/32183872589)
is the live `main` run that this record checked.

## Confirmed controls

Ruleset
[20926881](https://github.com/nstranquist/wip-commit/rules/20926881) is active
for the default branch. It has no bypass actor. It blocks deletion and
non-fast-forward updates. It requires linear history and one approving review.
It also requires approval after the last push and resolved review threads.
Pull requests must use rebase-only updates.

The ruleset requires these provider-bound checks:

- `test (ubuntu-latest)`
- `test (macos-latest)`
- `test (windows-latest)`
- `race`
- `lint`
- `dependency-review`

Workflow tokens use read-only permissions by default. They cannot approve pull
requests.

Private vulnerability reporting, secret scanning, secret-scanning push
protection, and Dependabot security updates are enabled.

## Lifecycle boundary

The repository has no Git tag and no GitHub release. Public source and passing
hosted checks do not prove a release, launch, adoption, or stable support.

The agent-facing workflow in KEP-0003 is local candidate work. It has not run on
hosted workers or landed on public `main`. Its exact final commit must pass the
required pull-request checks before a beta tag can use it.

These human or external gates remain:

- An independent beta exercise.
- An approved Code of Conduct and confidential reporting path.
- Maintainer security-notification verification.
- Explicit tag approval.
- Tag workflow, archive, checksum, and attestation verification.
- Clean installation from the final public tag.
