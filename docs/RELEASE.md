# Release a beta

This procedure builds and publishes a prerelease. A tag push starts the
release workflow. Do not create or push a tag until the repository owner
approves the tag and every public-beta gate passes. Approval of the repository
name and capture-only scope is not tag approval.

## Release boundary

The local release command creates files only. It does not create a tag, push a
ref, create a remote repository, or publish a release.

The hosted workflow runs only for a tag name that contains a prerelease
suffix, such as `v0.1.0-beta.1`. It builds six archives:

- Linux on AMD64 and ARM64.
- macOS on AMD64 and ARM64.
- Windows on AMD64 and ARM64.

Each archive contains the `wip` binary, license, README, and threat model. The
command also writes SHA-256 checksums and a JSON receipt. The receipt records
the source commit, source timestamp, Go version, file sizes, and digests.

## Rehearse locally

Review every release-triggered source in
[OSS-PRACTICE-GUIDE.md](OSS-PRACTICE-GUIDE.md). Update
`OSS-SOURCES.json` and the public-beta tracker when a source changes a project
requirement.

Use the exact minimum Go version that `go.mod` specifies. A clean scan from a
newer host toolchain does not prove that the minimum toolchain is safe. Start
from a clean checkout at the intended release commit. Confirm the selected
version and run all local gates:

```text
GOTOOLCHAIN=go1.25.12 GOWORK=off go version
GOTOOLCHAIN=go1.25.12 GOWORK=off go mod verify
GOTOOLCHAIN=go1.25.12 GOWORK=off go test -count=1 ./...
GOTOOLCHAIN=go1.25.12 GOWORK=off go test -race -count=3 ./...
GOTOOLCHAIN=go1.25.12 GOWORK=off go vet ./...
GOTOOLCHAIN=go1.25.12 GOWORK=off go run golang.org/x/vuln/cmd/govulncheck@v1.7.0 ./...
actionlint
```

Stop if `govulncheck` finds a reachable standard-library vulnerability. Raise
the `go` directive to a fixed patch release, then repeat every gate on the new
candidate.

Choose a new output path. The builder refuses an existing path and a dirty
checkout.

```text
GOTOOLCHAIN=go1.25.12 GOWORK=off go run ./scripts/release \
  --version v0.1.0-beta.1 \
  --out dist
```

Run the command twice from the same commit with the same Go toolchain. Use two
new output paths. The complete directory trees must be byte-for-byte equal.
Archive reproducibility across different Go toolchain versions is not a
release claim.

Check each digest from inside one output directory:

```text
shasum -a 256 -c checksums.txt
```

Inspect `release-receipt.json`. Check the version, source commit, source
timestamp, target names, sizes, and digests.

## Create the release

Complete the owner, hosted-CI, repository-settings, and independent-user gates
in the public-beta tracker. Record the evidence before tag creation.
Use [PUBLICATION-HANDOFF.md](PUBLICATION-HANDOFF.md) for the first push and
[HOSTED-SETUP.md](HOSTED-SETUP.md) for the hosted controls. Require a
non-author receipt produced with [BETA-EXERCISE.md](BETA-EXERCISE.md) and
validated against [BETA-EXERCISE.schema.json](BETA-EXERCISE.schema.json).

Before the tag, move the release notes from `Unreleased` to a
`0.1.0-beta.1` section with the actual tag date. Add the release comparison
links. Treat that edit as a source change: rerun every local and hosted gate on
the new commit before tag approval.

Create one signed tag at the approved commit:

```text
git tag -s v0.1.0-beta.1 -m "wip-commit v0.1.0-beta.1"
git tag -v v0.1.0-beta.1
git push origin refs/tags/v0.1.0-beta.1
```

The tag push starts `.github/workflows/release-beta.yml`. The workflow repeats
the tests, builds the archives, creates GitHub artifact attestations, and
publishes a GitHub prerelease. All third-party actions use full commit hashes.

Do not move, replace, or reuse a published tag. Increment the SemVer
prerelease number after any release failure that requires a source change.

## Check hosted evidence

After the workflow succeeds, download the release files in a clean directory.
Check `checksums.txt`, then check the attestations:

```text
gh attestation verify wip-commit_0.1.0-beta.1_linux_amd64.tar.gz \
  --repo nstranquist/wip-commit
```

Install the tagged module in a clean environment and run `wip version`. Keep
the hosted workflow URL, attestation result, checksum result, and installation
receipt in the public-beta evidence tracker.
