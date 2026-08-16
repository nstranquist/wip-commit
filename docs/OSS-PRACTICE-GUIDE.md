# Open source practice guide

## Purpose

This guide turns external open source guidance into project rules. It also
records when a maintainer must review the sources again.

The machine-readable source register is in
[OSS-SOURCES.json](OSS-SOURCES.json). Each source entry records its authority,
reviewed revision, and intended use.

An external guide is advisory evidence. The repository contract, tests, and
recorded decisions remain authoritative for `wip-commit`.

## Source selection

Use primary or official sources. Prefer a maintained HTML page or source
repository over an unofficial summary.

Use each source for its documented purpose:

| Question | Source |
| --- | --- |
| Is the project honest, governable, and maintainable? | Producing Open Source Software |
| Are community files, roles, and contributor paths complete? | GitHub Open Source Guides |
| Does development follow secure defaults and review practices? | OpenSSF secure-development guide and NIST SSDF |
| Is a dependency suitable and securely usable? | OpenSSF evaluation guide |
| Can Git environment or configuration change command behavior? | Pro Git and the installed Git manual |
| How must hosted branch, security, and provenance controls work? | Current GitHub repository and security documentation |

Review the exact source, not only its title or table of contents. Record the
review date and source revision in `OSS-SOURCES.json`.

## Required review triggers

Review the applicable sources before these events:

1. Create the first public repository or push its first history.
2. Prepare any release candidate.
3. Approve a stable release.
4. Change the state format, capture transaction, or trust boundary.
5. Add a supported operating system, filesystem, or Git version.
6. Respond to a security or data-integrity incident.
7. Transfer maintainer or repository-administrator authority.

If a source changed, record the new revision. Then review the affected project
requirements and tests.

## Review method

1. State the project decision or risk that needs evidence.
2. Select the applicable official sources from `OSS-SOURCES.json`.
3. Read the relevant source sections.
4. Record each learning as a project requirement, artifact, test, or external
   gate.
5. Mark local proof, hosted proof, human approval, and operational evidence as
   different evidence types.
6. Update the public-beta tracker when a requirement changes.
7. Record accepted gaps. Do not convert an absent external result into local
   proof.

Automated repository dissection can identify files, history, languages, and
maintainer concentration. Treat that output as navigation evidence. A generated
stub, inferred label, or score is not semantic review evidence.

Do not store copyrighted textbook bodies without permission. Bibliographic
metadata, public table-of-contents facts, and legally licensed source material
are sufficient for source discovery.

## Applied project rules

### Mission and status

State the user, problem, and product boundary in the README. Keep local
preparation, public launch, stable support, and external adoption as separate
claims.

Do not call the project stable before the stable gates pass. Do not imply that
a local release rehearsal is a hosted release.

### Governance and community

Document current authority, decision rules, access rules, and succession. Do
not imply that a maintainer team or community exists before evidence supports
that claim.

Provide contribution, support, security, and conduct policies before public
contribution intake. Configure a confidential conduct-reporting path before
the project solicits contributions.

Review maintainer load and response measurements after the public beta starts.
Use those measurements to change support claims or contributor processes.

### Secure development

Document security requirements and the trust boundary. Add adversarial tests
for each safety-sensitive change.

Use negative tests for data loss, foreign staged content, races, path aliases,
symlink escape, corrupt state, and interrupted writes. Keep state readers
bounded and make unsupported state fail closed.

Review dependencies before adoption. Record their purpose, license,
maintenance evidence, vulnerability posture, and secure-use constraints.

Use private vulnerability reporting. Preserve evidence during an incident.
After a fix, add a regression test and review the same failure class elsewhere.

### Git correctness

Treat Git environment variables as part of the command input. Bind discovery,
refs, worktrees, object storage, and hooks to the selected repository.

Keep the active source index explicit. Give capture hooks the private candidate
index. Verify the source `HEAD`, complete index, selected tree, and expected old
ref at the publication boundary.

Do not use stash, reset, clean, force-update, merge, or push as a capture
recovery action.

### Releases and supply chain

Use SemVer. Never replace or reuse a published tag.

Build from an approved exact commit. Publish checksums and provenance for the
same source. Verify an archive and public installation from a clean consumer
environment.

Use least privilege and multi-factor authentication for repository access.
Require review and hosted tests on protected branches.
Select required checks from the names reported by the first hosted pull
request. Do not infer those names from local workflow labels. Follow
[HOSTED-SETUP.md](HOSTED-SETUP.md) for current repository settings and retained
evidence.

Before a stable release, review OpenSSF Scorecard and Best Practices criteria.
Record the SBOM decision and every accepted supply-chain gap.

### Operations and support

State the supported versions and support limits. Do not promise a response
time without measured capacity.

Collect only explicit, redacted beta evidence. Do not collect repository names,
paths, contents, messages, credentials, usernames, or remote addresses.

If maintenance stops, mark the project as unmaintained, archive it, or transfer
it. Do not imply active support after maintenance ends.

## Current application to `wip-commit`

| Practice | Current evidence | Remaining evidence |
| --- | --- | --- |
| Clear mission and truthful beta status | `README.md`, `docs/OSS-READINESS.md` | Public use and adoption |
| Governance, support, and succession | `GOVERNANCE.md`, `SUPPORT.md` | Backup administrator or tested transfer |
| Contribution and security paths | `CONTRIBUTING.md`, `SECURITY.md` | Approved Code of Conduct and confidential conduct channel |
| Threat model and secure defaults | `THREAT-MODEL.md`, adversarial tests | Independent security review |
| Split capture and exact publication | Private index, receipts, lease fencing, exact ref compare-and-swap | Independent shared and worktree exercises |
| Release integrity | Deterministic builder, checksums, pinned workflow, attestations | Hosted execution and clean consumer verification |
| Hosted controls | Pinned workflows and `docs/HOSTED-SETUP.md` | Owner-approved repository settings and hosted receipts |
| Supply-chain posture | Dependency review, vulnerability scan, secret scan | Scorecard review, Best Practices review, and SBOM decision |
| Project health | Redacted beta evidence contract | Public response and maintainer-load measurements |

The detailed gate state is in
[OSS-PUBLIC-BETA.requirements.yaml](OSS-PUBLIC-BETA.requirements.yaml). Do not
infer a passed gate from this summary table.

## Maintenance receipt

For each source review, record:

- the review date.
- the exact source revision or publication identifier.
- the project decision that required the review.
- the affected requirement identifiers.
- the artifacts and tests that changed.
- each accepted gap and its owner.

Keep this guide small. Add a rule only when an official source, project
decision, incident, or test supports it.
