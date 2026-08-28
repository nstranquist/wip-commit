# wip-commit

`wip` lets concurrent agents capture exact paths from a staged index or a
worktree snapshot. It does not move the source checkout's `HEAD` or rewrite the
caller index. Each agent writes to a local ref under
`refs/heads/wip/<agent>/<lane>`.

The source repository is public. No tag or GitHub release exists. The planned
first prerelease is `v0.2.0-beta.1`. The current candidate is suitable for
local beta use, but it is not a stable release. Verify the public tag and
release before you treat a checkout as released software.

## Why it exists

A normal `git commit` consumes the complete shared index and moves the checked
out branch. When several agents use one checkout, that operation is unsafe.
`wip` uses four controls:

- Path leases prevent active lanes from claiming overlapping paths.
- A private source snapshot contains only the reviewed source paths.
- A private index builds only the planned commits.
- One compare-and-swap updates the agent ref after all commit groups pass.

Two lanes can therefore capture disjoint source paths in parallel. They do not
commit directly to the checked-out branch. Review and landing are separate
operations.

One repository can have only one coordination domain. Standalone state uses
`<git-common-dir>/wip/v1`. Legacy Nicos Tools state uses
`<git-common-dir>/ndev-wip/v1`. Both commands use one shared domain lock and
stop with `COORDINATION_DOMAIN_CONFLICT` if the other domain exists.

## Requirements

- Git 2.36 or newer.
- Go 1.25.12 or newer to build from source.
- A local filesystem with working advisory file locks and same-filesystem hard
  links.

## Build locally

Build the current checkout with:

```text
go build -trimpath -o ./bin/wip ./cmd/wip
./bin/wip version
./bin/wip --json version
./bin/wip --json contract
```

Source builds identify themselves as `0.2.0-dev` with `channel: development`.
The release builder injects both the selected SemVer and its exact tag. Only
that pair reports `channel: release`. Release binaries also report the exact
VCS revision and a clean source state. A local Go build reports VCS fields when
the toolchain supplies them. If `source_stamped` is false, do not let the
binary read or change existing coordination state.

The version result also reports the executable SHA-256 and the embedded
portable-skill payload SHA-256. An integration must hash the selected regular
executable independently, compare both digests with the contract, and reject a
file identity or digest change before or after each subprocess.

`wip --json contract` reports the command protocol, explicit capabilities,
portable-skill contract, and embedded portable payload digest. Integrations
must validate those fields. Do not infer compatibility from a display version.
The current development protocol is `wip.command/3.0.0`.

For a published `v0.2.0-beta.1` tag, the install command is:

```text
go install github.com/nstranquist/wip-commit/cmd/wip@v0.2.0-beta.1
```

Before you run that command, verify that the tag exists at that public address.

## Quick start

Build the current checkout, then start the setup wizard inside the Git
repository where you want to capture work:

```text
go build -trimpath -o ./bin/wip ./cmd/wip
./bin/wip init
```

The wizard prints the lane environment command. Load it in the agent shell.
Then review an exact worktree-source plan and capture it:

```text
eval "$(./bin/wip env --lane <lane-id>)"
./bin/wip plan \
  --source worktree_snapshot \
  --untracked forbid \
  --path <owned-path> \
  --out plan.json
# Add one message to each group. Then set state to reviewed.
./bin/wip capture --plan plan.json
./bin/wip release
```

Use a linked worktree when one agent owns the complete checkout. Use
shared mode when cooperating agents need disjoint path leases in one checkout.
Automation must capture the exact generated plan. Do not construct a version 3
plan from a partial example because its source and lease digests are immutable.

## Use the agent skill

The portable skill is in [skills/wip-commit](skills/wip-commit). Install that
complete directory through the skill mechanism for your agent runtime. Keep
its `references` and `agents` directories with `SKILL.md`.

Invoke `$wip-commit` for agent work. The skill uses only the public `wip`
command and standard Git inspection. It defaults automation to reviewed split
plans and treats typed JSON evidence as the capture result.

An internal integration can add repository policy around this skill. It must
not weaken the path-ownership, split-plan, result-check, or no-landing rules.

## Upgrade an existing installation

Upgrade one target at a time. Preview is read-only. Apply requires the exact
digest from that preview:

