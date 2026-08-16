package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nstranquist/wip-commit/internal/atomicfile"
	"github.com/nstranquist/wip-commit/internal/fail"
	"github.com/nstranquist/wip-commit/internal/filelock"
	"github.com/nstranquist/wip-commit/internal/gitx"
)

func TestConcurrentSharedClaimsAreExclusive(t *testing.T) {
	repo := testRepo(t)
	laneStore, err := Open(repo)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"lane-a", "lane-b"} {
		if _, err := laneStore.Create(context.Background(), CreateOptions{ID: id, Agent: id, Session: "session", Mode: ModeShared}); err != nil {
			t.Fatal(err)
		}
	}
	start := make(chan struct{})
	errorsByLane := make([]error, 2)
	var wait sync.WaitGroup
	for index, id := range []string{"lane-a", "lane-b"} {
		wait.Add(1)
		go func(index int, id string) {
			defer wait.Done()
			<-start
			_, errorsByLane[index] = laneStore.Claim(id, id, "session", []string{"src"})
		}(index, id)
	}
	close(start)
	wait.Wait()
	successes, conflicts := 0, 0
	for _, err := range errorsByLane {
		if err == nil {
			successes++
		} else if fail.Code(err) == "PATH_LEASE_CONFLICT" {
			conflicts++
		} else {
			t.Fatalf("unexpected claim error: %v (%s)", err, fail.Code(err))
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("successes=%d conflicts=%d errors=%v", successes, conflicts, errorsByLane)
	}
}

