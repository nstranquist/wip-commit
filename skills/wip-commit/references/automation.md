# Automated capture

Use JSON mode so the caller can check typed evidence. Do not accept a zero exit
status as the only proof of capture.

## Plan shape

Write one ordered object for each coherent commit:

```json
[
  {
    "message": "fix(parser): preserve source locations",
    "files": ["internal/parser/errors.go"],
    "verify": [
      {
        "argv": ["go", "test", "./internal/parser"],
        "timeout_ms": 120000
      }
    ]
  },
  {
    "message": "docs(parser): explain source locations",
    "files": ["docs/parser-errors.md"]
  }
]
```

Keep every file in exactly one group. Use repository-relative paths. Do not
include an unclaimed path. Keep each verification timeout finite.

Pass a file or standard input:

```text
wip --json commit --plan plan.json
wip --json commit --plan - < plan.json
```

## Result checks

Parse the single JSON envelope. Require `ok: true`. In `data`, require
`ref_updated: true`, `gate_outcome: "passed"`, and
`intent_state: "complete"`. Check the returned commit sequence and lane ref
against the plan and the lane recorded before capture.

If the command fails before the compare-and-swap ref update, fix the reported
cause and run the full plan again. If it fails after the ref update, use only
the returned `plan_id` and `plan_digest` with `wip reconcile`.

Do not run two captures for the same lane at once. Separate lanes with
disjoint leases can capture in parallel.
