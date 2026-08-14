# wip-commit

`wip` lets concurrent agents capture exact staged subsets without moving the
source checkout's `HEAD` or rewriting its Git index. Each agent writes to a
local ref under `refs/heads/wip/<agent>/<lane>`.

The current release candidate is `v0.1.0-beta.1`. It is suitable for local beta
use. It is not yet a stable release, and this checkout has not been published.

## Why it exists

A normal `git commit` consumes the complete shared index and moves the checked
out branch. That operation is unsafe when several agents use one checkout.
`wip` uses three controls:

- Path leases prevent active lanes from claiming overlapping paths.
- A private index contains only the planned staged entries.
- One compare-and-swap updates the agent ref after all commit groups pass.

Two lanes can therefore capture disjoint staged paths in parallel. They do not
commit directly to the same checked-out branch. Review and landing are separate
operations.

## Requirements

- Git 2.36 or newer
- Go 1.25 or newer to build from source
- A local filesystem with working advisory file locks

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

## Start with `wip init`

Run the wizard in a Git checkout:

```text
wip init
```

The wizard does the following work:

1. It inspects the repository and recommends `shared` or `worktree` mode.
2. It asks for the agent, session, lane, base ref, and path claims.
3. It can create a detached linked worktree when you explicitly select one.
4. It can copy the current binary to `~/.local/bin` when you explicitly agree.
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
directory. If the directory already contains a matching linked worktree at the
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
hook, diff check, and verify command passes.

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

Use one commit only when that is intentional:

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

Release the coordination state when the task is complete:

```text
wip release
```

Release and abort both preserve the local agent ref. Neither command deletes
commits, lands a branch, pushes a remote, or merges work.

## Important index behavior

A successful capture leaves the source `HEAD`, worktree, and complete Git index
unchanged. Captured entries therefore remain staged relative to the source
branch. This is deliberate: `wip` cannot safely clear selected entries while an
uncoordinated process might update the shared index.

You can modify and stage a later version of the same leased path. The next
capture compares that staged version with the current lane commit. Do not run a
broad reset, stash, or clean command to hide the staged state.

## Recover an interrupted capture

The JSON error result includes `plan_id` and `plan_digest` when the agent ref
moved but metadata did not finish. Use both immutable values:

```text
wip reconcile \
  --plan-id plan-0123456789abcdef01234567 \
  --plan-digest sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef
```

Reconciliation checks the target ref, every parent, tree, message, changed path,
allowed scope, and final tree before it updates lane metadata. Exact retries are
idempotent.

## Safety scope

`wip` protects coordination between cooperating local processes. It does not
protect against a malicious process running as the same operating-system user.
Hooks and verify commands are trusted repository code and can change files or
use the network. Read [THREAT-MODEL.md](THREAT-MODEL.md) before broad adoption.

The `wip` binary does not send telemetry or contact a network service. Use
`--json` for local structured evidence. A repository hook or an explicit verify
command can still use the network.

See [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) for the transaction model,
[docs/ERRORS.md](docs/ERRORS.md) for recovery actions, and
[docs/OSS-READINESS.md](docs/OSS-READINESS.md) for the publication summary. The
detailed proposal and evidence tracker are in
[docs/OSS-PUBLIC-BETA.md](docs/OSS-PUBLIC-BETA.md).

## License

MIT
