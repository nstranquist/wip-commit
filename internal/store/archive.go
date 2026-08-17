package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/nstranquist/wip-commit/internal/atomicfile"
	"github.com/nstranquist/wip-commit/internal/fail"
	"github.com/nstranquist/wip-commit/internal/filelock"
	"github.com/nstranquist/wip-commit/internal/recordjson"
	"github.com/nstranquist/wip-commit/internal/safeio"
	"github.com/nstranquist/wip-commit/internal/strictjson"
)

const (
	archiveSchemaVersion = 1
	maxArchiveLanes      = 1_000
)

type ArchiveCandidate struct {
	LaneID     string    `json:"lane_id"`
	State      string    `json:"state"`
	Ref        string    `json:"ref"`
	Commit     string    `json:"commit"`
	UpdatedAt  time.Time `json:"updated_at"`
	LeaseCount int       `json:"lease_count"`
}

type ArchiveReceipt struct {
	SchemaVersion int                `json:"schema_version"`
	ID            string             `json:"id"`
	Digest        string             `json:"digest"`
	State         string             `json:"state"`
	Before        time.Time          `json:"before"`
	Candidates    []ArchiveCandidate `json:"candidates"`
	Files         []string           `json:"files"`
	CreatedAt     time.Time          `json:"created_at"`
	UpdatedAt     time.Time          `json:"updated_at"`
}

func (store Store) ArchiveCandidates(before time.Time) ([]ArchiveCandidate, error) {
	if before.IsZero() {
		return nil, fail.New("INVALID_ARGS", "archive cutoff cannot be zero")
	}
	registry, err := store.registryLock(0)
	if err != nil {
		return nil, err
	}
	defer func() { _ = registry.Release() }()
	entries, err := readRecordEntries(filepath.Join(store.Root, "lanes"))
	if err != nil {
		return nil, err
	}
	var candidates []ArchiveCandidate
	for _, entry := range entries {
		if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 || !strings.HasSuffix(entry.Name(), ".json") {
			return nil, fail.New("ARCHIVE_REFUSED", "lane record directory contains an unexpected entry: "+entry.Name())
		}
		id := strings.TrimSuffix(entry.Name(), ".json")
		candidate, eligible, err := store.archiveCandidateLocked(id, before)
		if err != nil {
			return nil, err
		}
		if !eligible {
			continue
		}
		candidates = append(candidates, candidate)
	}
	sort.Slice(candidates, func(left, right int) bool { return candidates[left].LaneID < candidates[right].LaneID })
	if len(candidates) > maxArchiveLanes {
		return nil, fail.Errorf("ARCHIVE_REFUSED", "archive plan exceeds %d lanes; use a narrower cutoff or explicit lane list", maxArchiveLanes)
	}
	return candidates, nil
}

func (store Store) archiveCandidateLocked(id string, before time.Time) (ArchiveCandidate, bool, error) {
	lane, err := store.loadLane(id)
	if err != nil {
		return ArchiveCandidate{}, false, err
	}
	if lane.State != "released" && lane.State != "aborted" || !lane.UpdatedAt.Before(before) {
		return ArchiveCandidate{}, false, nil
	}
	leases, err := store.leases(id, false)
	if err != nil {
		return ArchiveCandidate{}, false, err
	}
	if err := validateLaneLeaseLinks(lane, leases); err != nil {
		return ArchiveCandidate{}, false, err
	}
	for _, lease := range leases {
		if lease.State == "active" {
			return ArchiveCandidate{}, false, fail.Errorf("ARCHIVE_REFUSED", "released lane %s still has active stored lease %s", id, lease.ID)
		}
	}
	return ArchiveCandidate{LaneID: id, State: lane.State, Ref: lane.Ref, Commit: lane.CurrentSHA, UpdatedAt: lane.UpdatedAt, LeaseCount: len(leases)}, true, nil
}