```text
wip --json upgrade --binary --install-dir ~/.local/bin
wip --json upgrade --binary --install-dir ~/.local/bin \
  --apply --yes --plan-digest sha256:<reviewed-digest>

wip --json upgrade --skill --skill-dir ~/.agents/skills
wip --json upgrade --skill --skill-dir ~/.agents/skills \
  --apply --yes --plan-digest sha256:<reviewed-digest>
```

The binary target must already be a regular executable. The skill target can
be an older canonical bundle, but it cannot contain unknown files, directories,
or symbolic links. Use `wip init --install` or `--install-skill` for a missing
target. Before a binary changes, WIP durably retains the complete previous and
desired executables under the content-addressed `.wip.revisions` directory next
to the install target. WIP does not prune that rollback store. The reviewed
plan reports both revision paths. See
[State compatibility](docs/STATE-COMPATIBILITY.md) for the separate
coordination-marker upgrade.

## Prove portable behavior

Run the behavioral benchmark from the repository root:

```text
go run ./skills/wip-commit/benchmark/regression.go
```

The runner builds the candidate with `GOPROXY=off`. It then uses the shipped
entrypoints in disposable Git repositories. It makes no network or AI calls.

The benchmark checks eight semantic cases and eight negative controls. The
reviewed baseline requires all 16 checks to pass.

Run the deliberately weakened candidate proof:

```text
go run ./skills/wip-commit/benchmark/regression.go --prove-weakened
```

The proof requires the benchmark to reject the unsafe candidate. The runner
cannot modify [the reviewed baseline](skills/wip-commit/benchmark/baseline.json).

If `ndev` is available, run `ndev --json skills benchmark wip-commit`. It finds
this external repository before it uses an internal skill fallback.

The cross-platform benchmark CI job runs the same cases on Linux, macOS, and
Windows. Read [the benchmark guide](skills/wip-commit/benchmark/README.md) for
the report schema, artifact location, and baseline policy.

## Start with `wip init`

Run the wizard in a Git checkout:

```text
wip init
```

The wizard does the following work:

1. It checks the coordination domain and recommends `worktree` mode.
2. It asks for the agent, session, lane, base ref, and path claims.
3. It can create a detached linked worktree after you select that mode.
4. It can copy the current binary to `~/.local/bin` after you approve the path.
5. It can install the embedded portable skill in `~/.agents/skills`.
6. It binds the lane to a named message policy.
7. It creates the lane, claims the paths, writes a private profile, and checks
   the staged diff.

Use `worktree` mode for one agent per task. Use `shared` mode only when lanes
must use the same checkout and index.

The staged-path read must succeed when the worktree exists. Git inspection
errors stop setup and preserve its recovery receipt. Staged whitespace errors
remain nonfatal, but the human and JSON results report them. A future worktree
dry-run reports that its staged check did not run.

The installers publish a complete file without overwriting an existing path.
They never overwrite different content or follow a target symlink. A retry can
finish a matching partial skill bundle after an interruption. The wizard does
not change global Git configuration or install hooks. Existing repository hooks
run during a non-dry capture.

Copied pre-commit and post-commit hooks receive
`NICOS_PREFLIGHT_READ_ONLY=1` with the private candidate index. Compatible
hooks must inspect only that candidate and must not apply repairs to the source
checkout. This value is a cooperative hook contract, not a sandbox. Repository
hooks remain trusted code and can still change files or use the network.

The wizard first validates and bootstraps the coordination domain. It then
writes a durable initialization intent before it changes a worktree, lane,
lease, profile, binary, or skill. Each completed step is idempotent. If setup
stops, the error includes the intent path, completed steps, and an exact resume
command. The command pins the absolute repository and worktree paths and the
resolved base commit ID.

An exact retry repeats the full canonical path claim. This action repairs an
interruption between lease publication and the lane back-reference.

`wip init` is the only command that can claim an uninitialized repository.
All other commands inspect the state first. When no state exists, they do not
create the public marker, state tree, or coordination lock.

Use non-interactive setup for an agent:

