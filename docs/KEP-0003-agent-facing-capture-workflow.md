# KEP-0003: Agent-facing capture workflow

Status: Accepted and implemented

Decision owner: Repository owner

Last updated: 2026-08-18

Target release: `v0.1.0-beta.1`

## Summary

Keep the private-index capture transaction unchanged. Improve the commands and
receipts that agents use around that transaction.

This change adds one operational status object, one reviewed plan schema,
automatic lease renewal, and one typed handoff. It also adds `wip capture`.

The change does not add landing, staging, merging, pushing, or index cleanup.

## Decision

Implement these contracts:

1. `wip status` reports exact staged and lease classifications.
2. `wip plan` writes a bound draft that `wip capture` reads directly.
3. A lane profile binds a named commit-message policy.
4. Successful ordinary lane use renews the complete active lease set.
5. `wip handoff` returns a validated, capture-only receipt.
6. Interactive initialization recommends `worktree` mode.
7. `wip capture` requires a reviewed v2 plan and returns its handoff.

`wip commit` keeps legacy plan-array and explicit `--single` support. This
compatibility path does not weaken the reviewed `wip capture` contract.

## Operational status

The status object reports these disjoint or related sets:

- `capture_candidates`: leased index entries that differ from the lane tree.
- `captured_but_still_staged`: leased staged entries that match the lane tree.
- `foreign_staged`: source staged entries outside all active leases.
- `leased_but_not_staged`: active claim scopes with no staged or pending entry.

The object also reports lease time, renewal state, ref health, message policy,
and the latest capture-intent state.

`wip status` renews the lane by default. Use `wip status --observe` for a pure
read that does not renew a lease.

## Reviewed plan

`wip plan --out <file>` creates the file without replacement. The draft binds:

- lane ID, lane ref, and lane tip.
- source `HEAD` and selected staged paths.
- selected source-index digest.
- active lease-scope digest.
- named message-policy receipt.
- ordered groups and optional verification commands.

The draft contains empty `message` fields. A reviewer must add each message and
change `state` from `draft` to `reviewed`.

`wip capture` recalculates every binding before hooks run. It also passes the
reviewed index digest into the capture kernel. The kernel checks that digest
again when it takes its source snapshot. `STALE_PLAN` reports a moved input
before the transaction. `SOURCE_INDEX_MOVED` reports an index race at the
kernel boundary.

The capture intent stores the reviewed-plan digest and policy receipt. The
intent digest also covers those fields.

## Message policy

The capture kernel validates message safety only. It rejects empty subjects,
invalid UTF-8, NUL, carriage returns, unsupported controls, and oversized text.

The default `conventional-v1` policy keeps the existing naming rules. The
optional `safety-only-v1` policy permits any message that passes kernel safety.

A repository can select the policy in `.wip-commit.json`:

```json
{
  "schema_version": "1.0.0",
  "message_policy": "safety-only-v1"
}
```

`wip init --message-policy <id>` is an explicit override. Initialization binds
the resolved policy to the lane profile and initialization intent.

All policy checks finish before pre-commit hooks and verification commands.

## Lease renewal

Authenticated `status`, `env`, `claim`, `plan`, `commit`, `capture`, and
`handoff` use the complete active lane lease set. They renew that set after
identity validation.

`status --observe`, `env --observe`, `doctor`, and archive preview do not renew
leases. A handoff for a released lane also remains a pure read. Expired active
leases remain fail-closed and require a new claim.

The released-lane read acquires the existing regular lane lock without
creating a directory or rewriting lock metadata. A missing or replaced lock
fails closed.

The equivalent `--observe=true` and `-observe` forms also select pure reads.
An explicit `--observe=false` keeps normal renewal. `env` checks the lane's
canonical worktree before renewal and returns `WORKTREE_MISMATCH` from another
checkout without changing a lease.

Every successful lane JSON envelope contains a `context` object. The context
includes the earliest expiry, remaining milliseconds, and renewal result.

## Handoff

The handoff validates the durable intent and every Git object in its chain. It
contains the lane ref, tip, parent, base, and final tree. It also contains the
ordered messages and path sets.

Intent selection, receipt validation, active-lease renewal, and status
construction use one lane lock. Completed intents do not bypass Git-object
validation.

The handoff omits agent IDs, session IDs, absolute paths, remote URLs, and file
contents. It sets `capture_only` to `true` and `landing_authorized` to `false`.

The handoff is evidence for a separate policy system. It cannot authorize a
merge, push, or target-ref update.

## Guided capture

Use this workflow:

```text
wip --json status
wip --json plan --out plan.json
# Review groups. Add messages. Set state to reviewed.
wip --json capture --plan plan.json
```

Use `--final` to release the lane after handoff validation. The command keeps
the lane lock through validation and release. It still does not land or delete
a ref.

## Compatibility

The state directory remains `<git-common-dir>/wip/v1/`. Lane, lease, and archive
records stay at schema version 1.

New profiles and initialization intents use schema version 2. Readers accept
version 1 and apply the historical `conventional-v1` policy in memory.

New reviewed captures use intent schema version 2. Legacy `wip commit` captures
can keep intent schema version 1.

Readers calculate a version 1 digest before they infer any new default. This
rule preserves immutable historical receipts.

## Security review basis

The design uses negative tests for stale and malformed inputs. It keeps stable
compatibility paths and records all security-sensitive bindings.

These choices follow the reviewed OpenSSF secure-development guidance and NIST
SSDF practices. The Git environment review also keeps repository selection and
index behavior explicit.

See [SECURITY-REVIEW-2026-08-18.md](SECURITY-REVIEW-2026-08-18.md).

## Rejected alternatives

### Add landing to capture

Rejected. Landing owns a different ref, lock, policy, and recovery transaction.

### Clear captured staged entries

Rejected. An uncoordinated Git process can update the shared index concurrently.

### Replace `git add`

Rejected. This feature does not own source worktree staging.

### Renew only with `wip renew`

Rejected. Ordinary authenticated use already proves current lane ownership.

### Make legacy arrays valid for `wip capture`

Rejected. A legacy array does not bind source, lease, lane, or policy state.

## Acceptance evidence

- Focused CLI tests cover reviewed success, draft refusal, and stale refusal.
- Handoff tests verify chain fields, privacy fields, and capture-only markers.
- Status tests cover pending, captured, foreign, empty-lease, and observe states.
- Policy tests cover default, override, strict configuration, and safety-only use.
- Compatibility tests preserve version 1 profile and intent digests.
- Deterministic contention tests prove that final release and active handoff
  keep the lane lock after receipt validation.
- Released-handoff tests prove that lock acquisition preserves the existing
  lock bytes and does not create a missing lock or parent directory.
- Real command fixtures validate against every published capture schema.
- Worktree tests prove that `env` fails before renewal in another checkout.
- Full race, vet, workflow, and dogfood evidence belongs in `docs/DOGFOOD.md`.

## Decision log

| Date | Decision |
| --- | --- |
| 2026-08-18 | Preserve the private-index and one-CAS capture kernel. |
| 2026-08-18 | Make the reviewed v2 plan mandatory for `wip capture`. |
| 2026-08-18 | Keep landing outside the capture product. |