// Archive applies the exact candidate records from a reviewed preview. The
// records are compared again while every selected lane and the lease registry
// are locked.
func (store Store) Archive(ctx context.Context, before time.Time, expected []ArchiveCandidate) (ArchiveReceipt, error) {
	if err := validateArchiveCandidates(expected, before); err != nil {
		return ArchiveReceipt{}, err
	}
	archiveLock, err := filelock.Acquire(filepath.Join(store.Root, "locks", "archive.lock"), 0)
	if err != nil {
		return ArchiveReceipt{}, fail.Wrap("LOCK_TIMEOUT", err)
	}
	defer func() { _ = archiveLock.Release() }()
	return store.archiveLocked(ctx, ArchiveReceipt{Before: before.UTC(), Candidates: append([]ArchiveCandidate(nil), expected...)}, false)
}

// ResumeArchive continues only the immutable prepared receipt named by id.
func (store Store) ResumeArchive(ctx context.Context, id string) (ArchiveReceipt, error) {
	if err := validateID(id, "archive"); err != nil {
		return ArchiveReceipt{}, err
	}
	archiveLock, err := filelock.Acquire(filepath.Join(store.Root, "locks", "archive.lock"), 0)
	if err != nil {
		return ArchiveReceipt{}, fail.Wrap("LOCK_TIMEOUT", err)
	}
	defer func() { _ = archiveLock.Release() }()
	receipt, err := loadArchiveReceipt(store.Root, id)
	if err != nil {
		return ArchiveReceipt{}, err
	}
	if err := ensureArchiveBatch(store.Root, id, false); err != nil {
		return ArchiveReceipt{}, err
	}
	if err := store.ValidateArchiveFiles(receipt); err != nil {
		return ArchiveReceipt{}, err
	}
	if receipt.State == "complete" {
		return receipt, nil
	}
	if receipt.State != "prepared" {
		return ArchiveReceipt{}, fail.New("ARCHIVE_REFUSED", "only a prepared archive receipt can resume")
	}
	return store.archiveLocked(ctx, receipt, true)
}

func (store Store) archiveLocked(ctx context.Context, receipt ArchiveReceipt, resume bool) (ArchiveReceipt, error) {
	locks := make([]*filelock.Lock, 0, len(receipt.Candidates))
	for _, candidate := range receipt.Candidates {
		lock, err := store.LaneLock(candidate.LaneID, 0)
		if err != nil {
			for index := len(locks) - 1; index >= 0; index-- {
				_ = locks[index].Release()
			}
			return ArchiveReceipt{}, err
		}
		locks = append(locks, lock)
	}
	defer func() {
		for index := len(locks) - 1; index >= 0; index-- {
			_ = locks[index].Release()
		}
	}()
	registry, err := store.registryLock(0)
	if err != nil {
		return ArchiveReceipt{}, err
	}
	defer func() { _ = registry.Release() }()
	if !resume {
		// Rebuild only the exact reviewed records under every relevant lock.
		for _, candidate := range receipt.Candidates {
			fresh, eligible, err := store.archiveCandidateLocked(candidate.LaneID, receipt.Before)
			if err != nil {
				return ArchiveReceipt{}, err
			}
			if !eligible || !sameArchiveCandidate(fresh, candidate) {
				return ArchiveReceipt{}, fail.New("ARCHIVE_PLAN_MOVED", "archive candidate changed after preview: "+candidate.LaneID)
			}
		}
		receipt, err = store.prepareArchiveReceipt(receipt.Before, receipt.Candidates)
		if err != nil {
			return ArchiveReceipt{}, err
		}
	}
	if err := store.ValidateArchiveFiles(receipt); err != nil {
		return receipt, err
	}
	for _, candidate := range receipt.Candidates {
		actual, refErr := store.Repo.Text(ctx, nil, "rev-parse", "--verify", candidate.Ref+"^{commit}")
		if refErr != nil || actual != candidate.Commit {
			return ArchiveReceipt{}, fail.New("ARCHIVE_REFUSED", "lane ref is missing or does not match its durable cursor: "+candidate.LaneID)
		}
	}
	root, err := os.OpenRoot(store.Root)
	if err != nil {
		return ArchiveReceipt{}, fail.Wrap("ARCHIVE_FAILED", err)
	}
	defer func() { _ = root.Close() }()
	files := append([]string(nil), receipt.Files...)
	sort.SliceStable(files, func(left, right int) bool {
		// Remove the lane manifest last so read-only commands cannot observe
		// a live lane without its support records.
		return !strings.HasPrefix(files[left], "lanes/") && strings.HasPrefix(files[right], "lanes/")
	})
	for _, relative := range files {
		if err := moveArchiveFile(root, receipt.ID, relative, false); err != nil {
			return receipt, err
		}
	}
	receipt.State, receipt.UpdatedAt = "complete", time.Now().UTC()
	if err := writeArchiveReceipt(store.Root, receipt); err != nil {
		return receipt, err
	}
	return receipt, nil
}