```text
wip --json --repo-dir /repo init \
  --mode worktree \
  --create-worktree /worktrees/parser-errors \
  --lane parser-errors \
  --agent codex \
  --session session-42 \
  --path internal/parser \
  --path docs/parser.md \
  --non-interactive \
  --no-install \
  --install-skill
```

Create and initialize a linked worktree from the anchor checkout:

```text
wip --repo-dir /repo init \
  --mode worktree \
  --create-worktree /worktrees/parser-errors \
  --lane parser-errors \
  --path internal/parser
```

The command uses `git worktree add --detach`. It refuses to replace an existing
directory. If an existing directory contains a matching linked worktree at the
requested base, the command reuses it.

The success output reports the linked worktree. Continue the workflow in that
checkout. `wip env` and all other lane commands reject a different checkout
with `WORKTREE_MISMATCH` before they renew a lease.

## Configuration

`wip` has no repository configuration file and no background service. The
`wip init` command records the selected lane, worktree, owner, and path leases
below the repository's common Git directory. Linked worktrees therefore share
one coordination domain.

Use `--repo-dir` to select a repository without changing directories. Use
`--json` when a program needs typed results. `wip env` exports `WIP_LANE`,
`WIP_AGENT`, and `WIP_SESSION` for later commands. An explicit
`GIT_INDEX_FILE` selects the caller index for `staged_index` source mode. Other
inherited repository-routing variables do not override the selected repository.

Commit plans hold per-group verification commands and optional timeouts. The
default verification and hook timeout is two minutes, and the maximum is 24
hours. Keep secrets and machine-specific paths out of plan files and commit
messages.

## Usage

### Capture work

Load the lane identity in each agent shell:

```text
eval "$(wip env --lane parser-errors)"
```

Run this command from the checkout recorded for the lane. A worktree-bound
lane returns `WORKTREE_MISMATCH` from another checkout and does not renew its
lease.

In a dedicated worktree, select `worktree_snapshot`. This source includes exact
tracked changes in the selected scopes. It does not stage those changes:

```text
wip --json status
wip --json plan \
  --source worktree_snapshot \
  --untracked forbid \
  --path internal/parser \
  --path docs/parser.md \
  --out plan.json
```

The `forbid` policy stops when an untracked path exists in a selected scope.
Use `--untracked include` only after you review every selected untracked path.

Use `staged_index` for a controlled shared-checkout exception. Stage only the
owned paths. Other staged paths can remain in the caller index:

```text
git add -- internal/parser docs/parser.md
wip --json plan \
  --source staged_index \
  --untracked not_applicable \
  --path internal/parser \
  --path docs/parser.md \
  --out plan.json
```

`wip status` is the operational read for an agent. It reports:

- leased entries that are capture candidates.
- captured entries that remain staged.
- foreign staged entries outside the lane.
- active claims with no staged or pending entry.
- lease time, ref health, policy, and latest intent state.

Status describes the caller index. It does not select or authorize a
`worktree_snapshot` source.

The command renews active leases after it validates the lane identity. Use
`wip status --observe` when a pure read is required.

`wip plan` groups the selected source paths at component boundaries. It writes
one draft with all capture bindings and empty message fields. The command
creates `plan.json` without replacing an existing file.

The planner compares the selected source with the lane's current commit. It
omits paths that the lane already captured.

Use `wip plan --observe` to preview the draft without lease renewal or an
output file. The command rejects `--observe` with `--out`. It does not change
the Git common directory, the checkout, or the caller index.

Review every group for intent and dependency closure. Add one message to each
group. Then change the top-level `state` from `draft` to `reviewed`.

