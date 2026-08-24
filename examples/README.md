# Legacy commit plan example

`shared-split-plan.json` shows the legacy commit-only plan array. Pass an edited
file only to `wip commit --plan <file>`. `wip capture` rejects this format.

For guided capture, generate a bound version 3 plan with `wip plan --out
<file>`. Do not construct that plan from a template. Its source, lane, lease,
and policy digests must come from the current repository state.

Before you use an example:

1. Use this legacy format only for a controlled staged-index compatibility
   capture.
2. Replace every path with a staged path in the active lane's lease.
3. Replace every message with a concrete Conventional Commit subject.
4. Replace each verification command with a bounded command for the candidate
   tree.
5. Run `wip plan --source staged_index --untracked not_applicable`.
6. Compare its source paths with the edited legacy file.
7. Run `wip commit --plan <file>`.

The command rejects paths outside the lease, duplicate files, vague messages,
and verification commands that fail or time out. It leaves the source `HEAD`
and complete index unchanged.