func validateArchiveCandidates(candidates []ArchiveCandidate, before time.Time) error {
	if before.IsZero() || len(candidates) == 0 || len(candidates) > maxArchiveLanes {
		return fail.New("ARCHIVE_EMPTY", "archive requires an exact non-empty reviewed candidate set")
	}
	last := ""
	for _, candidate := range candidates {
		if err := validateID(candidate.LaneID, "lane"); err != nil {
			return err
		}
		if candidate.LaneID <= last || candidate.State != "released" && candidate.State != "aborted" || !validArchiveRef(candidate.Ref, candidate.LaneID) || !objectID(candidate.Commit) || candidate.UpdatedAt.IsZero() || !candidate.UpdatedAt.Before(before) || candidate.LeaseCount < 0 {
			return fail.New("ARCHIVE_REFUSED", "archive candidate set is invalid or not canonical")
		}
		last = candidate.LaneID
	}
	return nil
}

func sameArchiveCandidate(left, right ArchiveCandidate) bool {
	return left.LaneID == right.LaneID && left.State == right.State && left.Ref == right.Ref && left.Commit == right.Commit && left.UpdatedAt.Equal(right.UpdatedAt) && left.LeaseCount == right.LeaseCount
}

func (store Store) RestoreArchive(ctx context.Context, id string) (ArchiveReceipt, error) {
	if err := validateID(id, "archive"); err != nil {
		return ArchiveReceipt{}, err
	}
	archiveLock, err := filelock.Acquire(filepath.Join(store.Root, "locks", "archive.lock"), 0)
	if err != nil {
		return ArchiveReceipt{}, fail.Wrap("LOCK_TIMEOUT", err)
	}
	defer func() { _ = archiveLock.Release() }()
	receipt, err := loadArchiveReceipt(store.Root, id)
	if err != nil {
		return ArchiveReceipt{}, err
	}
	if err := store.ValidateArchiveFiles(receipt); err != nil {
		return ArchiveReceipt{}, err
	}
	if receipt.State == "restored" {
		return receipt, nil
	}
	if receipt.State != "complete" && receipt.State != "restoring" {
		return ArchiveReceipt{}, fail.New("ARCHIVE_REFUSED", "only a complete or restoring archive receipt can restore")
	}
	locks := make([]*filelock.Lock, 0, len(receipt.Candidates))
	for _, candidate := range receipt.Candidates {
		lock, err := store.LaneLock(candidate.LaneID, 0)
		if err != nil {
			for index := len(locks) - 1; index >= 0; index-- {
				_ = locks[index].Release()
			}
			return ArchiveReceipt{}, err
		}
		locks = append(locks, lock)
	}
	defer func() {
		for index := len(locks) - 1; index >= 0; index-- {
			_ = locks[index].Release()
		}
	}()
	registry, err := store.registryLock(0)
	if err != nil {
		return ArchiveReceipt{}, err
	}
	defer func() { _ = registry.Release() }()
	for _, candidate := range receipt.Candidates {
		actual, refErr := store.Repo.Text(ctx, nil, "rev-parse", "--verify", candidate.Ref+"^{commit}")
		if refErr != nil || actual != candidate.Commit {
			return ArchiveReceipt{}, fail.New("ARCHIVE_REFUSED", "lane ref changed after archival: "+candidate.LaneID)
		}
	}
	if receipt.State == "complete" {
		receipt.State, receipt.UpdatedAt = "restoring", time.Now().UTC()
		if err := writeArchiveReceipt(store.Root, receipt); err != nil {
			return receipt, err
		}
	}
	root, err := os.OpenRoot(store.Root)
	if err != nil {
		return ArchiveReceipt{}, fail.Wrap("ARCHIVE_FAILED", err)
	}
	defer func() { _ = root.Close() }()
	files := append([]string(nil), receipt.Files...)
	sort.SliceStable(files, func(left, right int) bool {
		// Restore lane manifests last so readers cannot observe partial support files.
		return !strings.HasPrefix(files[left], "lanes/") && strings.HasPrefix(files[right], "lanes/")
	})
	for _, relative := range files {
		if err := moveArchiveFile(root, receipt.ID, relative, true); err != nil {
			return receipt, err
		}
	}
	receipt.State, receipt.UpdatedAt = "restored", time.Now().UTC()
	if err := writeArchiveReceipt(store.Root, receipt); err != nil {
		return receipt, err
	}
	return receipt, nil
}

