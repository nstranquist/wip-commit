package store

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/nstranquist/wip-commit/internal/atomicfile"
	"github.com/nstranquist/wip-commit/internal/fail"
	"github.com/nstranquist/wip-commit/internal/filelock"
	"github.com/nstranquist/wip-commit/internal/gitx"
	"github.com/nstranquist/wip-commit/internal/pathid"
	"github.com/nstranquist/wip-commit/internal/recordjson"
	"github.com/nstranquist/wip-commit/internal/safeio"
	"github.com/nstranquist/wip-commit/internal/strictjson"
)

const (
	SchemaVersion       = 1
	DefaultLeaseTTL     = 15 * time.Minute
	maxRecordBytes      = 1 << 20
	maxStateEntries     = 32
	maxRecordEntries    = 10_000
	domainSchemaVersion = 1
	domainID            = "github.com/nstranquist/wip-commit"
)

var (
	identifierPattern     = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
	stateDirectoryPattern = regexp.MustCompile(`^v([0-9]+)$`)
)

func ValidateID(value, label string) error { return validateID(value, label) }

type Mode string

const (
	ModeShared   Mode = "shared"
	ModeWorktree Mode = "worktree"
)

type Lane struct {
	SchemaVersion int       `json:"schema_version"`
	ID            string    `json:"id"`
	Agent         string    `json:"agent"`
	Session       string    `json:"session"`
	Mode          Mode      `json:"mode"`
	Ref           string    `json:"ref"`
	BaseRef       string    `json:"base_ref"`
	BaseSHA       string    `json:"base_sha"`
	CurrentSHA    string    `json:"current_sha"`
	Worktree      string    `json:"worktree"`
	LeaseIDs      []string  `json:"lease_ids,omitempty"`
	State         string    `json:"state"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
	LastCommit    string    `json:"last_commit,omitempty"`
}

type Lease struct {
	SchemaVersion int        `json:"schema_version"`
	ID            string     `json:"id"`
	LaneID        string     `json:"lane_id"`
	Agent         string     `json:"agent"`
	Session       string     `json:"session"`
	Paths         []string   `json:"paths"`
	State         string     `json:"state"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
	ExpiresAt     *time.Time `json:"expires_at,omitempty"`
	ReleasedAt    *time.Time `json:"released_at,omitempty"`
}

// LeaseConflict identifies one active cross-lane path overlap.
type LeaseConflict struct {
	LeftLane, LeftLease, LeftPath    string
	RightLane, RightLease, RightPath string
}

type Status struct {
	Lane   Lane    `json:"lane"`
	Leases []Lease `json:"leases"`
}

type CreateOptions struct {
	ID, Agent, Session, BaseRef, Worktree string
	Mode                                  Mode
}

type Store struct {
	Repo     gitx.Repo
	Root     string
	LeaseTTL time.Duration
}

func Open(repo gitx.Repo) (Store, error) {
	coordinationLock, err := filelock.Acquire(filepath.Join(repo.CommonDir, "wip-coordination.lock"), 0)
	if err != nil {
		return Store{}, fail.Wrap("LOCK_TIMEOUT", err)
	}
	defer func() { _ = coordinationLock.Release() }()
	root, err := prepareStateRoot(repo, true)
	if err != nil {
		return Store{}, err
	}
	return Store{Repo: repo, Root: root, LeaseTTL: DefaultLeaseTTL}, nil
}

// Check validates the coordination domain and existing state without creating
// files or directories. It is suitable for init preflight and dry-run.
func Check(repo gitx.Repo) error {
	_, err := prepareStateRoot(repo, false)
	return err
}

