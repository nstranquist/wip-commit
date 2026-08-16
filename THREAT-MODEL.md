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

The folded path key is idempotent, including Cherokee case pairs. Fuzz tests
verify key stability, overlap symmetry, and coverage consistency.

Capture renews its complete lease set before work starts. A heartbeat renews
the set during long gates. The publication callback renews the same set while
it holds the lease-registry lock. Each refresh also audits all active lease
records and stops if persisted state contains a cross-lane overlap.

Normal renewal and release also inspect the complete lease registry before a
write. Release verifies lane ownership and both directions of every lease
reference. It permits valid partial release so an interrupted release can
finish.

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

The command encodes and bounds the complete intent before the ref update. A
late heartbeat failure returns the applied receipt instead of reporting
success. This receipt permits exact reconciliation after the ref moved.

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
canonical paths. A first write creates and synchronizes a complete temporary
file, then uses a same-filesystem hard link to publish it without overwrite.
Atomic replacement is used for state transitions. Windows uses `MoveFileEx`
with replace and write-through flags. Record writes stage temporary files in the
store's `locks` directory, so an interrupted writer cannot add a foreign entry
to a lane, lease, profile, intent, or archive batch directory.

Each durable writer checks the final encoded size against its reader limit.
The archive writer performs this check before it creates a new batch. Thus, a
writer cannot publish a record that supported recovery commands cannot read.
New records also reserve their largest supported lifecycle form. This check
prevents a valid initial record from blocking a later completion or release.

New lease records use cryptographically random identifiers and no-overwrite
publication. Normal lane and lease scans reject unexpected record-directory
entries. A partially released lane cannot claim, renew, or capture paths; only
the idempotent release operation can finish that state transition.

State-directory creation uses a filesystem root handle. The handle confines
all paths to the Git common directory. The store rejects a symlink at `wip`,
`v1`, or any required state subdirectory.

Private-index directory creation uses a filesystem root handle for the checkout
Git directory. It rejects a symlink at `wip` or `wip/indexes` before it writes a
temporary index or hook snapshot.

Profiles must match the authoritative lane manifest. Shell output uses explicit
POSIX or PowerShell quoting. An invalid lane ID fails before path construction.

Git commands remove inherited repository-local environment variables except
the active source index. A caller cannot use routing, configuration, namespace,
replacement, shallow-file, or object-storage variables to make `--repo-dir`
inspect one checkout and write objects or refs through another. The caller's
`GIT_INDEX_FILE` remains the active source index. Prepared hooks receive only
the private candidate index for their Git operations.

The public and legacy implementations use one domain lock. Public state and
legacy `ndev-wip` state cannot start beside each other. Read-only legacy checks
do not create a legacy state directory. Invalid legacy actions and queue
previews also use inspection only. Only explicit legacy lane creation can claim
the legacy domain.

The store rejects unsupported state-directory and record-schema versions. It
checks the directory version before it creates local state. This rule prevents
an older binary from silently coordinating through a separate state version.

`wip init --install` synchronizes a complete temporary binary before a
no-overwrite hard-link publication. It accepts an existing target only when the
bytes match and the file is executable. It never replaces a different target.

`wip init --install-skill` installs the embedded public skill as a complete
directory. It rejects different files, extra files, and symlinks. A retry can
complete a matching partial bundle. Temporary skill files stay in the parent
skill directory, so interruption does not add a foreign entry to the bundle. A
durable initialization intent records each completed setup step and pins the
exact base commit and canonical paths. Domain validation and empty-store
bootstrap occur before this intent. The intent precedes worktree, lane, lease,
profile, binary, and skill changes. A per-intent lock serializes step updates,
so concurrent exact retries cannot replace a longer completed-step prefix with
a shorter one.

An exact initialization retry repeats the complete canonical path claim. This
action repairs a lease that was published before its lane back-reference. Git
index inspection errors stop the transaction and return its recovery receipt.

`wip doctor` reads bounded record sets and validates recovery state. `wip
archive` requires an exact reviewed digest and compares complete candidate
records under the archive, lane, and registry locks. The receipt binds every
lane and released lease record to its candidate and rejects extra batch records.
Archival moves the lane manifest last, preserves lane refs, and uses a durable
receipt for explicit resume. Restore records a durable `restoring` state before
its first move and can continue after an interruption.

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
- First-write publication needs same-filesystem hard links. An unsupported
  filesystem returns an error without publishing a partial destination.
- A hard process stop can leave a hidden complete temporary file. The file is
  not a state record. Inspect unexpected files before you remove them.
- Paths that are not valid UTF-8 are not supported by plans.
- Leases use the host wall clock. Large clock changes can shorten or extend a
  lease.
- `wip` does not land, merge, push, delete, reset, stash, or clean work. Those
  operations need a separate reviewed policy.

## Safe deployment statement

The beta is designed for cooperating local agents on repositories that use a
local filesystem. Do not describe it as safe for hostile multi-tenant users,
untrusted hooks, network filesystems, or automatic branch landing.
