# Support

## Current support level

`wip-commit` is a local beta candidate. It has not been published. Support is
best effort, and there is no response-time or resolution-time guarantee.

After publication, the newest beta is the only supported beta unless a release
note says otherwise. A published stable version will define a separate support
policy.

## Ask for help

After the repository is public, use its issue tracker for reproducible bugs and
its discussion area for usage questions, if discussions are enabled. Do not put
secrets, private paths, repository names, file contents, commit messages, or
credentials in a report.

For a security issue, follow [SECURITY.md](SECURITY.md). Do not report a
suspected vulnerability in a public issue.

A confidential conduct-reporting channel is not configured in this local
checkout. The public-beta plan requires the owner to configure that channel and
approve a Code of Conduct before the project solicits public contributions.

## Report a capture problem

If a command might have captured foreign content or changed the wrong ref:

1. Stop further capture and landing work.
2. Preserve the worktree, index, agent ref, JSON result, and recovery receipt.
3. Run `wip doctor` and record only redacted output.
4. Do not stash, reset, clean, force-update, or delete state.
5. Report the `wip` version, operating system, Git version, command outcome,
   typed error code, `ref_updated`, and `intent_state`.

The project does not promise recovery from manual changes to its state files,
uncoordinated Git index mutation, malicious same-user processes, or commands
outside the trust boundary in [THREAT-MODEL.md](THREAT-MODEL.md).
