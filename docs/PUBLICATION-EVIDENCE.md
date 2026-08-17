# Public source publication evidence

Status: Public source candidate. No tag or GitHub release exists.

Observed on: 2026-08-17

Repository: [nstranquist/wip-commit](https://github.com/nstranquist/wip-commit)

Final commit: `206fa8b6a1dde1d97081133e4d447c0881849922`

Final tree: `c0dd6638adcdc231840ad06406dc9f0caa38e45d`

This record contains public or redacted evidence. It contains no token, email
address, private report, local path, or raw security log.

## Hosted discovery and correction

The pre-first-push receipt bound candidate `ed3f1fadfbc74eb0aa41ef8b90e41f403213d33d`
and its 17-path manifest. The first hosted run found three Windows host
assumptions and one repository setting gap.

- [The first run](https://github.com/nstranquist/wip-commit/actions/runs/31995610617)
  found CRLF-sensitive checks and a POSIX-only mode assertion.
- Dependency review also required the dependency graph.
- Commit `206fa8b6a1dde1d97081133e4d447c0881849922` fixed the three Windows findings.
- The correction expanded the complete bootstrap delta to 20 reviewed paths.
- The repository owner enabled dependency alerts and the dependency graph.

[Pull request 1](https://github.com/nstranquist/wip-commit/pull/1) verified the
corrected commit. The pull request closed without a merge commit.

The corrected commit passed these runs:

- [candidate push](https://github.com/nstranquist/wip-commit/actions/runs/31996054315).
- [candidate pull request](https://github.com/nstranquist/wip-commit/actions/runs/31996057770).
- [final `main` push](https://github.com/nstranquist/wip-commit/actions/runs/31996220707).
- [dependency graph](https://github.com/nstranquist/wip-commit/actions/runs/31996222126).

## Source-current correction receipt

The command in
[PUBLICATION-CORRECTION.md](PUBLICATION-CORRECTION.md) verified the completed
bootstrap sequence. It used the explicit `finalized` phase.

The private receipt bound:

- the immutable first receipt digest.
- bootstrap `b276204385636c5a8ac338491565bd4894255217`.
- old candidate `ed3f1fadfbc74eb0aa41ef8b90e41f403213d33d`.
- final candidate `206fa8b6a1dde1d97081133e4d447c0881849922`.
- final tree `c0dd6638adcdc231840ad06406dc9f0caa38e45d`.
- verifier commit `6f36147f24f614cff0c7010533d864f8d9ad7628` and tree
  `e9559e5fc7fa7083e471acb7d9e72e10b9c3110a`.
- verifier command and schema SHA-256 digests.
- one merge-free correction commit.
- the complete 20-path bootstrap delta.
- the complete 5-path correction delta.
- four successful hosted runs.
- six successful checks from GitHub Actions integration `15368`.
- closed pull request 1 with `merged: false`.
- final candidate and `main` refs at the same commit.
- the active no-bypass ruleset and all review, history, and update controls.

The receipt passed
[PUBLICATION-CORRECTION.schema.json](PUBLICATION-CORRECTION.schema.json). Its
SHA-256 digest is
`77db44ba2bfa6f007186ace931f38444521d8a29cf48bd945e07a801eda36a9a`.

The private file remains outside the repository. The public record contains no
identity value, local path, token, private report, or raw security log.

## Required checks

All required checks came from GitHub Actions integration `15368`.

| Required context | Passing pull-request job |
| --- | --- |
| `test (ubuntu-latest)` | [job 95287787614](https://github.com/nstranquist/wip-commit/actions/runs/31996057770/job/95287787614) |
| `test (macos-latest)` | [job 95287787690](https://github.com/nstranquist/wip-commit/actions/runs/31996057770/job/95287787690) |
| `test (windows-latest)` | [job 95287787600](https://github.com/nstranquist/wip-commit/actions/runs/31996057770/job/95287787600) |
| `race` | [job 95287787629](https://github.com/nstranquist/wip-commit/actions/runs/31996057770/job/95287787629) |
| `lint` | [job 95287787563](https://github.com/nstranquist/wip-commit/actions/runs/31996057770/job/95287787563) |
| `dependency-review` | [job 95287787528](https://github.com/nstranquist/wip-commit/actions/runs/31996057770/job/95287787528) |

## Hosted controls

[Ruleset 20926881](https://github.com/nstranquist/wip-commit/rules/20926881)
is active on `main`. It has no bypass actor.

The ruleset requires:

- a pull request.
- one approval.
- approval after the last push.
- dismissal of stale approvals.
- resolution of review conversations.
- linear history.
- the six provider-bound checks above.
- blocked force pushes and branch deletion.

The repository permits rebase merges only. Workflow tokens use read-only
permissions by default and cannot approve pull requests.

The owner enabled and verified:

- the dependency graph.
- Dependabot alerts.
- Dependabot security updates.
- secret scanning.
- secret-scanning push protection.
- private vulnerability reporting.

GitHub serves [the security policy](https://github.com/nstranquist/wip-commit/security/policy).
The maintainer's personal security-notification setting remains unverified.

## Public source smoke test

A clean test used an empty Git credential helper and cloned the public HTTPS
URL. The clone resolved the final commit and tree above.

This command installed the exact untagged commit through the public Go module
path and Go proxy:

```text
go install github.com/nstranquist/wip-commit/cmd/wip@206fa8b6a1dde1d97081133e4d447c0881849922
```

The module resolved to
`v0.0.0-20260817045403-206fa8b6a1dd`. The installed command returned
`wip 0.1.0-beta.1`.

This result proves public source installation at one commit. It does not prove
the tagged-install gate.

## Remaining public-beta gates

Do not create `v0.1.0-beta.1` until all these gates pass:

- Obtain a valid receipt from one independent beta tester.
- Approve a Code of Conduct and a confidential conduct-reporting path.
- Verify the maintainer's security-alert notifications.
- Obtain explicit owner approval for the tag.
- Run and verify the tag-only archive and attestation workflow.
- Install the final tag through the public Go module path.
