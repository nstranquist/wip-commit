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
- Keep the portable public skill in `skills/wip-commit/` dependent only on the
  public `wip` command and standard Git inspection. Internal integrations can
  add policy around this contract, but they must not weaken it.
