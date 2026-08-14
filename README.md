# wip-commit

`wip` lets concurrent agents capture exact staged subsets without moving the
source checkout's `HEAD` or rewriting its Git index. Each agent writes to a
local ref under `refs/heads/wip/<agent>/<lane>`.

The current release candidate is `v0.1.0-beta.1`. It is suitable for local beta
use. It is not yet a stable release, and this checkout has not been published.

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

## Requirements

- Git 2.36 or newer.
- Go 1.25 or newer to build from source.
- A local filesystem with working advisory file locks.

## Build locally

This repository has no configured remote. Build the current checkout with:

```text
go build -trimpath -o ./bin/wip ./cmd/wip
./bin/wip version
```

After publication, the intended install command is:

```text
go install github.com/nstranquist/wip-commit/cmd/wip@v0.1.0-beta.1
```

Do not use that command until the module exists at that public address.

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

1. It inspects the repository and recommends `shared` or `worktree` mode.
2. It asks for the agent, session, lane, base ref, and path claims.
3. When you explicitly select one, it can create a detached linked worktree.
4. When you explicitly agree, it can copy the current binary to
   `~/.local/bin`.
5. It creates the lane, claims the paths, writes a private profile, and checks
   the staged diff.

The installer never overwrites a different binary. The wizard does not change
global Git configuration or install hooks. Existing repository hooks still run
during capture.

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
  --no-install
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
wip commit
```

Interactive capture proposes split groups by the first path component and asks
for one Conventional Commit message per group. No ref moves until every group,
hook, diff test, and command in `verify` passes.

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

Leases last 15 minutes. Renew them during long tasks:

```text
wip renew
```

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

## Important index behavior

A successful capture leaves the source `HEAD`, worktree, and complete Git index
unchanged. Captured entries therefore remain staged relative to the source
branch. This is deliberate: `wip` cannot safely clear selected entries while an
uncoordinated process can update the shared index.

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

See [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) for the transaction model,
[docs/ERRORS.md](docs/ERRORS.md) for recovery actions, and
[docs/STATE-COMPATIBILITY.md](docs/STATE-COMPATIBILITY.md) for upgrade and
downgrade rules. See [docs/RELEASE.md](docs/RELEASE.md) for deterministic beta
archives and the human-gated tag procedure. The publication summary is in
[docs/OSS-READINESS.md](docs/OSS-READINESS.md). The detailed proposal and
evidence tracker are in
[docs/OSS-PUBLIC-BETA.md](docs/OSS-PUBLIC-BETA.md).

## License

MIT