// Inspect opens existing state read-only. It never creates a marker, state
// directory, lock, lane, or lease.
func Inspect(repo gitx.Repo) (Store, bool, error) {
	if err := Check(repo); err != nil {
		return Store{}, false, err
	}
	root := filepath.Join(repo.CommonDir, "wip", fmt.Sprintf("v%d", SchemaVersion))
	info, err := os.Lstat(root)
	if errors.Is(err, os.ErrNotExist) {
		if _, markerErr := os.Lstat(filepath.Join(repo.CommonDir, "wip", "domain.json")); errors.Is(markerErr, os.ErrNotExist) {
			return Store{Repo: repo, Root: root, LeaseTTL: DefaultLeaseTTL}, false, nil
		} else if markerErr != nil {
			return Store{}, false, fail.Wrap("STORE_FAILED", markerErr)
		}
		return Store{Repo: repo, Root: root, LeaseTTL: DefaultLeaseTTL}, true, nil
	}
	if err != nil {
		return Store{}, false, fail.Wrap("STORE_FAILED", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return Store{}, false, fail.New("STORE_FAILED", "wip state version root is not a regular directory")
	}
	return Store{Repo: repo, Root: root, LeaseTTL: DefaultLeaseTTL}, true, nil
}

type domainRecord struct {
	SchemaVersion int    `json:"schema_version"`
	Domain        string `json:"domain"`
	StateVersion  int    `json:"state_version"`
}

func prepareStateRoot(repo gitx.Repo, create bool) (string, error) {
	common, err := os.OpenRoot(repo.CommonDir)
	if err != nil {
		return "", fail.Wrap("STORE_FAILED", err)
	}
	defer func() { _ = common.Close() }()
	if _, err := common.Lstat("ndev-wip"); err == nil {
		return "", fail.New("COORDINATION_DOMAIN_CONFLICT", "legacy ndev-wip state exists; stop all lane processes and migrate or archive that domain before using standalone wip")
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", fail.Wrap("STORE_FAILED", err)
	}
	stateExists, err := ensureDirectory(common, "wip", create)
	if err != nil {
		return "", err
	}
	stateRoot := filepath.Join(repo.CommonDir, "wip")
	root := filepath.Join(stateRoot, fmt.Sprintf("v%d", SchemaVersion))
	if !stateExists {
		return root, nil
	}
	if err := validateStateDirectories(common, "wip"); err != nil {
		return "", err
	}
	versionPath := fmt.Sprintf("wip/v%d", SchemaVersion)
	versionExists, err := ensureDirectory(common, versionPath, false)
	if err != nil {
		return "", err
	}
	if versionExists {
		if err := validateVersionLayout(common, versionPath); err != nil {
			return "", err
		}
	}
	markerPath := filepath.Join(stateRoot, "domain.json")
	markerBody, markerErr := safeio.ReadRegular(markerPath, 64<<10)
	if errors.Is(markerErr, os.ErrNotExist) {
		if !create {
			return root, nil
		}
		marker := domainRecord{SchemaVersion: domainSchemaVersion, Domain: domainID, StateVersion: SchemaVersion}
		body, marshalErr := json.MarshalIndent(marker, "", "  ")
		if marshalErr != nil {
			return "", fail.Wrap("STORE_FAILED", marshalErr)
		}
		if createErr := atomicfile.Create(markerPath, append(body, '\n'), 0o600); createErr != nil && !errors.Is(createErr, atomicfile.ErrExists) {
			return "", fail.Wrap("STORE_FAILED", createErr)
		}
		markerBody, markerErr = safeio.ReadRegular(markerPath, 64<<10)
	}
	if markerErr != nil {
		return "", fail.Wrap("STORE_FAILED", markerErr)
	}
	var marker domainRecord
	if err := strictjson.Decode(markerBody, &marker); err != nil {
		return "", fail.Wrap("STORE_FAILED", err)
	}
	if marker.Domain != domainID {
		return "", fail.New("COORDINATION_DOMAIN_CONFLICT", "wip state is owned by an unsupported coordination domain")
	}
	if marker.SchemaVersion != domainSchemaVersion {
		return "", fail.Errorf("MIGRATION_REQUIRED", "domain marker schema version %d is unsupported; this wip release supports version %d", marker.SchemaVersion, domainSchemaVersion)
	}
	if marker.StateVersion != SchemaVersion {
		return "", fail.Errorf("MIGRATION_REQUIRED", "state domain owns v%d; this wip release supports v%d", marker.StateVersion, SchemaVersion)
	}
	if !create {
		return root, nil
	}
	paths := []string{fmt.Sprintf("wip/v%d", SchemaVersion)}
	for _, directory := range []string{"lanes", "leases", "locks", "intents", "profiles", "init-intents", "archive"} {
		paths = append(paths, fmt.Sprintf("wip/v%d/%s", SchemaVersion, directory))
	}
	for _, path := range paths {
		if _, err := ensureDirectory(common, path, true); err != nil {
			return "", err
		}
	}
	return root, nil
}

func ensureDirectory(root *os.Root, path string, create bool) (bool, error) {
	info, err := root.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		if !create {
			return false, nil
		}
		if err := root.Mkdir(path, 0o700); err != nil {
			return false, fail.Wrap("STORE_FAILED", err)
		}
		info, err = root.Lstat(path)
	}
	if err != nil {
		return false, fail.Wrap("STORE_FAILED", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return false, fail.Errorf("STORE_FAILED", "%s is not a regular state directory", path)
	}
	if create {
		if err := root.Chmod(path, 0o700); err != nil {
			return false, fail.Wrap("STORE_FAILED", err)
		}
	}
	return true, nil
}

func validateStateDirectories(root *os.Root, path string) error {
	directory, err := root.Open(path)
	if err != nil {
		return fail.Wrap("STORE_FAILED", err)
	}
	defer func() { _ = directory.Close() }()
	entries, err := directory.ReadDir(maxStateEntries + 1)
	if err != nil && !errors.Is(err, io.EOF) {
		return fail.Wrap("STORE_FAILED", err)
	}
	if len(entries) > maxStateEntries {
		return fail.Errorf("STORE_FAILED", "wip state root exceeds %d entries", maxStateEntries)
	}
	for _, entry := range entries {
		match := stateDirectoryPattern.FindStringSubmatch(entry.Name())
		if len(match) != 2 {
			continue
		}
		version, parseErr := strconv.ParseUint(match[1], 10, 31)
		if parseErr != nil || version != SchemaVersion || entry.Name() != fmt.Sprintf("v%d", SchemaVersion) {
			return fail.Errorf("MIGRATION_REQUIRED", "state directory %s is unsupported; this wip release supports v%d", entry.Name(), SchemaVersion)
		}
		info, statErr := root.Lstat(filepath.Join(path, entry.Name()))
		if statErr != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fail.Errorf("STORE_FAILED", "state directory %s is not a regular directory", entry.Name())
		}
	}
	return nil
}

func validateVersionLayout(root *os.Root, path string) error {
	directory, err := root.Open(path)
	if err != nil {
		return fail.Wrap("STORE_FAILED", err)
	}
	defer func() { _ = directory.Close() }()
	entries, err := directory.ReadDir(maxStateEntries + 1)
	if err != nil && !errors.Is(err, io.EOF) {
		return fail.Wrap("STORE_FAILED", err)
	}
	if len(entries) > maxStateEntries {
		return fail.Errorf("STORE_FAILED", "%s exceeds %d entries", path, maxStateEntries)
	}
	allowed := map[string]bool{
		"lanes": true, "leases": true, "locks": true, "intents": true,
		"profiles": true, "init-intents": true, "archive": true,
	}
	for _, entry := range entries {
		if !allowed[entry.Name()] {
			return fail.Errorf("MIGRATION_REQUIRED", "state entry %s/%s is unsupported by version %d", path, entry.Name(), SchemaVersion)
		}
		info, statErr := root.Lstat(filepath.Join(path, entry.Name()))
		if statErr != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fail.Errorf("STORE_FAILED", "state entry %s/%s is not a regular directory", path, entry.Name())
		}
	}
	return nil
}

func LaneRef(agent, lane string) string { return "refs/heads/wip/" + agent + "/" + lane }

func (store Store) LaneLock(id string, wait time.Duration) (*filelock.Lock, error) {
	if err := validateID(id, "lane"); err != nil {
		return nil, err
	}
	lock, err := filelock.Acquire(filepath.Join(store.Root, "locks", "lane-"+id+".lock"), wait)
	if err != nil {
		return nil, fail.Wrap("LOCK_TIMEOUT", err)
	}
	return lock, nil
}

// The registry fence serializes operational lane and lease records. Windows
// can reject an open while another process atomically replaces a record. A
// caller that also needs a lane lock must acquire the lane lock first.
func (store Store) registryLock(wait time.Duration) (*filelock.Lock, error) {
	lock, err := filelock.Acquire(filepath.Join(store.Root, "locks", "leases.lock"), wait)
	if err != nil {
		return nil, fail.Wrap("LOCK_TIMEOUT", err)
	}
	return lock, nil
}