func (store Store) prepareArchiveReceipt(before time.Time, candidates []ArchiveCandidate) (ArchiveReceipt, error) {
	receipt := ArchiveReceipt{SchemaVersion: archiveSchemaVersion, State: "prepared", Before: before.UTC(), Candidates: append([]ArchiveCandidate(nil), candidates...)}
	for _, candidate := range candidates {
		receipt.Files = append(receipt.Files, filepath.ToSlash(filepath.Join("lanes", candidate.LaneID+".json")))
		leases, err := store.leases(candidate.LaneID, false)
		if err != nil {
			return ArchiveReceipt{}, err
		}
		for _, lease := range leases {
			receipt.Files = append(receipt.Files, filepath.ToSlash(filepath.Join("leases", lease.ID+".json")))
		}
		profile := filepath.Join(store.Root, "profiles", candidate.LaneID+".json")
		if _, err := safeio.ReadRegular(profile, maxRecordBytes); err == nil {
			receipt.Files = append(receipt.Files, filepath.ToSlash(filepath.Join("profiles", candidate.LaneID+".json")))
		} else if !errors.Is(err, os.ErrNotExist) {
			return ArchiveReceipt{}, fail.Wrap("ARCHIVE_FAILED", err)
		}
	}
	sort.Strings(receipt.Files)
	digest, err := digestArchiveReceipt(receipt)
	if err != nil {
		return ArchiveReceipt{}, err
	}
	receipt.Digest = digest
	receipt.ID = "archive-" + strings.TrimPrefix(digest, "sha256:")[:24]
	if existing, err := loadArchiveReceipt(store.Root, receipt.ID); err == nil {
		if existing.Digest != receipt.Digest || existing.State == "restoring" || existing.State == "restored" {
			return ArchiveReceipt{}, fail.New("ARCHIVE_CONFLICT", "existing archive receipt is incompatible with this plan")
		}
		if err := ensureArchiveBatch(store.Root, receipt.ID, false); err != nil {
			return ArchiveReceipt{}, err
		}
		return existing, nil
	} else if fail.Code(err) != "ARCHIVE_NOT_FOUND" {
		return ArchiveReceipt{}, err
	}
	now := time.Now().UTC()
	receipt.CreatedAt, receipt.UpdatedAt = now, now
	if err := validateArchiveReceiptCapacity(receipt); err != nil {
		return ArchiveReceipt{}, err
	}
	if err := ensureArchiveBatch(store.Root, receipt.ID, true); err != nil {
		return ArchiveReceipt{}, err
	}
	if err := writeArchiveReceipt(store.Root, receipt); err != nil {
		return ArchiveReceipt{}, err
	}
	return receipt, nil
}

