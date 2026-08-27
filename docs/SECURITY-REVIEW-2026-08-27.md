# Capture boundary security review

Review date: 2026-08-27

Scope: linked-worktree initialization recovery, copied-hook environment,
candidate-tree verifier materialization, and inherited Git pathspec variables.

## Sources

This review refreshed three primary sources in
[OSS-SOURCES.json](OSS-SOURCES.json):

- Pro Git, second edition, Environment Variables chapter, reviewed with Git
  2.55.0.
- OpenSSF Concise Guide for Developing More Secure Software, revision
  `eb54fd9f4264bebab64586d92dffc914fbe8c896`.
- NIST SP 800-218 version 1.1, February 2022.

Pro Git identifies repository, worktree, index, object-directory, and pathspec
environment variables as inputs to Git behavior. The capture kernel now removes
the inherited pathspec mode variables in addition to its existing repository
routing controls.

OpenSSF requires automated negative tests for security requirements. NIST SSDF
requires root-cause correction and tests that prevent recurrence. Each repaired
boundary has a negative or failure-boundary test.

## Applied findings

### Initialization recovery

When `wip init --create-worktree` starts inside that existing linked worktree,
the command finds the primary checkout that owns the shared Git directory. It
uses and reports that checkout as the immutable recovery anchor. A test reruns
the same completed initialization from the linked worktree and proves that the
command does not reject its own target.

### Copied hooks

The hook runner removes inherited copies of `WIP_CAPTURE`,
`WIP_HOOK_PHASE`, `WIP_SOURCE_READ_ONLY`, `WIP_TARGET_REF`, and
`WIP_COMMIT_OBJECT`. It then supplies one canonical value for the active hook
phase. A negative test supplies mixed-case spoofed values and proves that no
spoofed or duplicate value reaches the hook environment.

### Verifier materialization

The plan schema adds an optional `materialize` list. A command directory is an
implicit scope. The kernel lists the immutable candidate tree with literal
top-level pathspecs, rejects more than 16,384 entries or 512 MiB, and only then
creates a scratch checkout. It sends selected paths to `checkout-index` through
NUL-delimited standard input.

Tests prove that a scoped verifier sees its candidate directory and one
declared dependency but cannot see an unrelated candidate file. Parser tests
prove that entry, byte, and malformed-listing boundaries fail closed. A retained
real-tree overlay test materializes and verifies the NDev subtree with more than
10,000 files, then proves that an undeclared full nicos-tools tree returns
`VERIFY_MATERIALIZATION_LIMIT`.

## Residual boundaries

- Hooks and verify commands are trusted repository code. The read-only marker
  is a contract for cooperating hooks, not an operating-system sandbox.
- The materialization byte limit counts Git object content. Filesystem metadata
  and allocation can use additional space.
- External Git commands can ignore WIP leases and environment contracts.
- The capture handoff does not authorize landing, merge, push, tag, or release.