func (store Store) Create(ctx context.Context, options CreateOptions) (Lane, error) {
	for label, value := range map[string]string{"lane": options.ID, "agent": options.Agent, "session": options.Session} {
		if err := validateID(value, label); err != nil {
			return Lane{}, err
		}
	}
	if options.Mode != ModeShared && options.Mode != ModeWorktree {
		return Lane{}, fail.New("INVALID_MODE", "mode must be shared or worktree")
	}
	if options.BaseRef == "" {
		options.BaseRef = "HEAD"
	}
	base, err := store.Repo.Text(ctx, nil, "rev-parse", "--verify", options.BaseRef+"^{commit}")
	if err != nil {
		return Lane{}, fail.Wrap("BASE_NOT_FOUND", err)
	}
	worktree := store.Repo.Root
	if options.Worktree != "" {
		selected, discoverErr := gitx.Discover(ctx, options.Worktree)
		if discoverErr != nil {
			return Lane{}, fail.Wrap("WORKTREE_NOT_REGISTERED", discoverErr)
		}
		if selected.CommonDir != store.Repo.CommonDir || selected.Root != store.Repo.Root {
			return Lane{}, fail.New("WORKTREE_MISMATCH", "--worktree must be the current checkout in this Git common directory")
		}
		worktree = selected.Root
	}
	if options.Mode == ModeWorktree && store.Repo.GitDir == store.Repo.CommonDir {
		return Lane{}, fail.New("ANCHOR_WORKTREE_REFUSED", "worktree mode requires a linked non-anchor worktree")
	}
	head, err := store.Repo.Text(ctx, nil, "rev-parse", "HEAD")
	if err != nil || head != base {
		return Lane{}, fail.Errorf("WORKTREE_BASE_MISMATCH", "worktree HEAD must equal base %s", base)
	}
	ref := LaneRef(options.Agent, options.ID)
	if _, err := store.Repo.Text(ctx, nil, "check-ref-format", ref); err != nil {
		return Lane{}, fail.Wrap("INVALID_REF", err)
	}
	lock, err := store.LaneLock(options.ID, 0)
	if err != nil {
		return Lane{}, err
	}
	defer func() { _ = lock.Release() }()
	registry, registryErr := store.registryLock(0)
	if registryErr != nil {
		return Lane{}, registryErr
	}
	defer func() { _ = registry.Release() }()
	if conflict, conflictErr := store.worktreeConflict(options.ID, worktree, options.Mode); conflictErr != nil {
		return Lane{}, conflictErr
	} else if conflict != "" {
		return Lane{}, fail.Errorf("WORKTREE_CONFLICT", "worktree is already bound to incompatible lane %s", conflict)
	}
	candidate := Lane{SchemaVersion: SchemaVersion, ID: options.ID, Agent: options.Agent, Session: options.Session, Mode: options.Mode, Ref: ref, BaseRef: options.BaseRef, BaseSHA: base, CurrentSHA: base, Worktree: worktree, State: "creating"}
	if _, statErr := os.Stat(store.lanePath(options.ID)); statErr == nil {
		existing, loadErr := store.loadLane(options.ID)
		if loadErr != nil {
			return Lane{}, loadErr
		}
		if existing.State != "creating" || !sameCreation(existing, candidate) {
			return Lane{}, fail.New("LANE_EXISTS", "lane already exists: "+options.ID)
		}
		return store.finishCreate(ctx, existing)
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return Lane{}, fail.Wrap("STORE_FAILED", statErr)
	}
	if _, verifyErr := store.Repo.Text(ctx, nil, "rev-parse", "--verify", ref+"^{commit}"); verifyErr == nil {
		return Lane{}, fail.New("LANE_EXISTS", "lane ref already exists: "+ref)
	}
	now := time.Now().UTC()
	candidate.CreatedAt, candidate.UpdatedAt = now, now
	if err := store.writeLane(candidate); err != nil {
		return Lane{}, err
	}
	return store.finishCreate(ctx, candidate)
}

func (store Store) finishCreate(ctx context.Context, lane Lane) (Lane, error) {
	actual, err := store.Repo.Text(ctx, nil, "rev-parse", "--verify", lane.Ref+"^{commit}")
	if err == nil {
		if actual != lane.BaseSHA {
			return Lane{}, fail.New("LANE_EXISTS", "lane ref does not match the creating lane base")
		}
	} else if _, err := store.Repo.Text(ctx, nil, "update-ref", lane.Ref, lane.BaseSHA, ""); err != nil {
		return Lane{}, fail.Wrap("REF_CREATE_FAILED", err)
	}
	lane.State, lane.UpdatedAt = "active", time.Now().UTC()
	if err := store.writeLane(lane); err != nil {
		return Lane{}, err
	}
	return lane, nil
}