func ensureArchiveBatch(stateRoot, id string, requireEmpty bool) error {
	root, err := os.OpenRoot(stateRoot)
	if err != nil {
		return fail.Wrap("ARCHIVE_FAILED", err)
	}
	defer func() { _ = root.Close() }()
	batch := filepath.ToSlash(filepath.Join("archive", id))
	info, err := root.Lstat(batch)
	if errors.Is(err, os.ErrNotExist) {
		if err := root.Mkdir(batch, 0o700); err != nil {
			return fail.Wrap("ARCHIVE_FAILED", err)
		}
		info, err = root.Lstat(batch)
	}
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		if err != nil {
			return fail.Wrap("ARCHIVE_FAILED", err)
		}
		return fail.New("ARCHIVE_FAILED", "archive batch is not a regular directory: "+id)
	}
	if err := root.Chmod(batch, 0o700); err != nil {
		return fail.Wrap("ARCHIVE_FAILED", err)
	}
	batchEntries, err := rootDirectoryEntries(root, batch, 5)
	if err != nil {
		return err
	}
	for _, entry := range batchEntries {
		if entry.Name() == "receipt.json" && !requireEmpty && !entry.IsDir() && entry.Type()&os.ModeSymlink == 0 {
			continue
		}
		if entry.Name() != "lanes" && entry.Name() != "leases" && entry.Name() != "profiles" {
			return fail.New("ARCHIVE_CONFLICT", "archive batch contains an unexpected entry: "+entry.Name())
		}
	}
	for _, directory := range []string{"lanes", "leases", "profiles"} {
		path := batch + "/" + directory
		info, statErr := root.Lstat(path)
		if errors.Is(statErr, os.ErrNotExist) {
			if mkdirErr := root.Mkdir(path, 0o700); mkdirErr != nil {
				return fail.Wrap("ARCHIVE_FAILED", mkdirErr)
			}
			info, statErr = root.Lstat(path)
		}
		if statErr != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			if statErr != nil {
				return fail.Wrap("ARCHIVE_FAILED", statErr)
			}
			return fail.New("ARCHIVE_CONFLICT", "archive record directory is not a regular directory: "+path)
		}
		if requireEmpty {
			entries, readErr := rootDirectoryEntries(root, path, 1)
			if readErr != nil {
				return readErr
			}
			if len(entries) != 0 {
				return fail.New("ARCHIVE_CONFLICT", "receipt-free archive batch contains record files: "+path)
			}
		}
	}
	return nil
}

func rootDirectoryEntries(root *os.Root, relative string, limit int) ([]os.DirEntry, error) {
	directory, err := root.Open(filepath.FromSlash(relative))
	if err != nil {
		return nil, fail.Wrap("ARCHIVE_FAILED", err)
	}
	defer func() { _ = directory.Close() }()
	entries, err := directory.ReadDir(limit + 1)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, fail.Wrap("ARCHIVE_FAILED", err)
	}
	if len(entries) > limit {
		return nil, fail.New("ARCHIVE_CONFLICT", "archive directory exceeds its entry limit: "+relative)
	}
	return entries, nil
}

func moveArchiveFile(root *os.Root, id, relative string, restore bool) error {
	relative = filepath.ToSlash(relative)
	archive := filepath.ToSlash(filepath.Join("archive", id, filepath.FromSlash(relative)))
	source, destination := relative, archive
	if restore {
		source, destination = archive, relative
	}
	if _, err := root.Lstat(destination); err == nil {
		if _, sourceErr := root.Lstat(source); errors.Is(sourceErr, os.ErrNotExist) {
			return nil
		}
		return fail.New("ARCHIVE_CONFLICT", "archive source and destination both exist: "+relative)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fail.Wrap("ARCHIVE_FAILED", err)
	}
	info, err := root.Lstat(source)
	if err != nil {
		return fail.Wrap("ARCHIVE_FAILED", err)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return fail.New("ARCHIVE_FAILED", "archive record is not a regular file: "+relative)
	}
	if err := root.Rename(source, destination); err != nil {
		return fail.Wrap("ARCHIVE_FAILED", err)
	}
	return nil
}

func loadArchiveReceipt(root, id string) (ArchiveReceipt, error) {
	var receipt ArchiveReceipt
	path := filepath.Join(root, "archive", id, "receipt.json")
	body, err := safeio.ReadRegular(path, maxRecordBytes)
	if errors.Is(err, os.ErrNotExist) {
		return receipt, fail.New("ARCHIVE_NOT_FOUND", "archive receipt does not exist")
	}
	if err != nil {
		return receipt, fail.Wrap("ARCHIVE_FAILED", err)
	}
	if err := strictjson.Decode(body, &receipt); err != nil {
		return ArchiveReceipt{}, fail.Wrap("ARCHIVE_FAILED", err)
	}
	if err := validateArchiveEnvelope(receipt, id); err != nil {
		return ArchiveReceipt{}, err
	}
	return receipt, nil
}