```json
{
  "schema_version": "3.0.0",
  "state": "reviewed",
  "lane": "parser-errors",
  "agent": "codex",
  "session": "parser-errors-review",
  "target_ref": "refs/heads/wip/codex/parser-errors",
  "lane_tip": "<exact object ID>",
  "source_head": "<exact object ID>",
  "source_mode": "worktree_snapshot",
  "untracked_policy": "forbid",
  "source_digest": "sha256:<exact digest>",
  "source_scopes": ["docs/parser.md", "internal/parser"],
  "lease_digest": "sha256:<exact digest>",
  "message_policy": {
    "id": "conventional-v1",
    "version": "1.0.0",
    "digest": "sha256:<exact digest>"
  },
  "source_paths": ["docs/parser.md", "internal/parser/errors.go"],
  "groups": [
    {
      "key": "internal/parser",
      "suggested_scope": "parser",
      "suggested_prefix": "<type>(parser): ",
      "rationale": "nearest component boundary; review intent and dependency closure",
      "message": "fix(parser): preserve source error locations",
      "files": ["internal/parser/errors.go"],
      "verify": [
        {"argv": ["go", "test", "./internal/parser"], "timeout_ms": 120000}
      ]
    },
    {
      "key": "docs/parser",
      "suggested_scope": "parser",
      "suggested_prefix": "<type>(parser): ",
      "rationale": "nearest component boundary; review intent and dependency closure",
      "message": "docs(parser): explain source error locations",
      "files": ["docs/parser.md"]
    }
  ],
  "instruction": "Review every path group, add a non-empty message to each group, then change state from draft to reviewed. Capture rejects stale bindings and draft plans."
}
```

```text
wip --json capture --plan plan.json
```

`wip capture` recalculates the lane, source, path, lease, and policy bindings
before hooks run. It rejects a draft or stale plan.

The result contains the capture receipt and a typed handoff. The handoff is
capture-only evidence. It does not authorize landing, merging, or pushing.

Use `wip capture --final --plan plan.json` to release the lane after handoff
validation. The command holds the lane lock from validation through release.
The lane ref and commits remain.

The version 3 plan schema is [docs/PLAN.schema.json](docs/PLAN.schema.json).
The operational status and version 2 cumulative handoff schemas are
[docs/STATUS.schema.json](docs/STATUS.schema.json) and
[docs/HANDOFF.schema.json](docs/HANDOFF.schema.json).
The test suite validates real command data against all four schemas and checks
that strict schemas reject invalid data.

`wip commit` remains a compatibility and interactive command. It accepts the
reviewed version 3 plan or the legacy plan array. `wip capture` accepts only
version 3.

`wip commit --dry-run` is a no-exec preview. It snapshots repository hook bytes
and validates the source and candidate tree structure. It does not execute
repository hooks or verification commands. It does not renew leases, create
commits, write an intent, or move a ref.

The dry-run result uses schema `3.0.0`. It reports `gate_outcome: "skipped"`
and an `execution` object. Each execution count reports planned, executed, and
skipped operations. Every executed count is zero. A successful dry-run is not
proof that the configured gates pass.

When one commit is intentional, use this explicit command:

```text
wip commit --single \
  --message "fix(parser): preserve source error locations" \
  --path internal/parser
```

The default `conventional-v1` policy rejects vague subjects, unsupported types,
duplicate messages, long subjects, and unauthorized `wip:` prefixes.

Select `safety-only-v1` in `.wip-commit.json` when repository policy permits
free-form messages:

```json
{
  "schema_version": "1.0.0",
  "message_policy": "safety-only-v1"
}
```

The capture kernel still rejects unsafe bytes, empty subjects, and oversized
messages. Policy checks finish before hooks and verification commands run.

## Continue and finish

Leases last 15 minutes without authenticated lane use. `status`, `env`,
`claim`, `plan`, `commit`, `capture`, and `handoff` renew the complete active
set. A handoff for a released lane stays read-only.
It locks the existing lane lock file without creating or rewriting lock
metadata.

For a pure read, use `--observe`, `--observe=true`, or `-observe` with `status`,
`env`, and `plan`. `--observe=false` keeps normal renewal behavior. An observed
plan cannot write an output file.

Manual renewal remains available:

```text
wip renew
```

A non-dry `wip commit` snapshots and renews the complete active lease set before
capture. It renews that set in the background. The final publication check
renews and compares the set again. `wip commit --dry-run` does not renew leases.

Add another claim with:

```text
wip claim --path internal/scanner
```

Inspect the lane with:

```text
wip status
```

Use `wip handoff` to validate and print the complete base-to-tip capture chain:

```text
wip --json handoff --lane parser-errors
```

The version 2 handoff contains every continuous completed capture segment from
the lane base to its current tip. It binds the exact result with
`handoff_digest`. It sets `landing_authorized` to `false`.