func (store Store) Claim(id, agent, session string, paths []string) (Lease, error) {
	if err := validateID(agent, "agent"); err != nil {
		return Lease{}, err
	}
	if err := validateID(session, "session"); err != nil {
		return Lease{}, err
	}
	lock, err := store.LaneLock(id, 0)
	if err != nil {
		return Lease{}, err
	}
	defer func() { _ = lock.Release() }()
	lane, err := store.loadLane(id)
	if err != nil {
		return Lease{}, err
	}
	if err := ownedActive(lane, agent, session); err != nil {
		return Lease{}, err
	}
	normalized, err := store.Repo.NormalizePaths(paths)
	if err != nil {
		return Lease{}, err
	}
	if len(normalized) == 0 {
		return Lease{}, fail.New("INVALID_ARGS", "at least one path is required")
	}
	registry, err := store.registryLock(0)
	if err != nil {
		return Lease{}, err
	}
	defer func() { _ = registry.Release() }()
	leases, err := store.leases("", false)
	if err != nil {
		return Lease{}, err
	}
	if err := validateClaimLeaseLinks(lane, leases, normalized); err != nil {
		return Lease{}, err
	}
	now := time.Now().UTC()
	if conflict := FindActiveLeaseConflict(leases, now); conflict != nil {
		return Lease{}, leaseConflictError(conflict)
	}
	for _, existing := range leases {
		if existing.LaneID == id {
			if sameStrings(existing.Paths, normalized) {
				if !contains(lane.LeaseIDs, existing.ID) {
					lane.LeaseIDs = append(lane.LeaseIDs, existing.ID)
					lane.UpdatedAt = time.Now().UTC()
					if err := store.writeLane(lane); err != nil {
						return Lease{}, err
					}
				}
				if activeLease(existing, now) {
					return existing, nil
				}
			}
			continue
		}
		if !activeLease(existing, now) {
			continue
		}
		for _, wanted := range normalized {
			for _, held := range existing.Paths {
				if pathid.Overlap(wanted, held) {
					return Lease{}, fail.Errorf("PATH_LEASE_CONFLICT", "path %q conflicts with lane %s path %q", wanted, existing.LaneID, held)
				}
			}
		}
	}
	expires := now.Add(store.leaseTTL())
	lease := Lease{SchemaVersion: SchemaVersion, LaneID: id, Agent: agent, Session: session, Paths: normalized, State: "active", CreatedAt: now, UpdatedAt: now, ExpiresAt: &expires}
	updatedLane := lane
	for attempt := 0; attempt < 4; attempt++ {
		lease.ID, err = newLeaseID()
		if err != nil {
			return Lease{}, err
		}
		updatedLane = lane
		updatedLane.LeaseIDs = append(append([]string(nil), lane.LeaseIDs...), lease.ID)
		updatedLane.UpdatedAt = now
		if err := validateLaneRecordCapacity(updatedLane); err != nil {
			return Lease{}, err
		}
		err = store.createLease(lease)
		if err == nil {
			break
		}
		if !errors.Is(err, atomicfile.ErrExists) {
			return Lease{}, err
		}
	}
	if err != nil {
		return Lease{}, fail.New("STORE_FAILED", "could not allocate a unique lease id")
	}
	if err := store.writeLane(updatedLane); err != nil {
		return Lease{}, err
	}
	return lease, nil
}

func (store Store) Renew(id, agent, session string) ([]Lease, error) {
	lock, err := store.LaneLock(id, 0)
	if err != nil {
		return nil, err
	}
	defer func() { _ = lock.Release() }()
	lane, err := store.loadLane(id)
	if err != nil {
		return nil, err
	}
	if err := ownedActive(lane, agent, session); err != nil {
		return nil, err
	}
	registry, err := store.registryLock(0)
	if err != nil {
		return nil, err
	}
	defer func() { _ = registry.Release() }()
	leases, err := store.leases("", false)
	if err != nil {
		return nil, err
	}
	if err := validateLaneLeaseLinks(lane, leases); err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	if conflict := FindActiveLeaseConflict(leases, now); conflict != nil {
		return nil, leaseConflictError(conflict)
	}
	var renewed []Lease
	for _, lease := range leases {
		if lease.LaneID != id || !activeLease(lease, now) {
			continue
		}
		expires := now.Add(store.leaseTTL())
		lease.UpdatedAt, lease.ExpiresAt = now, &expires
		if err := store.writeLease(lease); err != nil {
			return nil, err
		}
		renewed = append(renewed, lease)
	}
	if len(renewed) == 0 {
		return nil, fail.New("LEASE_EXPIRED", "lane has no renewable active lease; claim paths again")
	}
	return renewed, nil
}

func (store Store) Current(agent, session, id string) (Status, error) {
	registry, err := store.registryLock(0)
	if err != nil {
		return Status{}, err
	}
	defer func() { _ = registry.Release() }()
	return store.currentLocked(agent, session, id)
}

func (store Store) currentLocked(agent, session, id string) (Status, error) {
	entries, err := readRecordEntries(filepath.Join(store.Root, "lanes"))
	if err != nil {
		return Status{}, fail.Wrap("STORE_FAILED", err)
	}
	var matches []Lane
	for _, entry := range entries {
		fileID, entryErr := recordEntryID(entry, "lane")
		if entryErr != nil {
			return Status{}, entryErr
		}
		lane, loadErr := store.loadLane(fileID)
		if loadErr != nil {
			return Status{}, loadErr
		}
		if lane.State != "active" || lane.Worktree != store.Repo.Root {
			continue
		}
		if id != "" && lane.ID != id || agent != "" && lane.Agent != agent || session != "" && lane.Session != session {
			continue
		}
		matches = append(matches, lane)
	}
	if len(matches) == 0 {
		return Status{}, fail.New("LANE_NOT_ACTIVE", "no active lane matches this checkout and identity")
	}
	if len(matches) > 1 {
		ids := make([]string, 0, len(matches))
		for _, lane := range matches {
			ids = append(ids, lane.ID)
		}
		sort.Strings(ids)
		return Status{}, fail.New("LANE_AMBIGUOUS", "multiple lanes match; select one: "+strings.Join(ids, ", "))
	}
	return store.statusLocked(matches[0].ID)
}

func (store Store) Status(id string) (Status, error) {
	registry, err := store.registryLock(0)
	if err != nil {
		return Status{}, err
	}
	defer func() { _ = registry.Release() }()
	return store.statusLocked(id)
}

func (store Store) statusLocked(id string) (Status, error) {
	lane, err := store.loadLane(id)
	if err != nil {
		return Status{}, err
	}
	leases, err := store.leases(id, false)
	return Status{Lane: lane, Leases: leases}, err
}

func (store Store) Load(id string) (Lane, error) {
	registry, err := store.registryLock(0)
	if err != nil {
		return Lane{}, err
	}
	defer func() { _ = registry.Release() }()
	return store.loadLane(id)
}

func (store Store) loadLane(id string) (Lane, error) {
	if err := validateID(id, "lane"); err != nil {
		return Lane{}, err
	}
	body, err := safeio.ReadRegular(store.lanePath(id), maxRecordBytes)
	if errors.Is(err, os.ErrNotExist) {
		return Lane{}, fail.New("LANE_NOT_FOUND", "lane not found: "+id)
	}
	if err != nil {
		return Lane{}, fail.Wrap("STORE_FAILED", err)
	}
	var lane Lane
	if err := strictjson.Decode(body, &lane); err != nil {
		return Lane{}, fail.Wrap("STORE_FAILED", err)
	}
	if err := store.validateLane(lane, id); err != nil {
		return Lane{}, err
	}
	return lane, nil
}

