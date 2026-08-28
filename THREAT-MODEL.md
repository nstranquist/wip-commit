# Threat model

## Protected assets

`wip` protects these local assets during capture:

- the source checkout's `HEAD`.
- all entries in the caller Git index.
- worktree and staged content outside the selected source paths.
- each lane ref and lane cursor.
- path-lease ownership.
- the order, tree, message, and scope of every planned commit.
- the reviewed source mode, digest, paths, lane, lease, and message-policy
  bindings.
- the recovery record for an applied ref update.

The primary failure is a successful-looking capture that includes another
agent's work. Other primary failures publish only part of a split plan or lose
the commit after a process stops.

## Trust boundaries

The following components are trusted:

- Git and the local filesystem.
- cooperating processes that obey path leases.
- repository hooks.
- commands listed in a plan's `verify` array.
- the operating-system user who owns the repository.

The following components are not treated as trusted inputs:

- concurrent lane commands.
- the current caller index and worktree outside the selected paths.
- persisted lane, lease, profile, and intent files.
- plan JSON.
- hook paths that can change during capture.
- verify working directories that can contain symlinks.

`wip` is not a security boundary against a malicious process with the same user
account, an administrator, compromised Git, or compromised kernel code.

## Controls

### Concurrent source work

Path claims use portable Unicode case folding, NFC normalization, and component
boundaries. A repository-wide lease lock serializes claim decisions. Active
leases cannot overlap across lanes.

The folded path key is idempotent, including Cherokee case pairs. Fuzz tests
verify key stability, overlap symmetry, and coverage consistency.

Capture renews its complete lease set before work starts. A heartbeat renews
the set during long gates. The publication callback renews the same set while
it holds the lease-registry lock. Each refresh also audits all active lease
records and stops if persisted state contains a cross-lane overlap.

Authenticated status, environment, claim, and plan commands renew the complete
active set after identity validation. Pure `--observe` status, environment,
and plan reads do not renew it.

Normal renewal and release also inspect the complete lease registry before a
write. Release verifies lane ownership and both directions of every lease
reference. It permits valid partial release so an interrupted release can
finish.

For `staged_index`, capture reads exact stage-zero caller-index entries for the
planned paths. It copies them into a private index. Unrelated staged paths
cannot enter the private tree.

For `worktree_snapshot`, capture builds a private source index from the lane
tip. It adds only tracked changes and reviewed untracked paths in the selected
scopes. It never runs `git add` against the caller index.

A final source-digest check detects a selected source entry that changed while
the plan ran. Changes outside the reviewed scopes do not stop a disjoint
capture and cannot enter its private tree.

### Partial split publication

Every group builds on the preceding private tree. Pre-commit hooks, `git diff
--check`, and typed verify commands run before publication. Commit objects are
created off-ref. One `git update-ref <new> <old>` compare-and-swap publishes the
complete chain.

### Reviewed-plan substitution

The version 3 plan binds the lane ref, lane tip, and source `HEAD`. It also binds
the source mode, untracked policy, source scopes, source paths, source digest,
active lease digest, and message-policy receipt.

`wip capture` recalculates every binding before it opens hooks. A changed input
returns `STALE_PLAN` and does not enter the capture transaction. The kernel
also compares its initial private-source snapshot with the reviewed digest.
This check closes the interval between command validation and kernel setup.

The capture intent stores the canonical reviewed-plan digest. The intent digest
covers that digest and the policy receipt.

### Message policy

The capture kernel validates message bytes, size, and subject presence. Named
repository policy validates message style before hooks or verification run.

The lane profile binds the resolved policy. A plan with another policy fails
before capture.

### Ref and metadata races

Each lane has an operating-system file lock. The ref update also requires the
expected old object ID. The source `HEAD`, selected source digest, hook
snapshots, lane cursor, lane identity, worktree binding, and active lease set
are checked again at the publication boundary.

Handoff validation and final release use the same lane lock. Another lane
transaction cannot change the cursor, intent selection, or lease links between
the validated handoff and release.

### Crash recovery

Lane creation writes a `creating` manifest before it creates the ref. An exact
retry can finish either side of that boundary.

Initialization publishes its intent before setup side effects, then holds the
per-intent transaction lock until every side effect is journaled or the command
fails. Exact retries and `init-abort` use the same lock. An abort therefore
cannot remove the recovery intent between lane creation and the corresponding
journal update.