func validateArchiveEnvelope(receipt ArchiveReceipt, id string) error {
	if receipt.SchemaVersion != archiveSchemaVersion {
		return fail.Errorf("MIGRATION_REQUIRED", "archive receipt schema version %d is unsupported; this wip release supports version %d", receipt.SchemaVersion, archiveSchemaVersion)
	}
	if receipt.ID != id || receipt.State != "prepared" && receipt.State != "complete" && receipt.State != "restoring" && receipt.State != "restored" {
		return fail.New("ARCHIVE_FAILED", "archive receipt identity or state is invalid")
	}
	if err := validateArchiveReceipt(receipt); err != nil {
		return err
	}
	digest, err := digestArchiveReceipt(receipt)
	if err != nil || digest != receipt.Digest || receipt.ID != "archive-"+strings.TrimPrefix(digest, "sha256:")[:24] {
		return fail.New("ARCHIVE_FAILED", "archive receipt digest is invalid")
	}
	return nil
}

// LoadArchive reads and validates one immutable archive receipt.
func (store Store) LoadArchive(id string) (ArchiveReceipt, error) {
	if err := validateID(id, "archive"); err != nil {
		return ArchiveReceipt{}, err
	}
	return loadArchiveReceipt(store.Root, id)
}

// ValidateArchiveFiles checks that every receipt file is in the location
// required by its durable state. A prepared or restoring receipt can have each
// file on either side of the move, but never on both sides or neither side.
func (store Store) ValidateArchiveFiles(receipt ArchiveReceipt) error {
	if err := validateArchiveEnvelope(receipt, receipt.ID); err != nil {
		return err
	}
	root, err := os.OpenRoot(store.Root)
	if err != nil {
		return fail.Wrap("ARCHIVE_FAILED", err)
	}
	defer func() { _ = root.Close() }()
	if err := validateArchiveBatchContents(root, receipt); err != nil {
		return err
	}
	candidates := make(map[string]ArchiveCandidate, len(receipt.Candidates))
	laneRecords := map[string]bool{}
	leaseCounts := map[string]int{}
	for _, candidate := range receipt.Candidates {
		candidates[candidate.LaneID] = candidate
	}
	for _, relative := range receipt.Files {
		live, err := archiveRegularExists(root, relative)
		if err != nil {
			return err
		}
		archived := filepath.ToSlash(filepath.Join("archive", receipt.ID, filepath.FromSlash(relative)))
		stored, err := archiveRegularExists(root, archived)
		if err != nil {
			return err
		}
		if live == stored {
			return fail.New("ARCHIVE_FAILED", "archive receipt file exists on both sides or neither side: "+relative)
		}
		if receipt.State == "complete" && !stored || receipt.State == "restored" && !live {
			return fail.New("ARCHIVE_FAILED", "archive receipt file is on the wrong side for state "+receipt.State+": "+relative)
		}
		source := relative
		if stored {
			source = archived
		}
		parts := strings.Split(relative, "/")
		id := strings.TrimSuffix(parts[1], ".json")
		switch parts[0] {
		case "lanes":
			candidate, ok := candidates[id]
			if !ok || laneRecords[id] {
				return fail.New("ARCHIVE_FAILED", "archive lane record is not bound to exactly one candidate: "+relative)
			}
			body, err := readArchiveRecord(root, source)
			if err != nil {
				return err
			}
			var lane Lane
			if err := strictjson.Decode(body, &lane); err != nil {
				return fail.Wrap("ARCHIVE_FAILED", err)
			}
			if err := store.validateLane(lane, id); err != nil {
				return err
			}
			if lane.State != candidate.State || lane.Ref != candidate.Ref || lane.CurrentSHA != candidate.Commit || !lane.UpdatedAt.Equal(candidate.UpdatedAt) {
				return fail.New("ARCHIVE_FAILED", "archive lane record does not match its candidate: "+id)
			}
			laneRecords[id] = true
		case "leases":
			body, err := readArchiveRecord(root, source)
			if err != nil {
				return err
			}
			var lease Lease
			if err := strictjson.Decode(body, &lease); err != nil {
				return fail.Wrap("ARCHIVE_FAILED", err)
			}
			if err := store.validateLease(lease, id); err != nil {
				return err
			}
			if _, ok := candidates[lease.LaneID]; !ok || lease.State != "released" {
				return fail.New("ARCHIVE_FAILED", "archive lease record is not released or does not belong to a candidate: "+id)
			}
			leaseCounts[lease.LaneID]++
		case "profiles":
			if _, ok := candidates[id]; !ok {
				return fail.New("ARCHIVE_FAILED", "archive profile is not bound to a candidate: "+id)
			}
		}
	}
	for _, candidate := range receipt.Candidates {
		if !laneRecords[candidate.LaneID] || leaseCounts[candidate.LaneID] != candidate.LeaseCount {
			return fail.New("ARCHIVE_FAILED", "archive receipt record counts do not match candidate "+candidate.LaneID)
		}
	}
	return nil
}