func (store Store) LoadLease(id string) (Lease, error) {
	registry, err := store.registryLock(0)
	if err != nil {
		return Lease{}, err
	}
	defer func() { _ = registry.Release() }()
	return store.loadLease(id)
}

func (store Store) loadLease(id string) (Lease, error) {
	if err := validateID(id, "lease"); err != nil {
		return Lease{}, err
	}
	body, err := safeio.ReadRegular(store.leasePath(id), maxRecordBytes)
	if errors.Is(err, os.ErrNotExist) {
		return Lease{}, fail.New("LEASE_NOT_FOUND", "lease not found: "+id)
	}
	if err != nil {
		return Lease{}, fail.Wrap("STORE_FAILED", err)
	}
	var lease Lease
	if err := strictjson.Decode(body, &lease); err != nil {
		return Lease{}, fail.Wrap("STORE_FAILED", err)
	}
	if err := store.validateLease(lease, id); err != nil {
		return Lease{}, err
	}
	return lease, nil
}

func (store Store) ActivePaths(id string) ([]string, error) {
	registry, err := store.registryLock(0)
	if err != nil {
		return nil, err
	}
	defer func() { _ = registry.Release() }()
	return store.activePathsLocked(id)
}

func (store Store) activePathsLocked(id string) ([]string, error) {
	leases, err := store.leases(id, true)
	if err != nil {
		return nil, err
	}
	var paths []string
	for _, lease := range leases {
		paths = append(paths, lease.Paths...)
	}
	sort.Strings(paths)
	return paths, nil
}

func (store Store) ValidateCapture(ctx context.Context, expected Lane, expectedPaths []string) error {
	registry, err := store.registryLock(0)
	if err != nil {
		return err
	}
	defer func() { _ = registry.Release() }()
	current, err := store.loadLane(expected.ID)
	if err != nil {
		return err
	}
	if err := store.validateCaptureIdentity(ctx, current, expected); err != nil {
		return err
	}
	leases, err := store.leases("", false)
	if err != nil {
		return err
	}
	if err := validateLaneLeaseLinks(current, leases); err != nil {
		return err
	}
	now := time.Now().UTC()
	if conflict := FindActiveLeaseConflict(leases, now); conflict != nil {
		return leaseConflictError(conflict)
	}
	actual, _ := activeLaneLeaseSet(leases, current.ID, now)
	want := append([]string(nil), expectedPaths...)
	sort.Strings(want)
	if !sameStrings(actual, want) {
		return fail.New("LEASE_MOVED", "active lease paths changed while capture was prepared")
	}
	return nil
}

// RefreshCaptureLease validates the complete capture fence and extends every
// active lease while the caller holds the lane lock. The registry lock prevents
// another lane from claiming an expired path between validation and renewal.
func (store Store) RefreshCaptureLease(ctx context.Context, expected Lane, expectedPaths []string) error {
	registry, err := store.registryLock(0)
	if err != nil {
		return err
	}
	defer func() { _ = registry.Release() }()
	current, err := store.loadLane(expected.ID)
	if err != nil {
		return err
	}
	if err := store.validateCaptureIdentity(ctx, current, expected); err != nil {
		return err
	}
	leases, err := store.leases("", false)
	if err != nil {
		return err
	}
	if err := validateLaneLeaseLinks(current, leases); err != nil {
		return err
	}
	now := time.Now().UTC()
	if conflict := FindActiveLeaseConflict(leases, now); conflict != nil {
		return leaseConflictError(conflict)
	}
	actual, active := activeLaneLeaseSet(leases, current.ID, now)
	want := append([]string(nil), expectedPaths...)
	sort.Strings(want)
	if !sameStrings(actual, want) {
		return fail.New("LEASE_MOVED", "active lease paths changed or expired while capture was running")
	}
	expires := now.Add(store.leaseTTL())
	for _, lease := range active {
		lease.UpdatedAt, lease.ExpiresAt = now, &expires
		if err := store.writeLease(lease); err != nil {
			return err
		}
	}
	return nil
}

func activeLaneLeaseSet(leases []Lease, laneID string, now time.Time) ([]string, []Lease) {
	var paths []string
	var active []Lease
	for _, lease := range leases {
		if lease.LaneID == laneID && activeLease(lease, now) {
			paths = append(paths, lease.Paths...)
			active = append(active, lease)
		}
	}
	sort.Strings(paths)
	return paths, active
}

func leaseConflictError(conflict *LeaseConflict) error {
	return fail.Errorf("PATH_LEASE_CONFLICT", "active lease %s path %q in lane %s overlaps lease %s path %q in lane %s", conflict.LeftLease, conflict.LeftPath, conflict.LeftLane, conflict.RightLease, conflict.RightPath, conflict.RightLane)
}

func validateLaneLeaseLinks(lane Lane, leases []Lease) error {
	owned := map[string]Lease{}
	for _, lease := range leases {
		if lease.LaneID != lane.ID {
			continue
		}
		if lease.Agent != lane.Agent || lease.Session != lane.Session || !contains(lane.LeaseIDs, lease.ID) {
			return fail.New("LEASE_MOVED", "lane lease ownership or reverse reference is inconsistent")
		}
		if lane.State == "active" || lane.State == "creating" {
			if lease.State == "released" {
				return fail.New("LANE_RELEASE_RECOVERY_REQUIRED", "lane release started but its manifest is still active; rerun release before other lane operations")
			}
		} else if lease.State == "active" {
			return fail.New("RELEASED_LANE_ACTIVE_LEASE", "released or aborted lane still has an active stored lease")
		}
		owned[lease.ID] = lease
	}
	for _, leaseID := range lane.LeaseIDs {
		if _, ok := owned[leaseID]; !ok {
			return fail.New("LEASE_MOVED", "lane references a missing or cross-lane lease")
		}
	}
	return nil
}

