package store

import (
	"context"
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

	"github.com/nstranquist/wip-commit/internal/fail"
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
