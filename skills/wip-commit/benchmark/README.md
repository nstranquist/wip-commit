# WIP Commit Portable Behavioral Benchmark

This deterministic, zero-AI benchmark runs the shipped `wip` entrypoints in
disposable Git repositories. It checks the same semantic cases on Linux,
macOS, and Windows:

- shared and linked-worktree setup.
- concurrent disjoint lanes and an overlapping-lease refusal.
- reviewed split-plan defaults and draft-plan refusal.
- the explicit `--single` opt-out.
- complete source-index and foreign-stage preservation.
- dry-run non-publication and a non-dry sensitivity control.
- failed verification non-publication and a passing-gate sensitivity control.
- reconciliation of a reproduced applied-intent crash boundary.

Every case has one positive assertion and one negative control. All 16 checks
must pass for a report to match the reviewed baseline. Reports contain no
fixture paths, credentials, file contents, network results, or AI output.

## Run

From this repository:

```text
go run ./skills/wip-commit/benchmark/regression.go
```

The default mutable report is:

```text
~/.nicos-dev/benchmarks/wip-commit/latest-report.json
```

Use `--out-dir <path>` for an isolated report directory. Use
`--candidate <path>` to evaluate an already-built executable.

## Prove the negative control

```text
go run ./skills/wip-commit/benchmark/regression.go --prove-weakened
```

This builds the deliberately unsafe no-op candidate under
`testdata/weakened` and runs the full case matrix. The proof requires the
normal benchmark to reject that candidate. It writes `weakened-report.json`.
Running the unsafe executable through the ordinary `--candidate` path exits
nonzero.

## Baseline policy

`baseline.json` is reviewed repository data. The runner never writes or
promotes it. Review the case IDs, required check counts, and minimum score for
each baseline change. A failing candidate can write only mutable artifacts
outside the repository. Thus, it cannot replace the reviewed baseline.

When a candidate falls below baseline, the runner writes `proposal.json` with
the failed case IDs and exits nonzero. The report schema, benchmark version,
score, cases, elapsed milliseconds, and zero-AI counters are bounded and
versioned.
