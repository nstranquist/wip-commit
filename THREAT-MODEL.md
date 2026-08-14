# Threat model

## Protected assets

`wip` protects these local assets during capture:

- the source checkout's `HEAD`;
- all entries in the source Git index;
- staged content outside the selected paths;
- each lane ref and lane cursor;
- path-lease ownership;
- the order, tree, message, and scope of every planned commit;
- the recovery record for an applied ref update.

The primary failure to prevent is a successful-looking capture that includes
another agent's staged work, publishes only part of a split plan, or loses the
commit after a process stops.

## Trust boundaries

The following components are trusted:

- Git and the local filesystem;
- cooperating processes that obey path leases;
- repository hooks;
- commands listed in a plan's `verify` array;
- the operating-system user who owns the repository.

The following components are not treated as trusted inputs:

- concurrent lane commands;
- the current shared index outside the selected paths;
- persisted lane, lease, profile, and intent files;
- plan JSON;
- hook paths that can change during capture;
- verify working directories that can contain symlinks.

`wip` is not a security boundary against a malicious process with the same user
account, an administrator, compromised Git, or compromised kernel code.

## Controls

### Concurrent staged work

Path claims use portable Unicode case folding, NFC normalization, and component
boundaries. A repository-wide lease lock serializes claim decisions. Active
leases cannot overlap across lanes.

Capture reads exact stage-zero index entries for the planned paths into a
private index. It does not use `git add` against the source worktree. A final
digest check detects a selected staged entry that changed while the plan ran.
Changes to unrelated staged paths do not stop a disjoint capture and cannot
enter its private tree.

### Partial split publication

Every group builds on the preceding private tree. Pre-commit hooks, `git diff
--check`, and typed verify commands run before publication. Commit objects are
created off-ref. One `git update-ref <new> <old>` compare-and-swap publishes the
complete chain.

### Ref and metadata races

Each lane has an operating-system file lock. The ref update also requires the
expected old object ID. The source `HEAD`, selected index digest, hook snapshots,
lane cursor, lane identity, worktree binding, and active lease set are checked
again at the publication boundary.

### Crash recovery

Lane creation writes a `creating` manifest before it creates the ref. An exact
retry can finish either side of that boundary.

A capture writes an immutable `prepared` intent before it updates the ref. It
then records `applied` after the ref compare-and-swap and `complete` after lane
metadata advances. Intent files use mode `0600`, bounded strict JSON, a digest
over immutable fields, file synchronization, and directory synchronization on
Unix.

`wip reconcile` checks all immutable Git evidence before it advances metadata.

### Hooks

`wip` opens each pre-commit and post-commit hook, compares the open file with the
path, requires a regular non-symlink file, limits it to 4 MiB, and copies it to a
private mode-`0700` path. It validates the original path again before the ref
update. The copied hook runs with the private index.

On Windows, Git 2.36 or newer runs the private hook so that Git for Windows can
apply its normal interpreter rules.

### Verify commands

Plan commands use typed argument arrays. They do not pass through a shell. Each
command runs in a temporary checkout of the candidate private index. A command
directory cannot be absolute, escape with `..`, or follow a symlink outside the
candidate tree. Time, argument, output, command-count, group-count, path-count,
and plan-size limits are enforced.

On Unix, a timed-out command kills its process group. Other platforms use Go's
bounded process wait behavior.

### Persisted state and installation

State readers require bounded regular non-symlink files, reject path replacement
during reads, reject unknown JSON fields, and validate stored identities and
canonical paths. Atomic replacement is used for state transitions. Windows uses
`MoveFileEx` with replace and write-through flags.

The store rejects unsupported state-directory and record-schema versions. It
checks the directory version before it creates local state. This rule prevents
an older binary from silently coordinating through a separate state version.

`wip init --install` creates a new binary with exclusive file creation. It
accepts an existing target only when the bytes match and the file is executable.
It never replaces a different target.

## Residual risks

- A Git command that does not use `wip` can ignore path leases. The final check
  detects selected-index movement before publication, but no transaction can
  atomically lock every external Git implementation. A write in the small gap
  after the final read can make the receipt describe the earlier staged
  snapshot. It cannot make `wip` rewrite the source index or include an
  unselected path.
- Hooks and verify commands are trusted code. They can change the worktree,
  start background work, access credentials, or use the network.
- A post-commit hook runs after the ref update. Its failure is reported as a
  warning and does not roll back the published local ref.
- Process-tree termination is strongest on Unix. Descendant cleanup is
  best-effort on Windows and unsupported platforms.
- Directory synchronization is not available through the same primitive on
  Windows. File replacement uses the strongest practical write-through API.
- Advisory locks can be unreliable on some network filesystems. Use a local
  filesystem for concurrent capture.
- Paths that are not valid UTF-8 are not supported by plans.
- Leases use the host wall clock. Large clock changes can shorten or extend a
  lease.
- `wip` does not land, merge, push, delete, reset, stash, or clean work. Those
  operations need a separate reviewed policy.

## Safe deployment statement

The beta is designed for cooperating local agents on repositories that use a
local filesystem. Do not describe it as safe for hostile multi-tenant users,
untrusted hooks, network filesystems, or automatic branch landing.
