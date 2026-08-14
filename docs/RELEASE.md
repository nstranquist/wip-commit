# Release a beta

This procedure builds and publishes a prerelease. A tag push starts the
release workflow. Do not create or push a tag until the repository owner
approves the public-beta plan.

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

Use the same Go version that `go.mod` specifies. Start from a clean checkout at
the intended release commit. Run all local gates:

```text
go mod verify
go test -count=1 ./...
go test -race -count=3 ./...
go vet ./...
actionlint
```

Choose a new output path. The builder refuses an existing path and a dirty
checkout.

```text
go run ./scripts/release \
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
