# Commit plan examples

The files in this directory show the JSON shape for a commit plan. Pass an
edited file with `wip commit --plan <file>`. These files are templates, not
commands to run unchanged.

Before you use an example:

1. Replace every path with a staged path in the active lane's lease.
2. Replace every message with a concrete Conventional Commit subject.
3. Replace each verification command with a bounded command for the candidate
   tree.
4. Run `wip plan` and compare its path groups with the edited file.
5. Run `wip commit --plan <file>`.

The command rejects paths outside the lease, duplicate files, vague messages,
and verification commands that fail or time out. It leaves the source `HEAD`
and complete index unchanged.
