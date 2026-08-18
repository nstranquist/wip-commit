# OSS public beta plan

Plan ID: `wip-oss-public-beta`

Status: Accepted

Target: `v0.1.0-beta.1`

Owner: Repository owner

Last review: 2026-08-16

Approval: `owner-session-2026-08-16`

Local preparation: Extended safety pass complete on 2026-08-16

## Approved decision

The owner approved a capture-only public beta with these boundaries:

- Keep the repository name `wip-commit`.
- Keep the module path `github.com/nstranquist/wip-commit`.
- Publish the `wip` command and a portable agent skill from this repository.
- Support shared checkouts and linked worktrees through the same capture
  transaction.
- Keep branch landing, merging, pushing, and remote coordination out of scope.
- Use `v0.1.0-beta.1` only after all public-beta gates pass.

## Hosted state on 2026-08-17

The public source repository now exists at
[`nstranquist/wip-commit`](https://github.com/nstranquist/wip-commit).
Commit `206fa8b6a1dde1d97081133e4d447c0881849922` passed the hosted candidate and
final `main` checks.

The active `main` ruleset and security settings passed the hosted setup audit.
[PUBLICATION-EVIDENCE.md](PUBLICATION-EVIDENCE.md) contains the public record.

No beta tag or GitHub release exists. The independent beta, conduct, correction
receipt, security-notification, tag-approval, and tagged-install gates remain.

This approval fixes the repository name, module path, and capture-only scope.
It does not approve a release tag. The first-push handoff ran on 2026-08-17.
Do not run [PUBLICATION-HANDOFF.md](PUBLICATION-HANDOFF.md) again.

## Why this plan exists

The owning integration KEP controls the internal workflow. This plan controls
the standalone repository and its public release.

The detailed state is in
[OSS-PUBLIC-BETA.requirements.yaml](OSS-PUBLIC-BETA.requirements.yaml). The file
uses the JSON subset of YAML. The root Go test enforces its structure without
adding a YAML dependency.

Use these status values:

- `verified`: Recorded local or hosted evidence passed on the exact source.
- `prepared`: The repository artifact exists, but hosted evidence is absent.
- `planned`: Owner-controlled implementation work remains.
- `human-gated`: An owner decision or external mutation is required.
- `external-evidence`: A hosted system or independent person must supply proof.
- `deferred`: The item is outside the public-beta gate.

Do not convert a human gate or external-evidence gate into a failed
implementation claim.

## Local preparation result

The owner-controlled repository work is complete. The checkout now contains:

- a single-domain interlock that prevents standalone and legacy coordination
  state from starting in the same Git common directory. Only `wip init` can
  claim an uninitialized repository.
- a resumable `wip init` transaction with safe binary and embedded-skill
  installation, complete no-overwrite first writes, and immutable recovery
  values.
- no-clobber lease creation, a capture heartbeat, a final exact-set publication
  fence, and refusal of partially released lanes.
- read-only split proposals and a split-plan default for automation.
- bounded `wip doctor` inspection, structural capture-receipt validation, and
  exact recoverable state archival.
- receipt-bound archive records and recovery of a deterministic empty batch
  when interruption occurs before receipt publication.
- filesystem-root confinement for persistent state and private-index
  directories.
- a portable public skill with a regression test for private dependencies and
  unsafe commands.
- a deterministic six-target builder with archive, checksum, receipt, and
  host-binary tests.
- a tag-only prerelease workflow with immutable action references and GitHub
  artifact attestation.
- an explicit state compatibility policy and typed fail-closed errors for
  unsupported state.
- failure-boundary tests for state-directory, lane, lease, intent, and profile
  versions.
- an exact minimum Go 1.25.12 gate that excludes reachable standard-library
  vulnerabilities found in Go 1.25.0.

Two clean six-target rehearsals produced equal directory trees. Every recorded
archive checksum passed, and the native archive reported the target version.
This local proof does not satisfy hosted CI, attestation, independent testing,
public installation, or publication gates.

## Product boundary

The public beta captures exact staged subsets into agent-owned local refs. It
does not land those refs onto a shared branch.

`wip init` is the onboarding entry point. It configures a shared lane or a
linked-worktree lane. It can install the current binary only after explicit
consent, and it does not overwrite different bytes.

Both modes use path leases, private indexes, bounded hooks, durable intents,
and an exact ref compare-and-swap. Worktree mode adds worktree ownership. It
does not use a different publication algorithm.

The portable agent skill must use only public `wip` commands and standard Git
inspection. It must not require private helpers, private paths, or an internal
catalog command. The integrated skill can add private policy around the public
command.

## Safety acceptance

The public beta must retain these properties:

1. A successful capture leaves the source `HEAD` unchanged.
2. A successful capture leaves the complete source index unchanged.
3. An unselected staged path cannot enter an agent commit.
4. A failed group, hook, gate, or ref race publishes none of a split chain.
5. Reconciliation accepts only the original immutable plan evidence.
6. Two disjoint shared lanes can capture at the same time.
7. A linked-worktree capture leaves both checkout heads and indexes unchanged.
8. Release and abort preserve the local agent ref.
9. No command lands, merges, pushes, stashes, resets, or cleans work.
10. Public and legacy coordination domains cannot start beside each other.
11. A long capture keeps the exact active lane lease set captured at its start
    alive and checks that same set again before publication.
12. After an empty coordination-store bootstrap, initialization records every
    durable step and exact retry value before it changes worktree, lane, lease,
    profile, binary, or skill resources.
13. State inspection is read-only and bounded.
14. Archive apply rechecks every reviewed candidate under lock and preserves
    all lane refs and commits.
15. A prepared archive can resume or restore by immutable receipt ID.
16. Commands other than `wip init` cannot claim an uninitialized repository.
17. New lease publication cannot replace an existing lease record. A partial
    release blocks claim, renewal, and capture until release resumes.
18. Every durable record writer rejects bytes that its corresponding reader
    cannot read. Capture intent rejection occurs before the lane ref update.
19. An exact initialization retry repairs an exact active lease that lacks its
    lane back-reference. Git inspection errors cannot appear as a passed check.
20. A heartbeat failure after ref publication returns the applied plan evidence
    and requires receipt-based reconciliation.
21. Invalid legacy actions, legacy dry-runs, queue previews, archive resume, and
    archive restore cannot claim an uninitialized coordination domain.
22. Renewal and release audit the complete lease registry before mutation.
    Release also verifies ownership and reverse references.
23. Portable path keys are idempotent for valid UTF-8 paths. Overlap stays
    symmetric across Unicode case pairs and component boundaries.
24. Inherited Git variables cannot redirect repository discovery, refs, object
    storage, or prepared hooks away from the selected canonical checkout.

The threat model remains part of the release contract. Cooperating processes
must honor leases. Hooks and `verify` commands remain trusted repository code.

## Release sequence

### Phase 1: accept the public contract — complete

The owner approved the repository name, module path, capture-only boundary, and
standalone skill ownership boundary on 2026-08-16. The requirement tracker
records the same approval reference.

### Phase 2: finish local public artifacts

1. Add the portable agent skill and its public-only regression tests.
2. Remove internal-only instructions from public contributor surfaces.
3. Add a single-domain compatibility contract for legacy integrations.
4. Add resumable initialization and embedded portable-skill installation.
5. Add automatic capture lease renewal and final lease fencing.
6. Add bounded state diagnosis and exact recoverable archival.
7. Add deterministic archives, checksums, and a provenance workflow.
8. Add a state-schema compatibility and migration policy.
9. Add governance and support policies that state the current single-maintainer
   model and do not imply a service level or contributor community.
10. Record the capture-only beta boundary and future landing requirements in
    [KEP-0001](KEP-0001-capture-to-landing-boundary.md).
11. Add the redacted [independent beta exercise](BETA-EXERCISE.md), its
    [receipt schema](BETA-EXERCISE.schema.json), and the human-gated
    [hosted setup runbook](HOSTED-SETUP.md).
12. Run the complete local gate with `GOWORK=off`.
13. Scan the complete history and worktree for secrets.

### Phase 3: create the hosted candidate

Follow [PUBLICATION-HANDOFF.md](PUBLICATION-HANDOFF.md), then
[HOSTED-SETUP.md](HOSTED-SETUP.md), and retain their redacted evidence.
The exact required CI check names come from the first hosted pull request.
Do not guess them from workflow job labels.

1. Create the public repository only after the owner authorizes it.
2. Push `main` without a release tag.
3. Enable private vulnerability reporting and security notifications.
4. Enable dependency alerts and secret scanning.
5. Protect `main` with pull requests, review, and required CI jobs.
6. Block force pushes and branch deletion.
7. Run Linux, macOS, and Windows jobs on hosted workers.
8. Approve a Code of Conduct, assign enforcement responsibility, and configure
   a confidential conduct-reporting path before soliciting contributions.

Hosted Windows execution is mandatory. A Windows cross-build does not exercise
Windows locks, process cleanup, or atomic replacement.

### Phase 4: prove the beta

1. Ask a non-author to follow [BETA-EXERCISE.md](BETA-EXERCISE.md).
2. Require all six scenarios, including shared and worktree split capture,
   failure isolation, duplicate refusal, and reconciliation.
3. Validate the redacted receipt against
   [BETA-EXERCISE.schema.json](BETA-EXERCISE.schema.json). Do not accept
   repository paths, branch names, commit messages, file names, or contents.
4. Fix every data-loss or foreign-content defect before release.
5. Repeat the hosted matrix after each release change.

### Phase 5: publish the beta

1. Create an approved, signed `v0.1.0-beta.1` tag.
2. Build release archives from that exact tag.
3. Publish checksums and provenance attestations with the archives.
4. Test one archive on each operating system.
5. Run the public `go install` command from a clean environment.
6. Request the module through the public Go proxy.
7. Change catalog and portfolio state only after public verification passes.

Never move or replace a published Go module tag. If a released version is
defective, publish a new beta tag.

## Local gate

Run this gate from the standalone repository. Keep `GOTOOLCHAIN` equal to the
exact minimum in `go.mod`:

```text
GOTOOLCHAIN=go1.25.12 GOWORK=off go mod tidy -diff
GOTOOLCHAIN=go1.25.12 GOWORK=off go mod verify
GOTOOLCHAIN=go1.25.12 GOWORK=off go test -count=1 ./...
GOTOOLCHAIN=go1.25.12 GOWORK=off go test -race -count=3 ./...
GOTOOLCHAIN=go1.25.12 GOWORK=off go vet ./...
golangci-lint run ./...
GOTOOLCHAIN=go1.25.12 GOWORK=off go run golang.org/x/vuln/cmd/govulncheck@v1.7.0 ./...
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 GOTOOLCHAIN=go1.25.12 GOWORK=off go test -exec=/usr/bin/true ./...
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 GOTOOLCHAIN=go1.25.12 GOWORK=off go test -exec=/usr/bin/true ./...
CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 GOTOOLCHAIN=go1.25.12 GOWORK=off go test -exec=/usr/bin/true ./...
CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 GOTOOLCHAIN=go1.25.12 GOWORK=off go test -exec=/usr/bin/true ./...
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 GOTOOLCHAIN=go1.25.12 GOWORK=off go test -exec=/usr/bin/true ./...
CGO_ENABLED=0 GOOS=windows GOARCH=arm64 GOTOOLCHAIN=go1.25.12 GOWORK=off go test -exec=/usr/bin/true ./...
actionlint
GOTOOLCHAIN=go1.25.12 GOWORK=off go run ./scripts/release --version v0.1.0-beta.1 --out <new-output-a>
GOTOOLCHAIN=go1.25.12 GOWORK=off go run ./scripts/release --version v0.1.0-beta.1 --out <new-output-b>
diff -rq <new-output-a> <new-output-b>
gitleaks git --redact --exit-code 1 .
gitleaks dir --redact --exit-code 1 .
git diff --check
git fsck --strict --no-dangling
```

Review the pinned `govulncheck` version before each release audit. Record the
selected version in the evidence tracker. Stop if the exact minimum Go
toolchain has a reachable vulnerability, even when a newer host toolchain is
clean.

## Release controls

The default-branch rule must require the operating-system jobs, race job, lint
job, dependency review, and one review. The rule must reject force pushes.

The release job must receive write permissions only in the tag workflow. Test
jobs retain read-only repository permissions. Pin every third-party action to a
reviewed commit.

The release must contain archives for these targets:

- Linux `amd64` and `arm64`.
- macOS `amd64` and `arm64`.
- Windows `amd64` and `arm64`.

Each archive must contain the binary, `LICENSE`, `README.md`, and
`THREAT-MODEL.md`. The release must include SHA-256 checksums and build
provenance. Follow [RELEASE.md](RELEASE.md) for the local rehearsal, approved
tag, hosted attestation, and verification sequence.

## Primary references

- Use GitHub [repository rulesets](https://docs.github.com/en/repositories/configuring-branches-and-merges-in-your-repository/managing-rulesets/available-rules-for-rulesets) for branch controls.
- Use GitHub [private vulnerability reporting](https://docs.github.com/en/code-security/how-tos/report-and-fix-vulnerabilities/configure-vulnerability-reporting/configure-for-a-repository) for confidential reports.
- Use GitHub [artifact attestations](https://docs.github.com/en/actions/concepts/security/artifact-attestations) for build provenance.
- Follow the Go [module publication sequence](https://go.dev/doc/modules/publishing) for the public tag.
- Use [Producing Open Source Software](https://producingoss.com/en/producingoss.html)
  for project, governance, release, and succession practices.
- Use the [GitHub Open Source Guides](https://opensource.guide/) for community
  health and maintainer practices.
- Review the OpenSSF [Concise Guide for Developing More Secure Software](https://github.com/ossf/wg-best-practices-os-developers/blob/main/docs/Concise-Guide-for-Developing-More-Secure-Software.md)
  and [Concise Guide for Evaluating Open Source Software](https://github.com/ossf/wg-best-practices-os-developers/blob/main/docs/Concise-Guide-for-Evaluating-Open-Source-Software.md).
- Map the secure-development lifecycle to the NIST
  [Secure Software Development Framework](https://csrc.nist.gov/pubs/sp/800/218/final).

## Evaluation and privacy

The command sends no product telemetry. Keep that default for the public beta.

Collect beta evidence through explicit, redacted reports. Record only the
`wip` version, operating system, Git version, command outcome, and error code.
Also record `ref_updated`, `intent_state`, and whether reconciliation was
required. For initialization or archival recovery, record only the typed state
and whether the exact receipt-based retry completed.

Do not collect repository names, paths, file contents, commit messages,
credentials, usernames, or remote addresses.

## Rollback and disclosure

If the configuration is wrong before the first push, delete the empty hosted
repository. Local history remains authoritative.

After the first push, treat the complete pushed history as disclosed. Removing
a secret from Git history does not revoke it. Revoke the credential first, then
publish repaired history under explicit owner direction.

After a tag is public, do not reuse that version. Mark the release as affected,
publish a fixed beta version, and document the upgrade.

## Stable release gate

`v1.0.0` requires a public beta period and real compatibility tests. It also
requires an independent security review and state migration policy. No open
data-loss or foreign-staged-content defect can remain.

Landing remains a separate product decision. If the public contract states the
boundary clearly, capture-only behavior can remain the stable scope.