func validateClaimLeaseLinks(lane Lane, leases []Lease, requested []string) error {
	owned := map[string]Lease{}
	for _, lease := range leases {
		if lease.LaneID != lane.ID {
			continue
		}
		if lease.Agent != lane.Agent || lease.Session != lane.Session {
			return fail.New("LEASE_MOVED", "lane lease ownership is inconsistent")
		}
		if lease.State == "released" {
			return fail.New("LANE_RELEASE_RECOVERY_REQUIRED", "lane release started but its manifest is still active; rerun release before claiming paths")
		}
		if !contains(lane.LeaseIDs, lease.ID) && (lease.State != "active" || !sameStrings(lease.Paths, requested)) {
			return fail.New("LEASE_MOVED", "lane has an unlisted lease that does not match this claim retry")
		}
		owned[lease.ID] = lease
	}
	for _, leaseID := range lane.LeaseIDs {
		if _, ok := owned[leaseID]; !ok {
			return fail.New("LEASE_MOVED", "lane references a missing or cross-lane lease")
		}
	}
	return nil
}

type leasePathOwner struct {
	lane, lease, path, key string
}

type leasePathNode struct {
	children   map[string]*leasePathNode
	terminal   map[string]leasePathOwner
	descendant map[string]leasePathOwner
}

// FindActiveLeaseConflict returns one deterministic cross-lane overlap from a
// validated lease set. Same-lane scopes can overlap because one lane owns one
// capture fence.
func FindActiveLeaseConflict(leases []Lease, now time.Time) *LeaseConflict {
	var owners []leasePathOwner
	for _, lease := range leases {
		if !activeLease(lease, now) {
			continue
		}
		for _, path := range lease.Paths {
			owners = append(owners, leasePathOwner{lane: lease.LaneID, lease: lease.ID, path: path, key: pathid.Key(path)})
		}
	}
	sort.Slice(owners, func(left, right int) bool {
		if owners[left].key != owners[right].key {
			return owners[left].key < owners[right].key
		}
		if owners[left].lane != owners[right].lane {
			return owners[left].lane < owners[right].lane
		}
		return owners[left].lease < owners[right].lease
	})
	root := &leasePathNode{}
	for _, owner := range owners {
		if other, ok := root.findOverlap(owner); ok {
			return &LeaseConflict{LeftLane: other.lane, LeftLease: other.lease, LeftPath: other.path, RightLane: owner.lane, RightLease: owner.lease, RightPath: owner.path}
		}
		root.insert(owner)
	}
	return nil
}

func (node *leasePathNode) findOverlap(owner leasePathOwner) (leasePathOwner, bool) {
	current := node
	for _, component := range strings.Split(owner.key, "/") {
		if other, ok := otherLaneOwner(current.terminal, owner.lane); ok {
			return other, true
		}
		next := current.children[component]
		if next == nil {
			return leasePathOwner{}, false
		}
		current = next
	}
	if other, ok := otherLaneOwner(current.terminal, owner.lane); ok {
		return other, true
	}
	return otherLaneOwner(current.descendant, owner.lane)
}

func (node *leasePathNode) insert(owner leasePathOwner) {
	current := node
	if current.descendant == nil {
		current.descendant = map[string]leasePathOwner{}
	}
	current.descendant[owner.lane] = owner
	for _, component := range strings.Split(owner.key, "/") {
		if current.children == nil {
			current.children = map[string]*leasePathNode{}
		}
		next := current.children[component]
		if next == nil {
			next = &leasePathNode{}
			current.children[component] = next
		}
		current = next
		if current.descendant == nil {
			current.descendant = map[string]leasePathOwner{}
		}
		current.descendant[owner.lane] = owner
	}
	if current.terminal == nil {
		current.terminal = map[string]leasePathOwner{}
	}
	current.terminal[owner.lane] = owner
}

func otherLaneOwner(owners map[string]leasePathOwner, lane string) (leasePathOwner, bool) {
	var selected leasePathOwner
	found := false
	for ownerLane, owner := range owners {
		if ownerLane == lane {
			continue
		}
		if !found || owner.lane < selected.lane || owner.lane == selected.lane && (owner.lease < selected.lease || owner.lease == selected.lease && owner.path < selected.path) {
			selected, found = owner, true
		}
	}
	return selected, found
}

func (store Store) validateCaptureIdentity(ctx context.Context, current, expected Lane) error {
	if current.State != "active" || current.Agent != expected.Agent || current.Session != expected.Session || current.Mode != expected.Mode || current.Ref != expected.Ref || current.BaseSHA != expected.BaseSHA || current.CurrentSHA != expected.CurrentSHA || current.Worktree != expected.Worktree {
		return fail.New("LANE_MOVED", "lane identity or cursor changed while capture was prepared")
	}
	if store.Repo.Root != current.Worktree {
		return fail.New("WORKTREE_MISMATCH", "lane is bound to a different checkout")
	}
	head, err := store.Repo.Text(ctx, nil, "rev-parse", "HEAD")
	if err != nil || head != current.BaseSHA {
		return fail.New("SOURCE_HEAD_MOVED", "source HEAD moved from the lane base")
	}
	return nil
}

func (store Store) RecordCommit(ctx context.Context, id, commit string) error {
	registry, err := store.registryLock(0)
	if err != nil {
		return err
	}
	defer func() { _ = registry.Release() }()
	lane, err := store.loadLane(id)
	if err != nil {
		return err
	}
	if lane.CurrentSHA == commit {
		return nil
	}
	actual, err := store.Repo.Text(ctx, nil, "rev-parse", "--verify", lane.Ref+"^{commit}")
	if err != nil || actual != commit {
		return fail.New("CAPTURE_RECEIPT_MISMATCH", "lane ref does not match the captured commit")
	}
	parents, err := store.Repo.Lines(ctx, nil, "rev-list", "--first-parent", commit)
	if err != nil {
		return fail.Wrap("GIT_FAILED", err)
	}
	found := false
	for _, parent := range parents {
		if parent == lane.CurrentSHA {
			found = true
			break
		}
	}
	if !found {
		return fail.New("CAPTURE_RECEIPT_MISMATCH", "captured commit does not continue the lane first-parent chain")
	}
	lane.CurrentSHA, lane.LastCommit, lane.UpdatedAt = commit, commit, time.Now().UTC()
	return store.writeLane(lane)
}

