# wip-commit

`wip` lets concurrent agents capture exact staged subsets without moving the
source checkout's `HEAD` or rewriting its Git index. Each agent writes to a
local ref under `refs/heads/wip/<agent>/<lane>`.

The planned first prerelease is `v0.1.0-beta.1`. The current candidate is
suitable for local beta use, but it is not a stable release. Verify the public
tag and release before you treat a checkout as published software.

## Why it exists

A normal `git commit` consumes the complete shared index and moves the checked
out branch. When several agents use one checkout, that operation is unsafe.
`wip` uses three controls:

- Path leases prevent active lanes from claiming overlapping paths.
- A private index contains only the planned staged entries.
- One compare-and-swap updates the agent ref after all commit groups pass.

Two lanes can therefore capture disjoint staged paths in parallel. They do not
commit directly to the same checked-out branch. Review and landing are separate
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
```

For a published `v0.1.0-beta.1` tag, the install command is:

```text
go install github.com/nstranquist/wip-commit/cmd/wip@v0.1.0-beta.1
```

Before you run that command, verify that the tag exists at that public address.

## Use the agent skill

The portable skill is in [skills/wip-commit](skills/wip-commit). Install that
complete directory through the skill mechanism for your agent runtime. Keep
its `references` and `agents` directories with `SKILL.md`.

Invoke `$wip-commit` for agent work. The skill uses only the public `wip`
command and standard Git inspection. It defaults automation to reviewed split
plans and treats typed JSON evidence as the capture result.

An internal integration can add repository policy around this skill. It must
not weaken the path-ownership, split-plan, result-check, or no-landing rules.

## Start with `wip init`

Run the wizard in a Git checkout:

```text
wip init
```

The wizard does the following work:

1. It checks the coordination domain and recommends `shared` or `worktree` mode.
2. It asks for the agent, session, lane, base ref, and path claims.
3. It can create a detached linked worktree after you select that mode.
4. It can copy the current binary to `~/.local/bin` after you approve the path.
5. It can install the embedded portable skill in `~/.agents/skills`.
6. It creates the lane, claims the paths, writes a private profile, and checks
   the staged diff.

The staged-path read must succeed when the worktree exists. Git inspection
errors stop setup and preserve its recovery receipt. Staged whitespace errors
remain nonfatal, but the human and JSON results report them. A future worktree
dry-run reports that its staged check did not run.

The installers publish a complete file without overwriting an existing path.
They never overwrite different content or follow a target symlink. A retry can
finish a matching partial skill bundle after an interruption. The wizard does
not change global Git configuration or install hooks. Existing repository hooks
still run during capture.

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
  --mode shared \
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

## Capture work

Load the lane identity in each agent shell:

```text
eval "$(wip env --lane parser-errors)"
```

Stage only the paths that the agent owns. Other staged paths can remain in the
same index.

```text
git add -- internal/parser docs/parser.md
wip plan
wip commit
```

`wip plan` is read-only. It groups paths at component boundaries and suggests
one Conventional Commit prefix per group. The type is concrete only for clear
documentation, dependency, CI, and test-only groups. Review semantic intent and
dependency closure before you write the final plan.

The planner compares the active source index with the lane's current commit.
It omits staged paths that the lane already captured.

Interactive capture uses the same proposals and asks for one Conventional
Commit message per group. No ref moves until all gates pass.

Automation must supply a split plan or explicitly opt out:

```json
[
  {
    "message": "fix(parser): preserve source error locations",
    "files": ["internal/parser"],
    "verify": [
      {"argv": ["go", "test", "./internal/parser"], "timeout_ms": 120000}
    ]
  },
  {
    "message": "docs(parser): explain source error locations",
    "files": ["docs/parser.md"]
  }
]
```

```text
wip commit --plan plan.json
```

When one commit is intentional, use this explicit command:

```text
wip commit --single \
  --message "fix(parser): preserve source error locations" \
  --path internal/parser
```

The command rejects vague subjects, unsupported commit types, duplicate
messages, subjects longer than 72 characters, and `wip:` unless you pass
`--allow-wip`.

## Continue and finish

Leases last 15 minutes while the agent edits. Renew them between captures:

```text
wip renew
```

`wip commit` snapshots and renews the lane's complete active lease set before
capture. It renews that same set in the background. The final publication check
renews and compares the set again.

Add another claim with:

```text
wip claim --path internal/scanner
```

Inspect the lane with:

```text
wip status
```

When the task is complete, release the coordination state:

```text
wip release
```

Release and abort both preserve the local agent ref. Neither command deletes
commits, lands a branch, pushes a remote, or merges work.

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

The preview returns an exact cutoff and plan digest. Reuse both values:

```text
wip archive \
  --cutoff '<exact-preview-cutoff>' \
  --plan-digest '<exact-preview-sha256>' \
  --apply --yes
```

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

## Important index behavior

A successful capture leaves the source `HEAD`, worktree, and complete Git index
unchanged. Captured entries therefore remain staged relative to the source
branch. This is deliberate: `wip` cannot safely clear selected entries while an
uncoordinated process can update the shared index.

`wip` ignores inherited repository-local Git variables that can redirect or
reinterpret the repository, common directory, worktree, namespace, refs,
shallow boundary, configuration, or object storage. The canonical repository
selected by `--repo-dir` or the current directory wins. It honors an inherited
`GIT_INDEX_FILE` as the caller's active source index. Capture hooks receive the
private candidate index instead.

You can modify and stage a later version of the same leased path. The next
capture compares that staged version with the current lane commit. Do not run a
broad reset, stash, or clean command to hide the staged state.

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
defines the safety boundary for a possible future landing command without
authorizing its implementation. Non-authors can use the redacted
[beta exercise](docs/BETA-EXERCISE.md) to produce independent evidence.
Repository owners can use the human-gated
[publication handoff](docs/PUBLICATION-HANDOFF.md) and
[hosted setup runbook](docs/HOSTED-SETUP.md). Use the
[correction receipt](docs/PUBLICATION-CORRECTION.md) after a hosted candidate
changes. These procedures do not authorize a push or release tag.

Project authority and succession are in [GOVERNANCE.md](GOVERNANCE.md).
Current support and safe incident-reporting guidance are in
[SUPPORT.md](SUPPORT.md). Security reports use [SECURITY.md](SECURITY.md).
The [open source practice guide](docs/OSS-PRACTICE-GUIDE.md) maps official
guidance to project rules and records when maintainers must review it again.
Local self-hosting receipts and safe-failure results are in
[docs/DOGFOOD.md](docs/DOGFOOD.md).

## License

MIT
