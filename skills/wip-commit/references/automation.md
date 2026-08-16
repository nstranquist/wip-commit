# Automated capture

Use JSON mode so the caller can check typed evidence. Do not accept a zero exit
status as the only proof of capture.

## Preflight and setup

Run `wip --json doctor` before setup. Stop if it reports an unsafe state record,
an incomplete recovery, or another coordination domain.

Inspect the staged path set with
`git diff --cached --no-renames --name-only -z`. Parse NUL-delimited paths so
tabs, spaces, and newlines in file names stay exact.

Use explicit setup values. Disable installation when a separate bootstrap step
owns installation:

```text
wip --json init \
  --mode shared \
  --lane <task-slug> \
  --agent <agent-id> \
  --session <session-id> \
  --path <owned-path> \
  --non-interactive \
  --no-install \
  --no-install-skill
```

If initialization fails after it writes an intent, parse `intent_id`,
`intent_state`, `completed_steps`, and `recovery`. Retry only the exact command
in `recovery`.

Run `wip --json plan` after staging. Treat its component groups and subject
prefixes as review hints. Check semantic intent and dependency closure before
you write the commit plan.

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
disjoint leases can capture in parallel. `wip commit` keeps the complete active
lane lease set captured at its start alive during hooks and verification. Use
`wip renew` only for editing time between captures.

## State maintenance

Run `wip --json doctor` before archival. Archive only a reviewed preview. Reuse
the exact `cutoff` and `plan_digest` with `--apply --yes`. If a failed apply
returns a prepared receipt, continue only with:

```text
wip --json archive --resume <archive-id> --apply --yes
```

Archive and restore preserve all lane refs and commits.
