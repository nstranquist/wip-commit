# Official source review: completed migration status

Date: 2026-08-27

## Decision

A completed coordination migration is historical cutover evidence. It must not
freeze the two live coordination stores. Migration status must report a later
live-store manifest without weakening exact checks before completion or exact
checks for immutable migration evidence.

## Sources

- OpenSSF Concise Guide for Developing More Secure Software, main revision
  `eb54fd9f4264bebab64586d92dffc914fbe8c896`. The applicable rule requires
  automated negative tests and review before a change is accepted.
- NIST SP 800-218 version 1.1, February 2022. Practices PW.8.2, PS.3.1, and
  PS.3.2 require repeatable security testing and protected integrity and
  provenance evidence.
- Pro Git, second edition, environment variables chapter, reviewed with Git
  2.55.0. `GIT_DIR`, `GIT_WORK_TREE`, `GIT_INDEX_FILE`, and object-directory
  variables can change repository behavior. The selected Git common directory
  and private-index root must remain explicit.

## Requirements and evidence

- A status read before migration completion still requires the exact reviewed
  canonical manifest.
- A completed status checks the immutable plan, journal, receipt, sealed
  archive, domain marker, and static migration envelope.
- A completed status reports the current canonical manifest separately from
  the cutover receipt.
- The status reader does not enumerate transient private-index contents. It
  rejects an unsafe private-index root.
- `OSS-015` tracks the affected stable gate.
- The full `internal/coordmigration` package, its race suite, and `go vet
  ./...` passed on 2026-08-27.

## Accepted gaps

- The completed status does not check every current lane, lease, intent, or
  queue record. `wip --json doctor` remains the bounded current-state
  validator. Owner: maintainer.
- This local review does not replace the independent security review required
  by `OSS-016`. Owner: independent reviewer.