The handoff omits agent IDs, session IDs, absolute worktree paths, remote URLs,
and file contents. Intent-chain validation, active-lease renewal, and status
construction use one lane lock. A supplied `--plan-id` and `--plan-digest` must
name the tip segment. They cannot select an older single segment.

When the task is complete, release the coordination state:

```text
wip release
```

Release and abort both preserve the local agent ref. Neither command deletes
commits, lands a branch, pushes a remote, or merges work.

## Migrate one coordination domain

Use the explicit migration only during a repository writer freeze. The preview
is byte-for-byte read-only:

```text
wip --json migrate --preview
```

Save the exact `data.plan` object as `migration-plan.json`. Review its producer,
source manifests, destination manifests, ref expectations, dispositions,
conflicts, and `plan_digest`.

Apply only the same plan with the same source-matched executable:

```text
wip --json migrate \
  --apply \
  --plan migration-plan.json \
  --plan-digest sha256:<reviewed-digest> \
  --yes
```

The migration imports valid capture records into `<git-common-dir>/wip/v1`.
It copies review records into `<git-common-dir>/wip/review/v1`. The review store
includes immutable `handoffs/` and `plans/` directories. It moves the legacy
store into a sealed, read-only archive after the new records are durable.

An active legacy lane is stale when it has no leases, or when all its exact
active leases have expired. The migration gives the lane and its leases an
`archive-only` disposition. It preserves the lane ref and binds its exact SHA
in the plan. A ref that advanced only from the stored cursor is also preserved
and bound. A missing ref, a diverged ref, or a moved ref with a live lease
blocks the migration.

Use `--resume` with the same plan after an interruption. Use `--rollback` only
before the legacy source moves. The command never dual-writes both domains and
never grants landing authority.

## Inspect and archive state

Run a read-only state audit:

```text
wip doctor
```

The command checks bounded record sets, lane refs, leases, profiles, capture
intents, and initialization intents. It does not create a state directory. It
returns exit code 1 when a recovery or error finding needs operator action.

Preview old released records:

```text
wip archive --older-than 720h
```

The preview returns an exact cutoff and plan digest. Reuse both values. If the
preview used one or more `--lane` filters, repeat the same filters on apply:

```text
wip archive \
  --lane '<same-preview-lane>' \
  --cutoff '<exact-preview-cutoff>' \
  --plan-digest '<exact-preview-sha256>' \
  --apply --yes
```

JSON previews also return `lane_filters` as the canonical sorted list to
replay. Omitting a preview filter changes the eligible set and fails closed
with `ARCHIVE_PLAN_MOVED`.

The store compares every reviewed candidate again while it holds the archive,
lane, and lease-registry locks. It then moves records into a recoverable archive
batch. The receipt binds each lane record and released lease to its candidate.
Unexpected or cross-lane records stop the operation. Archival never deletes or
moves lane refs or commits.

If an apply stops after it returns a prepared receipt, resume only that receipt:

```text
wip archive --resume <archive-id> --apply --yes
```

If the process stops before it writes the receipt, rerun the same reviewed
apply. The deterministic empty batch is safe to reuse. A batch with an
unexpected record stops with `ARCHIVE_CONFLICT`.

Restore one batch with:

```text
wip archive --restore <archive-id> --apply --yes
```

Restore writes a durable `restoring` state before it moves the first record. If
restore stops, run the same restore command again. `wip doctor` reports the
receipt until recovery completes.

A `restored` receipt remains as history. If you archive the same records again,
`wip doctor` requires each non-live record to have exactly one validated newer
archive owner. A missing, duplicated, or unbound record fails closed. Restore
the current complete receipt to make rearchived records live. Repeating an
older restored receipt only confirms that its earlier restore completed.

## Important index behavior

A successful capture leaves the source `HEAD`, worktree, and complete caller
index unchanged. A `staged_index` capture therefore leaves its entries staged.
A `worktree_snapshot` capture does not stage or unstage any path.

`wip` ignores inherited repository-local Git variables that can redirect or
reinterpret the repository, common directory, worktree, namespace, refs,
shallow boundary, configuration, or object storage. The canonical repository
selected by `--repo-dir` or the current directory wins. It honors an inherited
`GIT_INDEX_FILE` as the caller index for `staged_index` source mode. Capture
hooks receive the private candidate index instead.

