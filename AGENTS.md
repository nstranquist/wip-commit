# Agent instructions

- Read `README.md`, `THREAT-MODEL.md`, and `docs/ARCHITECTURE.md` before you
  change transaction code.
- Never stash, reset, clean, force-update, merge, or push as part of this
  project workflow.
- Use Go for production helpers.
- Keep state readers bounded and fail closed.
- Preserve source `HEAD`, the complete source index, foreign staged content,
  and exact ref compare-and-swap behavior.
- Add a failure-boundary test for each safety change.
- Run `go test ./...`, `go test -race -count=3 ./...`, `go vet ./...`, and
  `actionlint` before handoff.
- The canonical user-facing agent skill remains in its owning repository until
  an accepted extraction decision moves a portable version here.
