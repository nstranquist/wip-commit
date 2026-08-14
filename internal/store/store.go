package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/nstranquist/wip-commit/internal/atomicfile"
	"github.com/nstranquist/wip-commit/internal/fail"
	"github.com/nstranquist/wip-commit/internal/filelock"
	"github.com/nstranquist/wip-commit/internal/gitx"
	"github.com/nstranquist/wip-commit/internal/pathid"
	"github.com/nstranquist/wip-commit/internal/safeio"
	"github.com/nstranquist/wip-commit/internal/strictjson"
)

const (
	SchemaVersion   = 1
	DefaultLeaseTTL = 15 * time.Minute
	maxRecordBytes  = 1 << 20
)

var identifierPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

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

type Status struct {
	Lane   Lane    `json:"lane"`
	Leases []Lease `json:"leases"`
}

type CreateOptions struct {
	ID, Agent, Session, BaseRef, Worktree string
	Mode                                  Mode
}

type Store struct {
	Repo gitx.Repo
	Root string
}

func Open(repo gitx.Repo) (Store, error) {
	root := filepath.Join(repo.CommonDir, "wip", "v1")
	for _, directory := range []string{"lanes", "leases", "locks", "intents", "profiles"} {
		path := filepath.Join(root, directory)
		if err := os.MkdirAll(path, 0o700); err != nil {
			return Store{}, fail.Wrap("STORE_FAILED", err)
		}
		if err := os.Chmod(path, 0o700); err != nil {
			return Store{}, fail.Wrap("STORE_FAILED", err)
		}
	}
	return Store{Repo: repo, Root: root}, nil
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
	defer lock.Release()
	registry, registryErr := store.registryLock(0)
	if registryErr != nil {
		return Lane{}, registryErr
	}
	defer registry.Release()
	if conflict, conflictErr := store.worktreeConflict(options.ID, worktree, options.Mode); conflictErr != nil {
		return Lane{}, conflictErr
	} else if conflict != "" {
		return Lane{}, fail.Errorf("WORKTREE_CONFLICT", "worktree is already bound to incompatible lane %s", conflict)
	}
	candidate := Lane{SchemaVersion: SchemaVersion, ID: options.ID, Agent: options.Agent, Session: options.Session, Mode: options.Mode, Ref: ref, BaseRef: options.BaseRef, BaseSHA: base, CurrentSHA: base, Worktree: worktree, State: "creating"}
	if _, statErr := os.Stat(store.lanePath(options.ID)); statErr == nil {
		existing, loadErr := store.Load(options.ID)
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
	defer lock.Release()
	lane, err := store.Load(id)
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
	defer registry.Release()
	active, err := store.leases("", true)
	if err != nil {
		return Lease{}, err
	}
	for _, existing := range active {
		if existing.LaneID == id {
			if sameStrings(existing.Paths, normalized) {
				if !contains(lane.LeaseIDs, existing.ID) {
					lane.LeaseIDs = append(lane.LeaseIDs, existing.ID)
					lane.UpdatedAt = time.Now().UTC()
					if err := store.writeLane(lane); err != nil {
						return Lease{}, err
					}
				}
				return existing, nil
			}
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
	now := time.Now().UTC()
	expires := now.Add(DefaultLeaseTTL)
	lease := Lease{SchemaVersion: SchemaVersion, ID: fmt.Sprintf("lease-%d-%d", now.UnixNano(), os.Getpid()), LaneID: id, Agent: agent, Session: session, Paths: normalized, State: "active", CreatedAt: now, UpdatedAt: now, ExpiresAt: &expires}
	if err := store.writeLease(lease); err != nil {
		return Lease{}, err
	}
	lane.LeaseIDs = append(lane.LeaseIDs, lease.ID)
	lane.UpdatedAt = now
	if err := store.writeLane(lane); err != nil {
		return Lease{}, err
	}
	return lease, nil
}

func (store Store) Renew(id, agent, session string) ([]Lease, error) {
	lock, err := store.LaneLock(id, 0)
	if err != nil {
		return nil, err
	}
	defer lock.Release()
	lane, err := store.Load(id)
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
	defer registry.Release()
	leases, err := store.leases(id, false)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	var renewed []Lease
	for _, lease := range leases {
		if !activeLease(lease, now) {
			continue
		}
		expires := now.Add(DefaultLeaseTTL)
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
	entries, err := os.ReadDir(filepath.Join(store.Root, "lanes"))
	if err != nil {
		return Status{}, fail.Wrap("STORE_FAILED", err)
	}
	var matches []Lane
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		lane, loadErr := store.Load(strings.TrimSuffix(entry.Name(), ".json"))
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
	return store.Status(matches[0].ID)
}

func (store Store) Status(id string) (Status, error) {
	lane, err := store.Load(id)
	if err != nil {
		return Status{}, err
	}
	leases, err := store.leases(id, false)
	return Status{Lane: lane, Leases: leases}, err
}

func (store Store) Load(id string) (Lane, error) {
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

func (store Store) ActivePaths(id string) ([]string, error) {
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
	current, err := store.Load(expected.ID)
	if err != nil {
		return err
	}
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
	actual, err := store.ActivePaths(current.ID)
	if err != nil {
		return err
	}
	want := append([]string(nil), expectedPaths...)
	sort.Strings(want)
	if !sameStrings(actual, want) {
		return fail.New("LEASE_MOVED", "active lease paths changed while capture was prepared")
	}
	return nil
}

func (store Store) RecordCommit(ctx context.Context, id, commit string) error {
	lane, err := store.Load(id)
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
	defer lock.Release()
	registry, err := store.registryLock(0)
	if err != nil {
		return err
	}
	defer registry.Release()
	lane, err := store.Load(id)
	if err != nil {
		return err
	}
	if lane.Agent != agent || lane.Session != session {
		return fail.New("IDENTITY_MISMATCH", "lane belongs to another agent or session")
	}
	now := time.Now().UTC()
	leases, err := store.leases(id, false)
	if err != nil {
		return err
	}
	for _, lease := range leases {
		if lease.State != "active" {
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

func (store Store) worktreeConflict(id, worktree string, mode Mode) (string, error) {
	entries, err := os.ReadDir(filepath.Join(store.Root, "lanes"))
	if err != nil {
		return "", fail.Wrap("STORE_FAILED", err)
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		lane, loadErr := store.Load(strings.TrimSuffix(entry.Name(), ".json"))
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
	entries, err := os.ReadDir(filepath.Join(store.Root, "leases"))
	if err != nil {
		return nil, fail.Wrap("STORE_FAILED", err)
	}
	now := time.Now().UTC()
	var leases []Lease
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		body, readErr := safeio.ReadRegular(filepath.Join(store.Root, "leases", entry.Name()), maxRecordBytes)
		if readErr != nil {
			return nil, fail.Wrap("STORE_FAILED", readErr)
		}
		var lease Lease
		if err := strictjson.Decode(body, &lease); err != nil {
			return nil, fail.Wrap("STORE_FAILED", err)
		}
		fileID := strings.TrimSuffix(entry.Name(), ".json")
		if err := store.validateLease(lease, fileID); err != nil {
			return nil, err
		}
		if laneID != "" && lease.LaneID != laneID || activeOnly && !activeLease(lease, now) {
			continue
		}
		leases = append(leases, lease)
	}
	sort.Slice(leases, func(left, right int) bool { return leases[left].CreatedAt.Before(leases[right].CreatedAt) })
	return leases, nil
}

func activeLease(lease Lease, now time.Time) bool {
	return lease.State == "active" && (lease.ExpiresAt == nil || now.Before(*lease.ExpiresAt))
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
	if lane.SchemaVersion != SchemaVersion || lane.ID != id {
		return fail.New("STORE_FAILED", "lane manifest identity or schema is invalid")
	}
	for label, value := range map[string]string{"lane": lane.ID, "agent": lane.Agent, "session": lane.Session} {
		if err := validateID(value, label); err != nil {
			return fail.Wrap("STORE_FAILED", err)
		}
	}
	if lane.Mode != ModeShared && lane.Mode != ModeWorktree || lane.Ref != LaneRef(lane.Agent, lane.ID) {
		return fail.New("STORE_FAILED", "lane mode or ref is invalid")
	}
	if !objectID(lane.BaseSHA) || !objectID(lane.CurrentSHA) || strings.TrimSpace(lane.BaseRef) == "" {
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
	if lease.SchemaVersion != SchemaVersion || lease.ID != id {
		return fail.New("STORE_FAILED", "lease identity or schema is invalid")
	}
	for label, value := range map[string]string{"lease": lease.ID, "lane": lease.LaneID, "agent": lease.Agent, "session": lease.Session} {
		if err := validateID(value, label); err != nil {
			return fail.Wrap("STORE_FAILED", err)
		}
	}
	if lease.State != "active" && lease.State != "released" || lease.CreatedAt.IsZero() || lease.UpdatedAt.IsZero() || lease.ExpiresAt == nil {
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
	body, err := json.MarshalIndent(lane, "", "  ")
	if err != nil {
		return fail.Wrap("STORE_FAILED", err)
	}
	return fail.Wrap("STORE_FAILED", atomicfile.Write(store.lanePath(lane.ID), append(body, '\n'), 0o600))
}

func (store Store) writeLease(lease Lease) error {
	if err := store.validateLease(lease, lease.ID); err != nil {
		return err
	}
	body, err := json.MarshalIndent(lease, "", "  ")
	if err != nil {
		return fail.Wrap("STORE_FAILED", err)
	}
	return fail.Wrap("STORE_FAILED", atomicfile.Write(store.leasePath(lease.ID), append(body, '\n'), 0o600))
}