func (store Store) Release(id, agent, session string, abort bool) error {
	lock, err := store.LaneLock(id, 0)
	if err != nil {
		return err
	}
	defer func() { _ = lock.Release() }()
	registry, err := store.registryLock(0)
	if err != nil {
		return err
	}
	defer func() { _ = registry.Release() }()
	lane, err := store.loadLane(id)
	if err != nil {
		return err
	}
	if lane.Agent != agent || lane.Session != session {
		return fail.New("IDENTITY_MISMATCH", "lane belongs to another agent or session")
	}
	now := time.Now().UTC()
	leases, err := store.leases("", false)
	if err != nil {
		return err
	}
	if err := validateReleaseLeaseLinks(lane, leases); err != nil {
		return err
	}
	for _, lease := range leases {
		if lease.LaneID != id || lease.State != "active" {
			continue
		}
		lease.State, lease.UpdatedAt, lease.ReleasedAt = "released", now, &now
		if err := store.writeLease(lease); err != nil {
			return err
		}
	}
	lane.State = "released"
	if abort {
		lane.State = "aborted"
	}
	lane.UpdatedAt = now
	return store.writeLane(lane)
}

func validateReleaseLeaseLinks(lane Lane, leases []Lease) error {
	owned := map[string]Lease{}
	for _, lease := range leases {
		if lease.LaneID != lane.ID {
			continue
		}
		if lease.Agent != lane.Agent || lease.Session != lane.Session || !contains(lane.LeaseIDs, lease.ID) {
			return fail.New("LEASE_MOVED", "lane lease ownership or reverse reference is inconsistent")
		}
		owned[lease.ID] = lease
	}
	for _, leaseID := range lane.LeaseIDs {
		if _, ok := owned[leaseID]; !ok {
			return fail.New("LEASE_MOVED", "lane references a missing or cross-lane lease")
		}
	}
	return nil
}

func (store Store) worktreeConflict(id, worktree string, mode Mode) (string, error) {
	entries, err := readRecordEntries(filepath.Join(store.Root, "lanes"))
	if err != nil {
		return "", fail.Wrap("STORE_FAILED", err)
	}
	for _, entry := range entries {
		fileID, entryErr := recordEntryID(entry, "lane")
		if entryErr != nil {
			return "", entryErr
		}
		lane, loadErr := store.loadLane(fileID)
		if loadErr != nil {
			return "", loadErr
		}
		if lane.ID != id && (lane.State == "active" || lane.State == "creating") && lane.Worktree == worktree && (mode == ModeWorktree || lane.Mode == ModeWorktree) {
			return lane.ID, nil
		}
	}
	return "", nil
}

func (store Store) leases(laneID string, activeOnly bool) ([]Lease, error) {
	entries, err := readRecordEntries(filepath.Join(store.Root, "leases"))
	if err != nil {
		return nil, fail.Wrap("STORE_FAILED", err)
	}
	now := time.Now().UTC()
	var leases []Lease
	for _, entry := range entries {
		fileID, entryErr := recordEntryID(entry, "lease")
		if entryErr != nil {
			return nil, entryErr
		}
		lease, loadErr := store.loadLease(fileID)
		if loadErr != nil {
			return nil, loadErr
		}
		if laneID != "" && lease.LaneID != laneID || activeOnly && !activeLease(lease, now) {
			continue
		}
		leases = append(leases, lease)
	}
	sort.Slice(leases, func(left, right int) bool {
		if leases[left].CreatedAt.Equal(leases[right].CreatedAt) {
			return leases[left].ID < leases[right].ID
		}
		return leases[left].CreatedAt.Before(leases[right].CreatedAt)
	})
	return leases, nil
}

func activeLease(lease Lease, now time.Time) bool {
	return lease.State == "active" && (lease.ExpiresAt == nil || now.Before(*lease.ExpiresAt))
}

func (store Store) leaseTTL() time.Duration {
	if store.LeaseTTL <= 0 {
		return DefaultLeaseTTL
	}
	return store.LeaseTTL
}

func readRecordEntries(path string) ([]os.DirEntry, error) {
	directory, err := os.Open(path)
	if err != nil {
		return nil, fail.Wrap("STORE_FAILED", err)
	}
	defer func() { _ = directory.Close() }()
	entries, err := directory.ReadDir(maxRecordEntries + 1)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, fail.Wrap("STORE_FAILED", err)
	}
	if len(entries) > maxRecordEntries {
		return nil, fail.Errorf("STORE_FAILED", "%s exceeds %d entries", path, maxRecordEntries)
	}
	sort.Slice(entries, func(left, right int) bool { return entries[left].Name() < entries[right].Name() })
	return entries, nil
}

func recordEntryID(entry os.DirEntry, kind string) (string, error) {
	if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 || !strings.HasSuffix(entry.Name(), ".json") {
		return "", fail.Errorf("STORE_FAILED", "%s record directory contains an unexpected entry: %s", kind, entry.Name())
	}
	id := strings.TrimSuffix(entry.Name(), ".json")
	if err := validateID(id, kind); err != nil {
		return "", fail.Wrap("STORE_FAILED", err)
	}
	return id, nil
}

func ownedActive(lane Lane, agent, session string) error {
	if lane.Agent != agent || lane.Session != session {
		return fail.New("IDENTITY_MISMATCH", "lane belongs to another agent or session")
	}
	if lane.State != "active" {
		return fail.New("LANE_NOT_ACTIVE", "lane is not active")
	}
	return nil
}

func sameCreation(left, right Lane) bool {
	return left.SchemaVersion == right.SchemaVersion && left.ID == right.ID && left.Agent == right.Agent && left.Session == right.Session && left.Mode == right.Mode && left.Ref == right.Ref && left.BaseRef == right.BaseRef && left.BaseSHA == right.BaseSHA && left.CurrentSHA == right.CurrentSHA && left.Worktree == right.Worktree
}

func sameStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func contains(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func validateID(value, label string) error {
	if !identifierPattern.MatchString(value) {
		return fail.Errorf("INVALID_ID", "%s must match %s", label, identifierPattern.String())
	}
	return nil
}

func (store Store) validateLane(lane Lane, id string) error {
	if lane.SchemaVersion != SchemaVersion {
		return fail.Errorf("MIGRATION_REQUIRED", "lane schema version %d is unsupported; this wip release supports version %d", lane.SchemaVersion, SchemaVersion)
	}
	if lane.ID != id {
		return fail.New("STORE_FAILED", "lane manifest identity is invalid")
	}
	for label, value := range map[string]string{"lane": lane.ID, "agent": lane.Agent, "session": lane.Session} {
		if err := validateID(value, label); err != nil {
			return fail.Wrap("STORE_FAILED", err)
		}
	}
	if lane.Mode != ModeShared && lane.Mode != ModeWorktree || lane.Ref != LaneRef(lane.Agent, lane.ID) {
		return fail.New("STORE_FAILED", "lane mode or ref is invalid")
	}
	if !objectID(lane.BaseSHA) || !objectID(lane.CurrentSHA) || strings.TrimSpace(lane.BaseRef) == "" || lane.LastCommit != "" && (!objectID(lane.LastCommit) || lane.LastCommit != lane.CurrentSHA) {
		return fail.New("STORE_FAILED", "lane commit identity is invalid")
	}
	if lane.State != "creating" && lane.State != "active" && lane.State != "released" && lane.State != "aborted" {
		return fail.New("STORE_FAILED", "lane state is invalid")
	}
	if lane.Worktree == "" || !filepath.IsAbs(lane.Worktree) || filepath.Clean(lane.Worktree) != lane.Worktree || lane.CreatedAt.IsZero() || lane.UpdatedAt.IsZero() {
		return fail.New("STORE_FAILED", "lane path or timestamps are invalid")
	}
	seen := map[string]bool{}
	for _, leaseID := range lane.LeaseIDs {
		if err := validateID(leaseID, "lease"); err != nil || seen[leaseID] {
			return fail.New("STORE_FAILED", "lane lease id list is invalid")
		}
		seen[leaseID] = true
	}
	return nil
}

func (store Store) validateLease(lease Lease, id string) error {
	if lease.SchemaVersion != SchemaVersion {
		return fail.Errorf("MIGRATION_REQUIRED", "lease schema version %d is unsupported; this wip release supports version %d", lease.SchemaVersion, SchemaVersion)
	}
	if lease.ID != id {
		return fail.New("STORE_FAILED", "lease identity is invalid")
	}
	for label, value := range map[string]string{"lease": lease.ID, "lane": lease.LaneID, "agent": lease.Agent, "session": lease.Session} {
		if err := validateID(value, label); err != nil {
			return fail.Wrap("STORE_FAILED", err)
		}
	}
	if lease.State != "active" && lease.State != "released" || lease.CreatedAt.IsZero() || lease.UpdatedAt.IsZero() || lease.ExpiresAt == nil || lease.State == "active" && lease.ReleasedAt != nil || lease.State == "released" && lease.ReleasedAt == nil {
		return fail.New("STORE_FAILED", "lease state or timestamps are invalid")
	}
	paths, err := store.Repo.NormalizePaths(lease.Paths)
	if err != nil || len(paths) == 0 || !sameStrings(paths, lease.Paths) {
		return fail.New("STORE_FAILED", "lease path set is invalid or not canonical")
	}
	return nil
}

func objectID(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	for _, character := range value {
		if character < '0' || character > '9' {
			if character < 'a' || character > 'f' {
				return false
			}
		}
	}
	return true
}

func (store Store) lanePath(id string) string { return filepath.Join(store.Root, "lanes", id+".json") }
func (store Store) leasePath(id string) string {
	return filepath.Join(store.Root, "leases", id+".json")
}

func (store Store) writeLane(lane Lane) error {
	if err := store.validateLane(lane, lane.ID); err != nil {
		return err
	}
	if err := validateLaneRecordCapacity(lane); err != nil {
		return err
	}
	body, err := marshalLaneRecord(lane)
	if err != nil {
		return err
	}
	return fail.Wrap("STORE_FAILED", atomicfile.WriteWithTempDir(store.lanePath(lane.ID), filepath.Join(store.Root, "locks"), body, 0o600))
}

func (store Store) writeLease(lease Lease) error {
	if err := store.validateLease(lease, lease.ID); err != nil {
		return err
	}
	if err := validateLeaseRecordCapacity(lease); err != nil {
		return err
	}
	body, err := marshalLeaseRecord(lease)
	if err != nil {
		return err
	}
	return fail.Wrap("STORE_FAILED", atomicfile.WriteWithTempDir(store.leasePath(lease.ID), filepath.Join(store.Root, "locks"), body, 0o600))
}

func (store Store) createLease(lease Lease) error {
	if err := store.validateLease(lease, lease.ID); err != nil {
		return err
	}
	if err := validateLeaseRecordCapacity(lease); err != nil {
		return err
	}
	body, err := marshalLeaseRecord(lease)
	if err != nil {
		return err
	}
	err = atomicfile.CreateWithTempDir(store.leasePath(lease.ID), filepath.Join(store.Root, "locks"), body, 0o600)
	if errors.Is(err, atomicfile.ErrExists) {
		return atomicfile.ErrExists
	}
	return fail.Wrap("STORE_FAILED", err)
}

func marshalLaneRecord(lane Lane) ([]byte, error) {
	return recordjson.Marshal(lane, maxRecordBytes, "STORE_FAILED", "lane manifest")
}

func validateLaneRecordCapacity(lane Lane) error {
	maximum := lane
	maximum.State = "released"
	maximum.CreatedAt = maximumStoreRecordTimestamp()
	maximum.UpdatedAt = maximum.CreatedAt
	if maximum.LastCommit == "" {
		maximum.LastCommit = maximum.CurrentSHA
	}
	_, err := marshalLaneRecord(maximum)
	return err
}

func marshalLeaseRecord(lease Lease) ([]byte, error) {
	return recordjson.Marshal(lease, maxRecordBytes, "STORE_FAILED", "lease record")
}

func validateLeaseRecordCapacity(lease Lease) error {
	maximum := lease
	maximum.State = "released"
	maximum.CreatedAt = maximumStoreRecordTimestamp()
	maximum.UpdatedAt = maximum.CreatedAt
	maximum.ExpiresAt = &maximum.CreatedAt
	maximum.ReleasedAt = &maximum.CreatedAt
	_, err := marshalLeaseRecord(maximum)
	return err
}

func maximumStoreRecordTimestamp() time.Time {
	return time.Date(2000, time.December, 31, 23, 59, 59, 123456789, time.UTC)
}

func newLeaseID() (string, error) {
	var entropy [16]byte
	if _, err := rand.Read(entropy[:]); err != nil {
		return "", fail.Wrap("STORE_FAILED", err)
	}
	return fmt.Sprintf("lease-%x", entropy[:]), nil
}
