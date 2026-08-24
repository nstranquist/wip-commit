# Agent-facing workflow security review

This review is historical evidence for plan version 2. It does not review
private-source plan version 3, cumulative handoff version 2, or coordination
migration. Use the current threat model and a new dated review for those
contracts.

Review date: 2026-08-18

Scope: KEP-0003, capture plan v2, policy binding, lease renewal, status, and
handoff output.

## Sources

The review used these primary sources:

- OpenSSF Concise Guide for Developing More Secure Software, revision
  `429544ce24fa3351c193190ef563377f61ce8aa3`.
- NIST SP 800-218 version 1.1, February 2022.
- Pro Git, second edition, Environment Variables chapter.
- Local Git `2.55.0` behavior tests.

The source registry is [OSS-SOURCES.json](OSS-SOURCES.json).

## Applied findings

### Negative tests

The reviewed workflow rejects draft plans, moved lane tips, moved source
indexes, changed candidate paths, changed leases, and changed policies.

The tests also reject malformed policy receipts, unknown configuration fields,
unsafe message bytes, and handoffs without a complete validated intent.

### Stable compatibility

Version 1 records remain readable. The readers reconstruct each old digest
before they infer a historical default.

The command keeps legacy plan arrays under `wip commit`. It does not accept
those arrays under the stronger `wip capture` contract.

### Review evidence

The plan records each security-sensitive input before capture. The v2 intent
stores the reviewed-plan digest and message-policy receipt.

The command checks the selected index digest before it starts the transaction.
The kernel receives the same expected digest and checks its initial source
snapshot before hooks run. It checks the selected entries again before the
single ref update.

The handoff validates the ref and complete commit chain before output. It marks
the result as capture-only and non-authorizing.

### Git environment boundary

Repository discovery still removes inherited variables that can redirect the
repository, refs, config, worktree, namespace, or object store.

The caller's `GIT_INDEX_FILE` remains the explicit source index. The private
capture index remains a separate `GIT_INDEX_FILE` value.

## Residual boundaries

- Cooperating processes must honor leases.
- Hooks and verification commands remain trusted repository code.
- A same-user malicious process can alter local files and refs.
- The handoff does not prove review approval, landing, push, release, or adoption.
- Network filesystems remain unsupported.

These boundaries do not change the capture-only beta contract.