func validateArchiveBatchContents(root *os.Root, receipt ArchiveReceipt) error {
	batch := filepath.ToSlash(filepath.Join("archive", receipt.ID))
	entries, err := rootDirectoryEntries(root, batch, 4)
	if err != nil {
		return err
	}
	wantedTop := map[string]bool{"receipt.json": true, "lanes": true, "leases": true, "profiles": true}
	seenTop := map[string]bool{}
	for _, entry := range entries {
		if !wantedTop[entry.Name()] || seenTop[entry.Name()] {
			return fail.New("ARCHIVE_FAILED", "archive batch contains an unexpected entry: "+entry.Name())
		}
		seenTop[entry.Name()] = true
		if entry.Name() == "receipt.json" {
			if !entry.Type().IsRegular() || entry.Type()&os.ModeSymlink != 0 {
				return fail.New("ARCHIVE_FAILED", "archive receipt is not a regular file")
			}
			continue
		}
		if !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			return fail.New("ARCHIVE_FAILED", "archive record directory is not a regular directory: "+entry.Name())
		}
	}
	if len(seenTop) != len(wantedTop) {
		return fail.New("ARCHIVE_FAILED", "archive batch layout is incomplete")
	}
	expected := make(map[string]bool, len(receipt.Files))
	for _, relative := range receipt.Files {
		expected[relative] = true
	}
	for _, directory := range []string{"lanes", "leases", "profiles"} {
		path := batch + "/" + directory
		records, err := rootDirectoryEntries(root, path, maxRecordEntries)
		if err != nil {
			return err
		}
		for _, entry := range records {
			relative := directory + "/" + entry.Name()
			if !expected[relative] || !entry.Type().IsRegular() || entry.Type()&os.ModeSymlink != 0 {
				return fail.New("ARCHIVE_FAILED", "archive batch contains an unbound record: "+relative)
			}
		}
	}
	return nil
}

func readArchiveRecord(root *os.Root, relative string) ([]byte, error) {
	file, err := root.Open(filepath.FromSlash(relative))
	if err != nil {
		return nil, fail.Wrap("ARCHIVE_FAILED", err)
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		if err != nil {
			return nil, fail.Wrap("ARCHIVE_FAILED", err)
		}
		return nil, fail.New("ARCHIVE_FAILED", "archive record is not a regular file: "+relative)
	}
	body, err := io.ReadAll(io.LimitReader(file, maxRecordBytes+1))
	if err != nil {
		return nil, fail.Wrap("ARCHIVE_FAILED", err)
	}
	if len(body) > maxRecordBytes {
		return nil, fail.New("ARCHIVE_FAILED", "archive record exceeds the size limit: "+relative)
	}
	return body, nil
}