func TestClaimIsIdempotentAndExpiredLeaseCannotRenew(t *testing.T) {
	repo := testRepo(t)
	laneStore, _ := Open(repo)
	lane, err := laneStore.Create(context.Background(), CreateOptions{ID: "owner", Agent: "agent", Session: "session", Mode: ModeShared})
	if err != nil {
		t.Fatal(err)
	}
	first, err := laneStore.Claim(lane.ID, lane.Agent, lane.Session, []string{"alpha.txt"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := laneStore.Claim(lane.ID, lane.Agent, lane.Session, []string{"alpha.txt"})
	if err != nil || first.ID != second.ID {
		t.Fatalf("idempotent claim = %#v, %v; first = %#v", second, err, first)
	}
	past := time.Now().UTC().Add(-time.Minute)
	first.ExpiresAt = &past
	if err := laneStore.writeLease(first); err != nil {
		t.Fatal(err)
	}
	if _, err := laneStore.Renew(lane.ID, lane.Agent, lane.Session); fail.Code(err) != "LEASE_EXPIRED" {
		t.Fatalf("renew error = %v (%s)", err, fail.Code(err))
	}
	other, err := laneStore.Create(context.Background(), CreateOptions{ID: "other", Agent: "other", Session: "session", Mode: ModeShared})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := laneStore.Claim(other.ID, other.Agent, other.Session, []string{"ALPHA.txt"}); err != nil {
		t.Fatalf("expired lease still blocked portable path: %v", err)
	}
}

func TestNewLeasePublicationNeverReplacesAnExistingID(t *testing.T) {
	repo := testRepo(t)
	laneStore, err := Open(repo)
	if err != nil {
		t.Fatal(err)
	}
	lane, err := laneStore.Create(context.Background(), CreateOptions{ID: "lease-no-clobber", Agent: "agent", Session: "session", Mode: ModeShared})
	if err != nil {
		t.Fatal(err)
	}
	original, err := laneStore.Claim(lane.ID, lane.Agent, lane.Session, []string{"base.txt"})
	if err != nil {
		t.Fatal(err)
	}
	forged := original
	forged.Paths = []string{"other.txt"}
	if err := laneStore.createLease(forged); !errors.Is(err, atomicfile.ErrExists) {
		t.Fatalf("duplicate lease creation error = %v", err)
	}
	stored, err := laneStore.LoadLease(original.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !sameStrings(stored.Paths, original.Paths) {
		t.Fatalf("duplicate creation replaced lease paths: got %v want %v", stored.Paths, original.Paths)
	}
}

func TestLeaseWriterRejectsRecordThatItsReaderCannotRead(t *testing.T) {
	repo := testRepo(t)
	laneStore, err := Open(repo)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	expires := now.Add(time.Hour)
	paths := make([]string, 10_000)
	for index := range paths {
		paths[index] = fmt.Sprintf("generated/%04d/%0140d.txt", index, index)
	}
	lease := Lease{
		SchemaVersion: SchemaVersion, ID: "lease-oversized", LaneID: "lane-oversized",
		Agent: "agent", Session: "session", Paths: paths, State: "active",
		CreatedAt: now, UpdatedAt: now, ExpiresAt: &expires,
	}
	if err := laneStore.createLease(lease); fail.Code(err) != "STORE_FAILED" {
		t.Fatalf("oversized lease error = %v (%s)", err, fail.Code(err))
	}
	if _, err := os.Lstat(laneStore.leasePath(lease.ID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("oversized lease was published: %v", err)
	}
}

func TestLeaseWriterReservesSpaceForReleaseState(t *testing.T) {
	timestamp := maximumStoreRecordTimestamp()
	lease := Lease{
		SchemaVersion: SchemaVersion, ID: "lease-capacity", LaneID: "lane-capacity", Agent: "agent", Session: "session",
		Paths: []string{"x"}, State: "active", CreatedAt: timestamp, UpdatedAt: timestamp, ExpiresAt: &timestamp,
	}
	active, err := marshalLeaseRecord(lease)
	if err != nil {
		t.Fatal(err)
	}
	delta := int(maxRecordBytes) - 1 - len(active)
	if delta <= 0 {
		t.Fatalf("unexpected active record size %d", len(active))
	}
	lease.Paths[0] = strings.Repeat("x", delta+1)
	if _, err := marshalLeaseRecord(lease); err != nil {
		t.Fatalf("active form does not fit: %v", err)
	}
	if err := validateLeaseRecordCapacity(lease); fail.Code(err) != "STORE_FAILED" {
		t.Fatalf("release capacity error = %v (%s)", err, fail.Code(err))
	}
}

func TestExactClaimRetryRepairsInterruptedLaneBackReference(t *testing.T) {
	repo := testRepo(t)
	laneStore, err := Open(repo)
	if err != nil {
		t.Fatal(err)
	}
	lane, err := laneStore.Create(context.Background(), CreateOptions{ID: "claim-retry", Agent: "agent", Session: "session", Mode: ModeShared})
	if err != nil {
		t.Fatal(err)
	}
	lease, err := laneStore.Claim(lane.ID, lane.Agent, lane.Session, []string{"src/retry"})
	if err != nil {
		t.Fatal(err)
	}
	lane, err = laneStore.Load(lane.ID)
	if err != nil {
		t.Fatal(err)
	}
	lane.LeaseIDs = nil
	if err := laneStore.writeLane(lane); err != nil {
		t.Fatal(err)
	}
	recovered, err := laneStore.Claim(lane.ID, lane.Agent, lane.Session, []string{"src/retry"})
	if err != nil || recovered.ID != lease.ID {
		t.Fatalf("claim retry = %#v, err=%v; want lease %s", recovered, err, lease.ID)
	}
	lane, err = laneStore.Load(lane.ID)
	if err != nil || len(lane.LeaseIDs) != 1 || lane.LeaseIDs[0] != lease.ID {
		t.Fatalf("repaired lane = %#v, err=%v", lane, err)
	}
}

func TestInterruptedReleaseBlocksLaneOperationsUntilReleaseResumes(t *testing.T) {
	repo := testRepo(t)
	laneStore, err := Open(repo)
	if err != nil {
		t.Fatal(err)
	}
	lane, err := laneStore.Create(context.Background(), CreateOptions{ID: "release-recovery", Agent: "agent", Session: "session", Mode: ModeShared})
	if err != nil {
		t.Fatal(err)
	}
	first, err := laneStore.Claim(lane.ID, lane.Agent, lane.Session, []string{"first.txt"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := laneStore.Claim(lane.ID, lane.Agent, lane.Session, []string{"second.txt"}); err != nil {
		t.Fatal(err)
	}
	lane, err = laneStore.Load(lane.ID)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	first.State, first.UpdatedAt, first.ReleasedAt = "released", now, &now
	if err := laneStore.writeLease(first); err != nil {
		t.Fatal(err)
	}
	for operation, run := range map[string]func() error{
		"claim": func() error {
			_, claimErr := laneStore.Claim(lane.ID, lane.Agent, lane.Session, []string{"third.txt"})
			return claimErr
		},
		"renew": func() error {
			_, renewErr := laneStore.Renew(lane.ID, lane.Agent, lane.Session)
			return renewErr
		},
		"capture": func() error {
			return laneStore.RefreshCaptureLease(context.Background(), lane, []string{"second.txt"})
		},
	} {
		if err := run(); fail.Code(err) != "LANE_RELEASE_RECOVERY_REQUIRED" {
			t.Errorf("%s error = %v (%s)", operation, err, fail.Code(err))
		}
	}
	if err := laneStore.Release(lane.ID, lane.Agent, lane.Session, false); err != nil {
		t.Fatal(err)
	}
	released, err := laneStore.Load(lane.ID)
	if err != nil || released.State != "released" {
		t.Fatalf("resumed release = %#v err=%v", released, err)
	}
}

func TestCaptureRejectsCorruptedCrossLaneLeaseOverlap(t *testing.T) {
	repo := testRepo(t)
	laneStore, err := Open(repo)
	if err != nil {
		t.Fatal(err)
	}
	left, err := laneStore.Create(context.Background(), CreateOptions{ID: "fence-left", Agent: "left", Session: "session", Mode: ModeShared})
	if err != nil {
		t.Fatal(err)
	}
	right, err := laneStore.Create(context.Background(), CreateOptions{ID: "fence-right", Agent: "right", Session: "session", Mode: ModeShared})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := laneStore.Claim(left.ID, left.Agent, left.Session, []string{"src/left"}); err != nil {
		t.Fatal(err)
	}
	rightLease, err := laneStore.Claim(right.ID, right.Agent, right.Session, []string{"src/right"})
	if err != nil {
		t.Fatal(err)
	}
	rightLease.Paths = []string{"src"}
	if err := laneStore.writeLease(rightLease); err != nil {
		t.Fatal(err)
	}
	if err := laneStore.RefreshCaptureLease(context.Background(), left, []string{"src/left"}); fail.Code(err) != "PATH_LEASE_CONFLICT" {
		t.Fatalf("corrupted registry capture error = %v (%s)", err, fail.Code(err))
	}
	if err := laneStore.Release(right.ID, right.Agent, right.Session, false); err != nil {
		t.Fatalf("release could not remove corrupted overlap: %v", err)
	}
	if err := laneStore.RefreshCaptureLease(context.Background(), left, []string{"src/left"}); err != nil {
		t.Fatalf("capture fence stayed blocked after conflicting lane release: %v", err)
	}
}

func TestRenewRejectsCorruptedCrossLaneOverlapBeforeWriting(t *testing.T) {
	repo := testRepo(t)
	laneStore, err := Open(repo)
	if err != nil {
		t.Fatal(err)
	}
	left, err := laneStore.Create(context.Background(), CreateOptions{ID: "renew-left", Agent: "left", Session: "session", Mode: ModeShared})
	if err != nil {
		t.Fatal(err)
	}
	right, err := laneStore.Create(context.Background(), CreateOptions{ID: "renew-right", Agent: "right", Session: "session", Mode: ModeShared})
	if err != nil {
		t.Fatal(err)
	}
	leftLease, err := laneStore.Claim(left.ID, left.Agent, left.Session, []string{"src/left"})
	if err != nil {
		t.Fatal(err)
	}
	rightLease, err := laneStore.Claim(right.ID, right.Agent, right.Session, []string{"src/right"})
	if err != nil {
		t.Fatal(err)
	}
	rightLease.Paths = []string{"src"}
	if err := laneStore.writeLease(rightLease); err != nil {
		t.Fatal(err)
	}
	if _, err := laneStore.Renew(left.ID, left.Agent, left.Session); fail.Code(err) != "PATH_LEASE_CONFLICT" {
		t.Fatalf("corrupted registry renew error = %v (%s)", err, fail.Code(err))
	}
	stored, err := laneStore.LoadLease(leftLease.ID)
	if err != nil || !stored.UpdatedAt.Equal(leftLease.UpdatedAt) || !stored.ExpiresAt.Equal(*leftLease.ExpiresAt) {
		t.Fatalf("failed renew changed lease = %#v, err=%v; want %#v", stored, err, leftLease)
	}
}

func TestReleaseRejectsBrokenLeaseLinksBeforeWriting(t *testing.T) {
	for _, corruption := range []string{"owner", "reverse-reference"} {
		t.Run(corruption, func(t *testing.T) {
			repo := testRepo(t)
			laneStore, err := Open(repo)
			if err != nil {
				t.Fatal(err)
			}
			lane, err := laneStore.Create(context.Background(), CreateOptions{ID: "release-links", Agent: "agent", Session: "session", Mode: ModeShared})
			if err != nil {
				t.Fatal(err)
			}
			lease, err := laneStore.Claim(lane.ID, lane.Agent, lane.Session, []string{"owned.txt"})
			if err != nil {
				t.Fatal(err)
			}
			switch corruption {
			case "owner":
				lease.Agent = "other"
				if err := laneStore.writeLease(lease); err != nil {
					t.Fatal(err)
				}
			case "reverse-reference":
				lane, err = laneStore.Load(lane.ID)
				if err != nil {
					t.Fatal(err)
				}
				lane.LeaseIDs = nil
				if err := laneStore.writeLane(lane); err != nil {
					t.Fatal(err)
				}
			}
			if err := laneStore.Release(lane.ID, lane.Agent, lane.Session, false); fail.Code(err) != "LEASE_MOVED" {
				t.Fatalf("release error = %v (%s)", err, fail.Code(err))
			}
			storedLane, err := laneStore.Load(lane.ID)
			if err != nil || storedLane.State != "active" {
				t.Fatalf("failed release changed lane = %#v, err=%v", storedLane, err)
			}
			storedLease, err := laneStore.LoadLease(lease.ID)
			if err != nil || storedLease.State != "active" {
				t.Fatalf("failed release changed lease = %#v, err=%v", storedLease, err)
			}
		})
	}
}

func TestWorktreeModeBindingIsSymmetric(t *testing.T) {
	anchor := testRepo(t)
	linkedPath := filepath.Join(t.TempDir(), "agent-worktree")
	git(t, anchor.Root, "worktree", "add", "--detach", linkedPath, "HEAD")
	linked, err := gitx.Discover(context.Background(), linkedPath)
	if err != nil {
		t.Fatal(err)
	}
	anchorStore, _ := Open(anchor)
	if _, err := anchorStore.Create(context.Background(), CreateOptions{ID: "bad-anchor", Agent: "agent", Session: "session", Mode: ModeWorktree}); fail.Code(err) != "ANCHOR_WORKTREE_REFUSED" {
		t.Fatalf("anchor worktree-mode error = %v (%s)", err, fail.Code(err))
	}
	linkedStore, _ := Open(linked)
	shared, err := linkedStore.Create(context.Background(), CreateOptions{ID: "shared", Agent: "one", Session: "session", Mode: ModeShared})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := linkedStore.Create(context.Background(), CreateOptions{ID: "exclusive", Agent: "two", Session: "session", Mode: ModeWorktree}); fail.Code(err) != "WORKTREE_CONFLICT" {
		t.Fatalf("exclusive-after-shared error = %v (%s)", err, fail.Code(err))
	}
	if err := linkedStore.Release(shared.ID, shared.Agent, shared.Session, false); err != nil {
		t.Fatal(err)
	}
	exclusive, err := linkedStore.Create(context.Background(), CreateOptions{ID: "exclusive", Agent: "two", Session: "session", Mode: ModeWorktree})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := linkedStore.Create(context.Background(), CreateOptions{ID: "shared-two", Agent: "three", Session: "session", Mode: ModeShared}); fail.Code(err) != "WORKTREE_CONFLICT" {
		t.Fatalf("shared-after-exclusive error = %v (%s)", err, fail.Code(err))
	}
	if exclusive.Worktree != linked.Root {
		t.Fatalf("worktree = %q, want %q", exclusive.Worktree, linked.Root)
	}
}

func TestCreatingLaneRetryFinishesDurably(t *testing.T) {
	repo := testRepo(t)
	laneStore, _ := Open(repo)
	base := git(t, repo.Root, "rev-parse", "HEAD")
	now := time.Now().UTC()
	creating := Lane{SchemaVersion: SchemaVersion, ID: "retry", Agent: "agent", Session: "session", Mode: ModeShared, Ref: LaneRef("agent", "retry"), BaseRef: "HEAD", BaseSHA: base, CurrentSHA: base, Worktree: repo.Root, State: "creating", CreatedAt: now, UpdatedAt: now}
	if err := laneStore.writeLane(creating); err != nil {
		t.Fatal(err)
	}
	lane, err := laneStore.Create(context.Background(), CreateOptions{ID: "retry", Agent: "agent", Session: "session", Mode: ModeShared})
	if err != nil {
		t.Fatal(err)
	}
	if lane.State != "active" || git(t, repo.Root, "rev-parse", lane.Ref) != base {
		t.Fatalf("lane retry did not finish: %#v", lane)
	}
}

func TestMalformedOrSymlinkedManifestFailsClosed(t *testing.T) {
	repo := testRepo(t)
	laneStore, _ := Open(repo)
	lane, err := laneStore.Create(context.Background(), CreateOptions{ID: "strict", Agent: "agent", Session: "session", Mode: ModeShared})
	if err != nil {
		t.Fatal(err)
	}
	path := laneStore.lanePath(lane.ID)
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	corrupt := append(body[:len(body)-2], []byte(",\n  \"unknown\": true\n}\n")...)
	if err := os.WriteFile(path, corrupt, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := laneStore.Load(lane.ID); fail.Code(err) != "STORE_FAILED" {
		t.Fatalf("unknown-field error = %v (%s)", err, fail.Code(err))
	}
	if runtime.GOOS == "windows" {
		return
	}
	external := filepath.Join(t.TempDir(), "lane.json")
	if err := os.WriteFile(external, body, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(external, path); err != nil {
		t.Fatal(err)
	}
	if _, err := laneStore.Load(lane.ID); fail.Code(err) != "STORE_FAILED" {
		t.Fatalf("symlink manifest error = %v (%s)", err, fail.Code(err))
	}
}

func TestFutureStateDirectoryFailsBeforeV1Creation(t *testing.T) {
	for _, version := range []string{"v2", "v01"} {
		t.Run(version, func(t *testing.T) {
			repo := testRepo(t)
			stateRoot := filepath.Join(repo.CommonDir, "wip")
			if err := os.MkdirAll(filepath.Join(stateRoot, version), 0o700); err != nil {
				t.Fatal(err)
			}
			if _, err := Open(repo); fail.Code(err) != "MIGRATION_REQUIRED" {
				t.Fatalf("unsupported state error = %v (%s)", err, fail.Code(err))
			}
			if _, err := os.Lstat(filepath.Join(stateRoot, "v1")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("Open created v1 beside unsupported state: %v", err)
			}
		})
	}
}

func TestInspectReportsMarkerOnlyDomainWithoutCreatingState(t *testing.T) {
	repo := testRepo(t)
	stateRoot := filepath.Join(repo.CommonDir, "wip")
	if err := os.Mkdir(stateRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(domainRecord{SchemaVersion: domainSchemaVersion, Domain: domainID, StateVersion: SchemaVersion})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stateRoot, "domain.json"), body, 0o600); err != nil {
		t.Fatal(err)
	}
	opened, initialized, err := Inspect(repo)
	if err != nil || !initialized || opened.Root != filepath.Join(stateRoot, "v1") {
		t.Fatalf("marker-only inspection = %#v initialized=%v err=%v", opened, initialized, err)
	}
	if _, err := os.Lstat(opened.Root); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("inspection created version state: %v", err)
	}
}

func TestUnknownVersionedStateEntryRequiresMigrationBeforeMutation(t *testing.T) {
	for _, name := range []string{"future-records", "future-record.json"} {
		t.Run(name, func(t *testing.T) {
			repo := testRepo(t)
			stateRoot := filepath.Join(repo.CommonDir, "wip")
			versionRoot := filepath.Join(stateRoot, "v1")
			if err := os.MkdirAll(versionRoot, 0o700); err != nil {
				t.Fatal(err)
			}
			unknown := filepath.Join(versionRoot, name)
			if filepath.Ext(name) == ".json" {
				if err := os.WriteFile(unknown, []byte("{}\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			} else if err := os.Mkdir(unknown, 0o700); err != nil {
				t.Fatal(err)
			}
			if _, err := Open(repo); fail.Code(err) != "MIGRATION_REQUIRED" {
				t.Fatalf("unknown state error = %v (%s)", err, fail.Code(err))
			}
			if _, err := os.Lstat(filepath.Join(stateRoot, "domain.json")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("Open created a marker after layout failure: %v", err)
			}
			if _, err := os.Lstat(filepath.Join(versionRoot, "lanes")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("Open created known state after layout failure: %v", err)
			}
		})
	}
}

func TestOperationalReadsRejectUnexpectedRecordEntries(t *testing.T) {
	for _, directory := range []string{"lanes", "leases"} {
		t.Run(directory, func(t *testing.T) {
			repo := testRepo(t)
			laneStore, err := Open(repo)
			if err != nil {
				t.Fatal(err)
			}
			lane, err := laneStore.Create(context.Background(), CreateOptions{ID: "closed-records", Agent: "agent", Session: "session", Mode: ModeShared})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := laneStore.Claim(lane.ID, lane.Agent, lane.Session, []string{"base.txt"}); err != nil {
				t.Fatal(err)
			}
			unexpected := filepath.Join(laneStore.Root, directory, "unexpected.tmp")
			if err := os.WriteFile(unexpected, []byte("foreign\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			if directory == "lanes" {
				_, err = laneStore.Current(lane.Agent, lane.Session, lane.ID)
			} else {
				_, err = laneStore.ActivePaths(lane.ID)
			}
			if fail.Code(err) != "STORE_FAILED" {
				t.Fatalf("unexpected %s entry error = %v (%s)", directory, err, fail.Code(err))
			}
		})
	}
}

func TestStateRootReaderIsBoundedAndRejectsSymlink(t *testing.T) {
	t.Run("bounded", func(t *testing.T) {
		repo := testRepo(t)
		stateRoot := filepath.Join(repo.CommonDir, "wip")
		for index := 0; index <= maxStateEntries; index++ {
			path := filepath.Join(stateRoot, fmt.Sprintf("entry-%02d", index))
			if err := os.MkdirAll(path, 0o700); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := Open(repo); fail.Code(err) != "STORE_FAILED" {
			t.Fatalf("oversized state root error = %v (%s)", err, fail.Code(err))
		}
		if _, err := os.Lstat(filepath.Join(stateRoot, "v1")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("Open created v1 after a bound failure: %v", err)
		}
	})

	t.Run("symlink", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("symlink creation needs optional Windows privileges")
		}
		repo := testRepo(t)
		stateRoot := filepath.Join(repo.CommonDir, "wip")
		external := t.TempDir()
		if err := os.Symlink(external, stateRoot); err != nil {
			t.Fatal(err)
		}
		if _, err := Open(repo); fail.Code(err) != "STORE_FAILED" {
			t.Fatalf("symlinked state root error = %v (%s)", err, fail.Code(err))
		}
		if _, err := os.Lstat(filepath.Join(external, "v1")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("Open wrote through a state-root symlink: %v", err)
		}
	})
}

func TestStateSubdirectoriesCannotRedirectOutsideCommonDir(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation needs optional Windows privileges")
	}
	for _, relative := range []string{"v1", filepath.Join("v1", "lanes"), filepath.Join("v1", "leases"), filepath.Join("v1", "locks"), filepath.Join("v1", "intents"), filepath.Join("v1", "profiles"), filepath.Join("v1", "init-intents"), filepath.Join("v1", "archive")} {
		t.Run(filepath.ToSlash(relative), func(t *testing.T) {
			repo := testRepo(t)
			stateRoot := filepath.Join(repo.CommonDir, "wip")
			parent := filepath.Dir(filepath.Join(stateRoot, relative))
			if err := os.MkdirAll(parent, 0o700); err != nil {
				t.Fatal(err)
			}
			external := t.TempDir()
			if err := os.Symlink(external, filepath.Join(stateRoot, relative)); err != nil {
				t.Fatal(err)
			}
			if _, err := Open(repo); fail.Code(err) != "STORE_FAILED" {
				t.Fatalf("symlinked %s error = %v (%s)", relative, err, fail.Code(err))
			}
			entries, err := os.ReadDir(external)
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 0 {
				t.Fatalf("Open wrote through %s symlink: %v", relative, entries)
			}
		})
	}
}

func TestCoordinationDomainMarkerAndForeignDomainFailClosed(t *testing.T) {
	t.Run("marker", func(t *testing.T) {
		repo := testRepo(t)
		laneStore, err := Open(repo)
		if err != nil {
			t.Fatal(err)
		}
		body, err := os.ReadFile(filepath.Join(repo.CommonDir, "wip", "domain.json"))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(body), domainID) || laneStore.Root != filepath.Join(repo.CommonDir, "wip", "v1") {
			t.Fatalf("domain marker/root mismatch: %s root=%s", body, laneStore.Root)
		}
	})

	t.Run("foreign", func(t *testing.T) {
		repo := testRepo(t)
		if err := os.MkdirAll(filepath.Join(repo.CommonDir, "ndev-wip", "v1"), 0o700); err != nil {
			t.Fatal(err)
		}
		if _, err := Open(repo); fail.Code(err) != "COORDINATION_DOMAIN_CONFLICT" {
			t.Fatalf("foreign-domain error = %v (%s)", err, fail.Code(err))
		}
		if _, err := os.Lstat(filepath.Join(repo.CommonDir, "wip")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("Open created public state after foreign-domain conflict: %v", err)
		}
	})

	t.Run("future marker", func(t *testing.T) {
		repo := testRepo(t)
		if _, err := Open(repo); err != nil {
			t.Fatal(err)
		}
		marker := filepath.Join(repo.CommonDir, "wip", "domain.json")
		body, err := os.ReadFile(marker)
		if err != nil {
			t.Fatal(err)
		}
		body = []byte(strings.Replace(string(body), `"state_version": 1`, `"state_version": 2`, 1))
		if err := os.WriteFile(marker, body, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := Open(repo); fail.Code(err) != "MIGRATION_REQUIRED" {
			t.Fatalf("future marker error = %v (%s)", err, fail.Code(err))
		}
	})

	t.Run("future marker schema", func(t *testing.T) {
		repo := testRepo(t)
		if _, err := Open(repo); err != nil {
			t.Fatal(err)
		}
		marker := filepath.Join(repo.CommonDir, "wip", "domain.json")
		body, err := os.ReadFile(marker)
		if err != nil {
			t.Fatal(err)
		}
		body = []byte(strings.Replace(string(body), `"schema_version": 1`, `"schema_version": 2`, 1))
		if err := os.WriteFile(marker, body, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := Open(repo); fail.Code(err) != "MIGRATION_REQUIRED" {
			t.Fatalf("future marker schema error = %v (%s)", err, fail.Code(err))
		}
	})
}

func TestCoordinationLockPreventsDualDomainCreationRace(t *testing.T) {
	for iteration := 0; iteration < 10; iteration++ {
		repo := testRepo(t)
		start := make(chan struct{})
		results := make(chan string, 2)
		go func() {
			<-start
			if _, err := Open(repo); err == nil {
				results <- "public"
			} else if fail.Code(err) == "COORDINATION_DOMAIN_CONFLICT" {
				results <- "public-conflict"
			} else {
				results <- "public-error:" + err.Error()
			}
		}()
		go func() {
			<-start
			lock, err := filelock.Acquire(filepath.Join(repo.CommonDir, "wip-coordination.lock"), 0)
			if err != nil {
				results <- "legacy-error:" + err.Error()
				return
			}
			defer func() { _ = lock.Release() }()
			for _, marker := range []string{filepath.Join(repo.CommonDir, "wip", "domain.json"), filepath.Join(repo.CommonDir, "wip", "v1")} {
				if _, statErr := os.Lstat(marker); statErr == nil {
					results <- "legacy-conflict"
					return
				} else if !errors.Is(statErr, os.ErrNotExist) {
					results <- "legacy-error:" + statErr.Error()
					return
				}
			}
			if err := os.MkdirAll(filepath.Join(repo.CommonDir, "ndev-wip", "v1"), 0o700); err != nil {
				results <- "legacy-error:" + err.Error()
				return
			}
			results <- "legacy"
		}()
		close(start)
		first, second := <-results, <-results
		outcomes := []string{first, second}
		successes, conflicts := 0, 0
		for _, outcome := range outcomes {
			switch outcome {
			case "public", "legacy":
				successes++
			case "public-conflict", "legacy-conflict":
				conflicts++
			default:
				t.Fatalf("iteration %d unexpected outcome: %q (%v)", iteration, outcome, outcomes)
			}
		}
		if successes != 1 || conflicts != 1 {
			t.Fatalf("iteration %d outcomes = %v", iteration, outcomes)
		}
		_, publicErr := os.Lstat(filepath.Join(repo.CommonDir, "wip", "v1"))
		_, legacyErr := os.Lstat(filepath.Join(repo.CommonDir, "ndev-wip", "v1"))
		if (publicErr == nil) == (legacyErr == nil) {
			t.Fatalf("iteration %d created zero or two domains: public=%v legacy=%v", iteration, publicErr, legacyErr)
		}
	}
}

func TestArchiveReceiptWriterRejectsUnreadableRecordSize(t *testing.T) {
	now := time.Now().UTC()
	receipt := ArchiveReceipt{
		SchemaVersion: archiveSchemaVersion, ID: "archive-oversized", Digest: "sha256:" + strings.Repeat("a", 64),
		State: "prepared", Before: now.Add(time.Hour), CreatedAt: now, UpdatedAt: now,
	}
	for index := 0; index < maxArchiveLanes; index++ {
		laneID := fmt.Sprintf("lane-%04d-%048d", index, index)
		receipt.Candidates = append(receipt.Candidates, ArchiveCandidate{
			LaneID: laneID, State: "released", Ref: "refs/heads/wip/agent/" + laneID,
			Commit: strings.Repeat("b", 40), UpdatedAt: now, LeaseCount: 9,
		})
	}
	for index := 0; index < maxRecordEntries; index++ {
		receipt.Files = append(receipt.Files, fmt.Sprintf("leases/lease-%04d-%048d.json", index, index))
	}
	if _, err := marshalArchiveReceipt(receipt); fail.Code(err) != "ARCHIVE_FAILED" {
		t.Fatalf("oversized archive receipt error = %v (%s)", err, fail.Code(err))
	}
}

func TestArchiveIsRecoverableAndResumesPartialMoves(t *testing.T) {
	repo := testRepo(t)
	laneStore, err := Open(repo)
	if err != nil {
		t.Fatal(err)
	}
	lane, err := laneStore.Create(context.Background(), CreateOptions{ID: "archive-me", Agent: "agent", Session: "session", Mode: ModeShared})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := laneStore.Claim(lane.ID, lane.Agent, lane.Session, []string{"base.txt"}); err != nil {
		t.Fatal(err)
	}
	if err := laneStore.Release(lane.ID, lane.Agent, lane.Session, false); err != nil {
		t.Fatal(err)
	}
	lane, err = laneStore.Load(lane.ID)
	if err != nil {
		t.Fatal(err)
	}
	lane.UpdatedAt = time.Now().UTC().Add(-48 * time.Hour)
	if err := laneStore.writeLane(lane); err != nil {
		t.Fatal(err)
	}
	cutoff := time.Now().UTC().Add(-24 * time.Hour)
	candidates, err := laneStore.ArchiveCandidates(cutoff)
	if err != nil || len(candidates) != 1 {
		t.Fatalf("archive candidates = %#v err=%v", candidates, err)
	}
	prepared, err := laneStore.prepareArchiveReceipt(cutoff, candidates)
	if err != nil {
		t.Fatal(err)
	}
	receiptPath := filepath.Join(laneStore.Root, "archive", prepared.ID, "receipt.json")
	if err := os.Remove(receiptPath); err != nil {
		t.Fatal(err)
	}
	recovered, err := laneStore.prepareArchiveReceipt(cutoff, candidates)
	if err != nil {
		t.Fatalf("recover receipt-free batch: %v", err)
	}
	if recovered.ID != prepared.ID || recovered.State != "prepared" {
		t.Fatalf("recovered receipt = %#v, original=%#v", recovered, prepared)
	}
	prepared = recovered
	root, err := os.OpenRoot(laneStore.Root)
	if err != nil {
		t.Fatal(err)
	}
	partial := ""
	for _, relative := range prepared.Files {
		if strings.HasPrefix(relative, "leases/") {
			partial = relative
			break
		}
	}
	if partial == "" {
		t.Fatal("archive receipt has no lease record for interruption test")
	}
	if err := moveArchiveFile(root, prepared.ID, partial, false); err != nil {
		t.Fatal(err)
	}
	_ = root.Close()
	if err := laneStore.ValidateArchiveFiles(prepared); err != nil {
		t.Fatalf("prepared archive placement: %v", err)
	}
	receipt, err := laneStore.ResumeArchive(context.Background(), prepared.ID)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.ID != prepared.ID || receipt.State != "complete" {
		t.Fatalf("resumed archive receipt = %#v, prepared=%#v", receipt, prepared)
	}
	if err := laneStore.ValidateArchiveFiles(receipt); err != nil {
		t.Fatalf("complete archive placement: %v", err)
	}
	if _, err := laneStore.Load(lane.ID); fail.Code(err) != "LANE_NOT_FOUND" {
		t.Fatalf("archived lane remained live: %v (%s)", err, fail.Code(err))
	}
	if got := git(t, repo.Root, "rev-parse", lane.Ref); got != lane.CurrentSHA {
		t.Fatalf("archive moved lane ref: %s", got)
	}
	receipt.State, receipt.UpdatedAt = "restoring", time.Now().UTC()
	if err := writeArchiveReceipt(laneStore.Root, receipt); err != nil {
		t.Fatal(err)
	}
	restorePartial := ""
	for _, relative := range receipt.Files {
		if !strings.HasPrefix(relative, "lanes/") {
			restorePartial = relative
			break
		}
	}
	if restorePartial == "" {
		t.Fatal("archive receipt has no support record for restore interruption test")
	}
	root, err = os.OpenRoot(laneStore.Root)
	if err != nil {
		t.Fatal(err)
	}
	if err := moveArchiveFile(root, receipt.ID, restorePartial, true); err != nil {
		t.Fatal(err)
	}
	_ = root.Close()
	if err := laneStore.ValidateArchiveFiles(receipt); err != nil {
		t.Fatalf("restoring archive placement: %v", err)
	}
	restored, err := laneStore.RestoreArchive(context.Background(), receipt.ID)
	if err != nil || restored.State != "restored" {
		t.Fatalf("restore receipt = %#v err=%v", restored, err)
	}
	if err := laneStore.ValidateArchiveFiles(restored); err != nil {
		t.Fatalf("restored archive placement: %v", err)
	}
	loaded, err := laneStore.Load(lane.ID)
	if err != nil || loaded.State != "released" || loaded.CurrentSHA != lane.CurrentSHA {
		t.Fatalf("restored lane = %#v err=%v", loaded, err)
	}
	if got := git(t, repo.Root, "rev-parse", lane.Ref); got != lane.CurrentSHA {
		t.Fatalf("restore moved lane ref: %s", got)
	}
	again, err := laneStore.RestoreArchive(context.Background(), receipt.ID)
	if err != nil || again.State != "restored" {
		t.Fatalf("idempotent restore = %#v err=%v", again, err)
	}
}

func TestArchiveRejectsCandidateMovementAfterPreview(t *testing.T) {
	repo := testRepo(t)
	laneStore, err := Open(repo)
	if err != nil {
		t.Fatal(err)
	}
	lane, err := laneStore.Create(context.Background(), CreateOptions{ID: "archive-moved", Agent: "agent", Session: "session", Mode: ModeShared})
	if err != nil {
		t.Fatal(err)
	}
	if err := laneStore.Release(lane.ID, lane.Agent, lane.Session, false); err != nil {
		t.Fatal(err)
	}
	lane, err = laneStore.Load(lane.ID)
	if err != nil {
		t.Fatal(err)
	}
	lane.UpdatedAt = time.Now().UTC().Add(-72 * time.Hour)
	if err := laneStore.writeLane(lane); err != nil {
		t.Fatal(err)
	}
	cutoff := time.Now().UTC().Add(-24 * time.Hour)
	preview, err := laneStore.ArchiveCandidates(cutoff)
	if err != nil || len(preview) != 1 {
		t.Fatalf("preview = %#v err=%v", preview, err)
	}
	lane.UpdatedAt = lane.UpdatedAt.Add(time.Second)
	if err := laneStore.writeLane(lane); err != nil {
		t.Fatal(err)
	}
	if _, err := laneStore.Archive(context.Background(), cutoff, preview); fail.Code(err) != "ARCHIVE_PLAN_MOVED" {
		t.Fatalf("moved archive error = %v (%s)", err, fail.Code(err))
	}
	if _, err := laneStore.Load(lane.ID); err != nil {
		t.Fatalf("moved plan archived live record: %v", err)
	}
	if got := git(t, repo.Root, "rev-parse", lane.Ref); got != lane.CurrentSHA {
		t.Fatalf("moved plan changed lane ref: %s", got)
	}
}

func TestArchiveReceiptRejectsCrossLaneAndExtraRecords(t *testing.T) {
	repo := testRepo(t)
	laneStore, err := Open(repo)
	if err != nil {
		t.Fatal(err)
	}
	lanes := make([]Lane, 0, 2)
	leases := make([]Lease, 0, 2)
	for index, id := range []string{"archive-bound-a", "archive-bound-b"} {
		lane, err := laneStore.Create(context.Background(), CreateOptions{ID: id, Agent: id, Session: "session", Mode: ModeShared})
		if err != nil {
			t.Fatal(err)
		}
		lease, err := laneStore.Claim(lane.ID, lane.Agent, lane.Session, []string{fmt.Sprintf("owned-%d.txt", index)})
		if err != nil {
			t.Fatal(err)
		}
		if err := laneStore.Release(lane.ID, lane.Agent, lane.Session, false); err != nil {
			t.Fatal(err)
		}
		lane, err = laneStore.Load(lane.ID)
		if err != nil {
			t.Fatal(err)
		}
		lane.UpdatedAt = time.Now().UTC().Add(-48 * time.Hour)
		if err := laneStore.writeLane(lane); err != nil {
			t.Fatal(err)
		}
		lease, err = laneStore.LoadLease(lease.ID)
		if err != nil {
			t.Fatal(err)
		}
		lanes = append(lanes, lane)
		leases = append(leases, lease)
	}
	cutoff := time.Now().UTC().Add(-24 * time.Hour)
	candidate, eligible, err := laneStore.archiveCandidate(lanes[0].ID, cutoff)
	if err != nil || !eligible {
		t.Fatalf("candidate = %#v eligible=%v err=%v", candidate, eligible, err)
	}
	receipt, err := laneStore.prepareArchiveReceipt(cutoff, []ArchiveCandidate{candidate})
	if err != nil {
		t.Fatal(err)
	}
	forged := leases[0]
	forged.LaneID = lanes[1].ID
	if err := laneStore.writeLease(forged); err != nil {
		t.Fatal(err)
	}
	if err := laneStore.ValidateArchiveFiles(receipt); fail.Code(err) != "ARCHIVE_FAILED" {
		t.Fatalf("cross-lane receipt error = %v (%s)", err, fail.Code(err))
	}
	if err := laneStore.writeLease(leases[0]); err != nil {
		t.Fatal(err)
	}
	extra := filepath.Join(laneStore.Root, "archive", receipt.ID, "leases", "foreign.json")
	if err := os.WriteFile(extra, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := laneStore.ValidateArchiveFiles(receipt); fail.Code(err) != "ARCHIVE_FAILED" {
		t.Fatalf("extra archive record error = %v (%s)", err, fail.Code(err))
	}
}

func TestFutureLaneAndLeaseSchemasRequireMigration(t *testing.T) {
	t.Run("lane", func(t *testing.T) {
		repo := testRepo(t)
		laneStore, err := Open(repo)
		if err != nil {
			t.Fatal(err)
		}
		lane, err := laneStore.Create(context.Background(), CreateOptions{ID: "future-lane", Agent: "agent", Session: "session", Mode: ModeShared})
		if err != nil {
			t.Fatal(err)
		}
		replaceStoreSchema(t, laneStore.lanePath(lane.ID), SchemaVersion+1)
		if _, err := laneStore.Load(lane.ID); fail.Code(err) != "MIGRATION_REQUIRED" {
			t.Fatalf("future lane error = %v (%s)", err, fail.Code(err))
		}
	})

	t.Run("lease", func(t *testing.T) {
		repo := testRepo(t)
		laneStore, err := Open(repo)
		if err != nil {
			t.Fatal(err)
		}
		lane, err := laneStore.Create(context.Background(), CreateOptions{ID: "future-lease", Agent: "agent", Session: "session", Mode: ModeShared})
		if err != nil {
			t.Fatal(err)
		}
		lease, err := laneStore.Claim(lane.ID, lane.Agent, lane.Session, []string{"base.txt"})
		if err != nil {
			t.Fatal(err)
		}
		replaceStoreSchema(t, filepath.Join(laneStore.Root, "leases", lease.ID+".json"), SchemaVersion+1)
		if _, err := laneStore.ActivePaths(lane.ID); fail.Code(err) != "MIGRATION_REQUIRED" {
			t.Fatalf("future lease error = %v (%s)", err, fail.Code(err))
		}
	})

	t.Run("archive receipt", func(t *testing.T) {
		repo := testRepo(t)
		laneStore, err := Open(repo)
		if err != nil {
			t.Fatal(err)
		}
		lane, err := laneStore.Create(context.Background(), CreateOptions{ID: "future-archive", Agent: "agent", Session: "session", Mode: ModeShared})
		if err != nil {
			t.Fatal(err)
		}
		if err := laneStore.Release(lane.ID, lane.Agent, lane.Session, false); err != nil {
			t.Fatal(err)
		}
		lane, err = laneStore.Load(lane.ID)
		if err != nil {
			t.Fatal(err)
		}
		lane.UpdatedAt = time.Now().UTC().Add(-48 * time.Hour)
		if err := laneStore.writeLane(lane); err != nil {
			t.Fatal(err)
		}
		cutoff := time.Now().UTC().Add(-24 * time.Hour)
		candidates, err := laneStore.ArchiveCandidates(cutoff)
		if err != nil {
			t.Fatal(err)
		}
		receipt, err := laneStore.prepareArchiveReceipt(cutoff, candidates)
		if err != nil {
			t.Fatal(err)
		}
		receipt.SchemaVersion = archiveSchemaVersion + 1
		if err := writeArchiveReceipt(laneStore.Root, receipt); err != nil {
			t.Fatal(err)
		}
		if _, err := laneStore.LoadArchive(receipt.ID); fail.Code(err) != "MIGRATION_REQUIRED" {
			t.Fatalf("future archive receipt error = %v (%s)", err, fail.Code(err))
		}
	})
}

func replaceStoreSchema(t *testing.T, path string, version int) {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	old := `"schema_version": 1`
	if strings.Count(string(body), old) != 1 {
		t.Fatalf("schema field count in %s is not one", path)
	}
	updated := strings.Replace(string(body), old, fmt.Sprintf(`"schema_version": %d`, version), 1)
	if err := os.WriteFile(path, []byte(updated), 0o600); err != nil {
		t.Fatal(err)
	}
}

func testRepo(t *testing.T) gitx.Repo {
	t.Helper()
	directory := t.TempDir()
	git(t, directory, "init", "-b", "main")
	git(t, directory, "config", "user.name", "WIP Tests")
	git(t, directory, "config", "user.email", "wip@example.invalid")
	if err := os.WriteFile(filepath.Join(directory, "base.txt"), []byte("base\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	git(t, directory, "add", "base.txt")
	git(t, directory, "commit", "-m", "test: create fixture")
	repo, err := gitx.Discover(context.Background(), directory)
	if err != nil {
		t.Fatal(err)
	}
	return repo
}

func git(t *testing.T, directory string, args ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", directory}, args...)...)
	command.Env = append(os.Environ(), "LC_ALL=C", "TZ=UTC")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
	return stringTrim(string(output))
}

func stringTrim(value string) string {
	for len(value) > 0 && (value[len(value)-1] == '\n' || value[len(value)-1] == '\r') {
		value = value[:len(value)-1]
	}
	return value
}