A capture writes an immutable `prepared` intent before it updates the ref. It
then records `applied` after the ref compare-and-swap and `complete` after lane
metadata advances. Intent files use mode `0600`, bounded strict JSON, a digest
over immutable fields, file synchronization, and directory synchronization on
Unix.

The command encodes and bounds the complete intent before the ref update. A
late heartbeat failure returns the applied receipt instead of reporting
success. This receipt permits exact reconciliation after the ref moved.

`wip reconcile` checks all immutable Git evidence before it advances metadata.

### Capture handoff

The handoff command accepts only one unique, complete intent chain from the lane
base to its tip. Every intent and Git object must pass receipt validation. A
gap, ambiguity, branch, cycle, moved ref, or invalid object fails closed.

The version 2 handoff binds the complete chain with `handoff_digest`. It is
review-eligible only when every segment has an exact reviewed-plan,
message-policy, and version 3 private-source binding.

The command holds the lane lock while it validates the chain, renews an active
lease, and builds status. A failed renewal does not emit a successful handoff.

A released-lane handoff opens and locks the existing regular lock file without
creating directories or rewriting owner metadata. A missing, replaced, or
non-regular lock file fails closed.

The handoff omits agent IDs, session IDs, absolute paths, remote URLs, and file
contents. It states that the receipt does not authorize landing. A separate
review authority must bind any landing decision to the full handoff digest.

### Hooks

`wip` opens each pre-commit and post-commit hook and compares the open file with
the path. It requires a regular non-symlink file and limits it to 4 MiB. It then
copies the hook to a private mode-`0700` path. It validates the original path
again before the ref update. The copied hook runs with the private index and a
forced `NICOS_PREFLIGHT_READ_ONLY=1` value. Compatible hook wrappers use this
value to inspect the candidate without applying repairs to the source checkout.
The value is a cooperative contract. It does not restrict arbitrary hook code.

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
State transitions use atomic replacement. Windows uses `MoveFileEx` with replace
and write-through flags. Record writes stage temporary files in the store's
`locks` directory. Thus, an interrupted writer cannot add a foreign entry to a
lane, lease, profile, intent, or archive batch directory.

Each durable writer checks the final encoded size against its reader limit.
The archive writer performs this check before it creates a new batch. Thus, a
writer cannot publish a record that supported recovery commands cannot read.
New records also reserve their largest supported lifecycle form. This check
prevents a valid initial record from blocking a later completion or release.

New lease records use cryptographically random identifiers and no-overwrite
publication. Normal lane and lease scans reject unexpected record-directory
entries. A partially released lane cannot claim, renew, or capture paths. Only
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
the caller index selector for `staged_index`. A caller cannot use routing,
configuration, namespace, replacement, shallow-file, or object-storage
variables to redirect `--repo-dir`. Prepared hooks receive only the private
candidate index for their Git operations.

The public and legacy implementations use one domain lock. Public state and
legacy `ndev-wip` state cannot start beside each other. Read-only legacy checks
do not create a legacy state directory. Invalid legacy actions and queue
previews also use inspection only. Only explicit legacy lane creation can claim
the legacy domain.

Coordination migration requires an exact producer identity, plan digest,
prestate, poststate, and ref set. Preview is byte-for-byte read-only. Apply uses
one writer fence and one journal. It publishes canonical records before it
moves and seals the complete legacy root. It never dual-writes both roots and
never grants landing authority.

The completion receipt is immutable cutover evidence. It is not a lock on the
future live store. Completed migration status reports later live-store
movement, skips transient private-index contents, and keeps the static
migration envelope exact. Current record health remains the responsibility of
the bounded doctor.

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
  detects selected-source movement before publication. No transaction can lock
  every external Git implementation. A later write can make the receipt
  describe the earlier snapshot. It cannot make `wip` rewrite the caller index
  or include an unselected path.
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
- A handoff proves local capture integrity. It does not prove review approval,
  branch landing, public release, deployment, adoption, or revenue.
- `wip` does not land, merge, push, delete, reset, stash, or clean work. Those
  operations need a separate reviewed policy.

## Safe deployment statement

The beta is designed for cooperating local agents on repositories that use a
local filesystem. Do not describe it as safe for hostile multi-tenant users,
untrusted hooks, network filesystems, or automatic branch landing.