You can modify a later version of the same leased path. The next capture
compares the selected source with the current lane commit. Do not run a broad
reset, stash, or clean command to hide source state.

## Recover an interrupted capture

When the agent ref moved but metadata did not finish, the JSON error result
includes `plan_id` and `plan_digest`. Use both immutable values:

```text
wip reconcile \
  --plan-id plan-0123456789abcdef01234567 \
  --plan-digest sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef
```

Reconciliation compares the target ref, every parent, tree, message, changed path,
allowed scope, and final tree before it updates lane metadata. Exact retries are
idempotent.

## Troubleshooting

Start with structured state evidence:

```text
wip --json doctor
wip --json status
```

Do not stash, reset, clean, delete state, or force-update a lane ref during
recovery. Common actions are:

- `LANE_NOT_ACTIVE`: run `wip init`, or load the exact lane identity.
- `PATH_LEASE_CONFLICT`: select disjoint paths or wait for the owner to release
  its lease.
- `SOURCE_MOVED`: inspect the selected source paths and create a new reviewed
  plan.
- `LOCK_TIMEOUT`: the default zero wait fails fast. Inspect active lanes and
  retry later, or set a bounded `--lock-wait` outside an agent turn.
- `ref_updated: true`: preserve `plan_id` and `plan_digest`, then run the exact
  `wip reconcile` command.

[The error and recovery guide](docs/ERRORS.md) lists every typed code and its
required action. [SUPPORT.md](SUPPORT.md) explains what evidence is safe to
include in a report.

## Safety scope

`wip` protects coordination between cooperating local processes. It does not
protect against a malicious process running as the same operating-system user.
Hooks and commands in `verify` are trusted repository code. They can change
files or use the network. Read [THREAT-MODEL.md](THREAT-MODEL.md) before broad
adoption.

The `wip` binary does not send telemetry or contact a network service. Use
`--json` for local structured evidence. A repository hook or an explicit
command in `verify` can still use the network.

A `verify` command does not inherit the active WIP lane identity or source
`GIT_INDEX_FILE`. It receives only `WIP_CANDIDATE_TREE` from `wip`. This rule
prevents a nested command from selecting the host capture lane by accident.

See [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) for the transaction model,
[docs/ERRORS.md](docs/ERRORS.md) for recovery actions, and
[docs/STATE-COMPATIBILITY.md](docs/STATE-COMPATIBILITY.md) for upgrade and
downgrade rules. See [docs/RELEASE.md](docs/RELEASE.md) for deterministic beta
archives and the human-gated tag procedure. The publication summary is in
[docs/OSS-READINESS.md](docs/OSS-READINESS.md). The detailed proposal and
evidence tracker are in
[docs/OSS-PUBLIC-BETA.md](docs/OSS-PUBLIC-BETA.md).

The beta remains capture-only. [KEP-0001](docs/KEP-0001-capture-to-landing-boundary.md)
defines the adapter boundary for a separate landing authority. It does not
authorize a standalone landing implementation. Non-authors can use the redacted
[beta exercise](docs/BETA-EXERCISE.md) to produce independent evidence.
Repository owners can use the human-gated
[publication handoff](docs/PUBLICATION-HANDOFF.md) and
[hosted setup runbook](docs/HOSTED-SETUP.md). Use the
[correction receipt](docs/PUBLICATION-CORRECTION.md) after a hosted candidate
changes. These procedures do not authorize a push or release tag.

Project authority and succession are in [GOVERNANCE.md](GOVERNANCE.md).
Current support and safe incident-reporting guidance are in
[SUPPORT.md](SUPPORT.md). Security reports use [SECURITY.md](SECURITY.md).
Community participation follows [CODE_OF_CONDUCT.md](CODE_OF_CONDUCT.md).
The [open source practice guide](docs/OSS-PRACTICE-GUIDE.md) maps official
guidance to project rules and records when maintainers must review it again.
Local self-hosting receipts and safe-failure results are in
[docs/DOGFOOD.md](docs/DOGFOOD.md).
The current public-source, hosted-control, and release boundary is in
[docs/HOSTED-STATUS-2026-08-18.md](docs/HOSTED-STATUS-2026-08-18.md).

## License

MIT