func archiveRegularExists(root *os.Root, relative string) (bool, error) {
	info, err := root.Lstat(filepath.FromSlash(relative))
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fail.Wrap("ARCHIVE_FAILED", err)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return false, fail.New("ARCHIVE_FAILED", "archive receipt path is not a regular file: "+relative)
	}
	return true, nil
}

func validateArchiveReceipt(receipt ArchiveReceipt) error {
	if err := validateID(receipt.ID, "archive"); err != nil {
		return fail.Wrap("ARCHIVE_FAILED", err)
	}
	if receipt.Before.IsZero() || receipt.CreatedAt.IsZero() || receipt.UpdatedAt.IsZero() || len(receipt.Candidates) == 0 || len(receipt.Candidates) > maxArchiveLanes {
		return fail.New("ARCHIVE_FAILED", "archive receipt bounds or timestamps are invalid")
	}
	lastLane := ""
	for _, candidate := range receipt.Candidates {
		if err := validateID(candidate.LaneID, "lane"); err != nil {
			return fail.Wrap("ARCHIVE_FAILED", err)
		}
		if candidate.LaneID <= lastLane || candidate.State != "released" && candidate.State != "aborted" || !validArchiveRef(candidate.Ref, candidate.LaneID) || !objectID(candidate.Commit) || candidate.UpdatedAt.IsZero() || !candidate.UpdatedAt.Before(receipt.Before) || candidate.LeaseCount < 0 {
			return fail.New("ARCHIVE_FAILED", "archive candidate identity is invalid")
		}
		lastLane = candidate.LaneID
	}
	if len(receipt.Files) == 0 || len(receipt.Files) > maxRecordEntries || !sort.StringsAreSorted(receipt.Files) {
		return fail.New("ARCHIVE_FAILED", "archive file list is empty or not canonical")
	}
	seen := map[string]bool{}
	for _, name := range receipt.Files {
		parts := strings.Split(name, "/")
		if len(parts) != 2 || parts[0] != "lanes" && parts[0] != "leases" && parts[0] != "profiles" || !strings.HasSuffix(parts[1], ".json") || seen[name] {
			return fail.New("ARCHIVE_FAILED", "archive file list contains an invalid path")
		}
		id := strings.TrimSuffix(parts[1], ".json")
		if err := validateID(id, "archive record"); err != nil {
			return fail.Wrap("ARCHIVE_FAILED", err)
		}
		seen[name] = true
	}
	return nil
}

func validArchiveRef(ref, lane string) bool {
	remainder := strings.TrimPrefix(ref, "refs/heads/wip/")
	if remainder == ref {
		return false
	}
	parts := strings.Split(remainder, "/")
	if len(parts) != 2 || parts[1] != lane {
		return false
	}
	return validateID(parts[0], "agent") == nil
}

func writeArchiveReceipt(root string, receipt ArchiveReceipt) error {
	body, err := marshalArchiveReceipt(receipt)
	if err != nil {
		return err
	}
	return fail.Wrap("ARCHIVE_FAILED", atomicfile.WriteWithTempDir(filepath.Join(root, "archive", receipt.ID, "receipt.json"), filepath.Join(root, "locks"), body, 0o600))
}

func marshalArchiveReceipt(receipt ArchiveReceipt) ([]byte, error) {
	return recordjson.Marshal(receipt, maxRecordBytes, "ARCHIVE_FAILED", "archive receipt")
}

func validateArchiveReceiptCapacity(receipt ArchiveReceipt) error {
	maximum := receipt
	maximum.State = "restoring"
	maximum.CreatedAt = maximumStoreRecordTimestamp()
	maximum.UpdatedAt = maximum.CreatedAt
	_, err := marshalArchiveReceipt(maximum)
	return err
}

func digestArchiveReceipt(receipt ArchiveReceipt) (string, error) {
	receipt.ID = ""
	receipt.Digest = ""
	receipt.State = ""
	receipt.CreatedAt = time.Time{}
	receipt.UpdatedAt = time.Time{}
	body, err := json.Marshal(receipt)
	if err != nil {
		return "", fail.Wrap("ARCHIVE_FAILED", err)
	}
	sum := sha256.Sum256(body)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}
