package coordmigration

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/nstranquist/wip-commit/internal/contract"
	"github.com/nstranquist/wip-commit/internal/fail"
	"github.com/nstranquist/wip-commit/internal/filelock"
	"github.com/nstranquist/wip-commit/internal/gitx"
	"github.com/nstranquist/wip-commit/internal/store"
)

func TestPreviewIsByteForByteReadOnly(t *testing.T) {
	repo, head := migrationTestRepo(t)
	writeLegacyFixture(t, repo, head)
	before := testTreeDigest(t, repo.CommonDir)
	plan, err := previewForTest(context.Background(), repo, PreviewOptions{})
	if err != nil {
		t.Fatal(err)
	}
	after := testTreeDigest(t, repo.CommonDir)
	if before != after {
		t.Fatalf("preview changed Git common directory: before=%s after=%s", before, after)
	}
	if plan.State != "ready" || !plan.SourceLegacy.Exists || len(plan.LaneImports) != 1 || len(plan.LeaseImports) != 1 {
		t.Fatalf("unexpected preview: %#v", plan)
	}
	if _, err := os.Lstat(filepath.Join(repo.CommonDir, "wip-coordination.lock")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("preview created a coordination lock: %v", err)
	}
}

func TestApplyLegacyOnlyImportsAndSeals(t *testing.T) {
	repo, head := migrationTestRepo(t)
	writeLegacyFixture(t, repo, head)
	writeTestFile(t, filepath.Join(repo.CommonDir, "ndev-commit-lane", "reconcile", "history.json"), []byte("{\"result\":\"ok\"}\n"), 0o640)
	plan := mustPreview(t, repo)
	result, err := applyForTest(context.Background(), repo, plan, plan.PlanDigest, ApplyOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Applied || result.Receipt == nil || result.Status.State != "complete" {
		t.Fatalf("unexpected result: %#v", result)
	}
	if _, err := os.Lstat(filepath.Join(repo.CommonDir, "ndev-wip")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("legacy source remains: %v", err)
	}
	assertDigest(t, filepath.Join(repo.CommonDir, "wip", "v1", "lanes", "lane1.json"), plan.LaneImports[0].SourceSHA256)
	assertDigest(t, filepath.Join(repo.CommonDir, "wip", "v1", "leases", "lease1.json"), plan.LeaseImports[0].SourceSHA256)
	assertDigest(t, filepath.Join(repo.CommonDir, "wip", "review", "v1", "queue.json"), findCopy(t, plan.ReviewCopies, "queue.json").SourceSHA256)
	assertDigest(t, filepath.Join(repo.CommonDir, "wip", "review", "v1", "reconciliation", "history.json"), plan.ReconciliationCopies[0].SourceSHA256)
	archive := filepath.Join(archiveMigrationRoot(repo.CommonDir, plan.MigrationID), "source")
	info, err := os.Stat(archive)
	if err != nil || info.Mode().Perm() != 0o500 {
		t.Fatalf("archive is not sealed: mode=%v err=%v", infoMode(info), err)
	}
	status, err := InspectStatus(repo, plan.MigrationID)
	if err != nil || !status.Archive || status.Receipt == nil {
		t.Fatalf("invalid final status: %#v err=%v", status, err)
	}
}

func TestEmptyAndPublicOnlyMigrations(t *testing.T) {
	t.Run("empty", func(t *testing.T) {
		repo, _ := migrationTestRepo(t)
		plan := mustPreview(t, repo)
		if plan.SourceLegacy.Exists || plan.ExpectedCanonical.Exists {
			t.Fatalf("unexpected input manifests: %#v", plan)
		}
		lockEntry, found := testManifestEntry(plan.PlannedCanonicalData.Capture, captureRegistryLock)
		if !found || lockEntry.Type != "file" || lockEntry.Mode != "0600" || lockEntry.Size != 0 || lockEntry.SHA256 != digest(nil) {
			t.Fatalf("planned registry lock is not exact: %#v", lockEntry)
		}
		if _, err := applyForTest(context.Background(), repo, plan, plan.PlanDigest, ApplyOptions{}); err != nil {
			t.Fatal(err)
		}
		lockPath := filepath.Join(repo.CommonDir, "wip", "v1", "locks", "leases.lock")
		info, err := os.Lstat(lockPath)
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
			t.Fatalf("registry lock is unavailable after apply: mode=%v err=%v", infoMode(info), err)
		}
		if body, err := os.ReadFile(lockPath); err != nil || len(body) != 0 {
			t.Fatalf("registry lock bytes changed during apply: %q err=%v", body, err)
		}
		if exists, err := regularDirectoryState(filepath.Join(repo.CommonDir, "wip", "review", "v1")); err != nil || !exists {
			t.Fatalf("review store missing: %v", err)
		}
		for _, directory := range []string{"archive", "capture-aborts", "capture-completions", "capture-reservations", "handoffs", "landing-intents", "locks", "plans", "reconciliation"} {
			path := filepath.Join(repo.CommonDir, "wip", "review", "v1", directory)
			if exists, err := regularDirectoryState(path); err != nil || !exists {
				t.Fatalf("required review store directory %s missing: %v", directory, err)
			}
		}
		marker, err := os.ReadFile(filepath.Join(repo.CommonDir, "wip", "review", "v1", "domain.json"))
		if err != nil {
			t.Fatal(err)
		}
		wantMarker := []byte(`{"schema_version":1,"domain":"nicos.tools/ndev/wip-review","state_version":2,"authority":"ndev","queue_entry_schema_version":2,"handoff_schema_version":"2.0.0","automatic_landing":false,"handoff_landing_authorized":false,"review_digest_required":true}` + "\n")
		if !bytes.Equal(marker, wantMarker) {
			t.Fatalf("review authority marker mismatch:\n%s", marker)
		}
	})
	t.Run("public-only", func(t *testing.T) {
		repo, _ := migrationTestRepo(t)
		if _, err := store.Open(repo); err != nil {
			t.Fatal(err)
		}
		lockPath := filepath.Join(repo.CommonDir, "wip", "v1", "locks", "leases.lock")
		writeTestFile(t, lockPath, []byte("existing registry lock\n"), 0o600)
		lockBefore, err := os.ReadFile(lockPath)
		if err != nil {
			t.Fatal(err)
		}
		plan := mustPreview(t, repo)
		if plan.SourceLegacy.Exists || !plan.ExpectedCanonical.Exists {
			t.Fatalf("unexpected input manifests: %#v", plan)
		}
		lockEntry, found := testManifestEntry(plan.PlannedCanonicalData.Capture, captureRegistryLock)
		if !found || lockEntry.Size != int64(len(lockBefore)) || lockEntry.SHA256 != digest(lockBefore) {
			t.Fatalf("existing registry lock is not bound exactly: %#v", lockEntry)
		}
		if _, err := applyForTest(context.Background(), repo, plan, plan.PlanDigest, ApplyOptions{}); err != nil {
			t.Fatal(err)
		}
		lockAfter, err := os.ReadFile(lockPath)
		if err != nil || !bytes.Equal(lockBefore, lockAfter) {
			t.Fatalf("migration changed the existing registry lock: before=%q after=%q err=%v", lockBefore, lockAfter, err)
		}
	})
	t.Run("nonregular registry lock", func(t *testing.T) {
		repo, _ := migrationTestRepo(t)
		if _, err := store.Open(repo); err != nil {
			t.Fatal(err)
		}
		lockPath := filepath.Join(repo.CommonDir, "wip", "v1", "locks", "leases.lock")
		if err := os.Mkdir(lockPath, 0o700); err != nil {
			t.Fatal(err)
		}
		if _, err := previewForTest(context.Background(), repo, PreviewOptions{}); fail.Code(err) != "MIGRATION_PLAN_FAILED" {
			t.Fatalf("nonregular registry lock was accepted: %v (%s)", err, fail.Code(err))
		}
	})
}

func TestRegistryLockPublishesBeforeImportedRecords(t *testing.T) {
	repo, head := migrationTestRepo(t)
	writeLegacyFixture(t, repo, head)
	plan := mustPreview(t, repo)

	_, err := applyForTest(context.Background(), repo, plan, plan.PlanDigest, ApplyOptions{AfterPhase: func(phase string) error {
		if phase == phaseCanonicalPrepared {
			return errors.New("stop before record publication")
		}
		return nil
	}})
	if fail.Code(err) != "MIGRATION_INTERRUPTED" {
		t.Fatalf("apply did not stop after canonical preparation: %v (%s)", err, fail.Code(err))
	}

	lockRelative := "wip/v1/" + captureRegistryLock
	lockPath := filepath.Join(repo.CommonDir, filepath.FromSlash(lockRelative))
	info, err := os.Lstat(lockPath)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		t.Fatalf("registry lock is unavailable before record publication: mode=%v err=%v", infoMode(info), err)
	}
	if body, err := os.ReadFile(lockPath); err != nil || len(body) != 0 {
		t.Fatalf("registry lock is not the reviewed empty artifact: %q err=%v", body, err)
	}
	for _, relative := range []string{"wip/v1/lanes/lane1.json", "wip/v1/leases/lease1.json"} {
		if _, err := os.Lstat(filepath.Join(repo.CommonDir, filepath.FromSlash(relative))); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("imported record became readable before registry lock publication completed: %s: %v", relative, err)
		}
	}
	journal, exists, err := readJournal(migrationJournalPath(repo.CommonDir))
	if err != nil || !exists {
		t.Fatalf("migration journal is unavailable: exists=%v err=%v", exists, err)
	}
	lockAuthorized := false
	for _, path := range journal.PublishedPaths {
		if path == lockRelative {
			lockAuthorized = true
			break
		}
	}
	if !lockAuthorized {
		t.Fatalf("registry lock is not bound in the migration journal: %#v", journal.PublishedPaths)
	}

	if _, err := applyForTest(context.Background(), repo, plan, plan.PlanDigest, ApplyOptions{Resume: true}); err != nil {
		t.Fatal(err)
	}
	opened, initialized, err := store.Inspect(repo)
	if err != nil || !initialized {
		t.Fatalf("canonical store cannot open after resume: initialized=%v err=%v", initialized, err)
	}
	if _, err := opened.Load("lane1"); err != nil {
		t.Fatalf("imported lane is unreadable after resume: %v", err)
	}
	if _, err := opened.LoadLease("lease1"); err != nil {
		t.Fatalf("imported lease is unreadable after resume: %v", err)
	}
}

func TestPreviewRejectsShadowByteConflict(t *testing.T) {
	repo, head := migrationTestRepo(t)
	if _, err := store.Open(repo); err != nil {
		t.Fatal(err)
	}
	writeLegacyFixture(t, repo, head)
	destination := filepath.Join(repo.CommonDir, "wip", "v1", "lanes", "lane1.json")
	var lane legacyLane
	if _, err := readJSONFile(filepath.Join(repo.CommonDir, "ndev-wip", "v1", "lanes", "lane1.json"), &lane); err != nil {
		t.Fatal(err)
	}
	lane.UpdatedAt = lane.UpdatedAt.Add(time.Second)
	writeTestJSON(t, destination, lane, 0o600)
	var lease legacyLease
	if _, err := readJSONFile(filepath.Join(repo.CommonDir, "ndev-wip", "v1", "leases", "lease1.json"), &lease); err != nil {
		t.Fatal(err)
	}
	writeTestJSON(t, filepath.Join(repo.CommonDir, "wip", "v1", "leases", "lease1.json"), lease, 0o600)
	plan := mustPreview(t, repo)
	if plan.State != "blocked" || len(plan.Conflicts) == 0 || plan.Conflicts[0].Code != "SHADOW_MISMATCH" {
		t.Fatalf("expected shadow mismatch, got %#v", plan.Conflicts)
	}
	_, err := applyForTest(context.Background(), repo, plan, plan.PlanDigest, ApplyOptions{})
	if fail.Code(err) != "MIGRATION_PLAN_BLOCKED" {
		t.Fatalf("expected blocked apply, got %v (%s)", err, fail.Code(err))
	}
}

func TestIncompatibleTerminalHistoryIsArchiveOnlyAndCanonicalStoreReads(t *testing.T) {
	repo, head := migrationTestRepo(t)
	writeLegacyFixture(t, repo, head)
	now := time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)
	released := now
	releasedLane := legacyLane{SchemaVersion: 1, ID: "released-lane", Agent: "agent1", Session: "session1", Mode: store.ModeWorktree, Ref: "refs/heads/wip/agent1/released-lane", BaseRef: "HEAD", BaseSHA: head, CurrentSHA: head, Worktree: repo.Root, LeaseIDs: []string{"released-lease"}, State: "released", CreatedAt: now, UpdatedAt: now}
	legacyNullExpiry := []byte(`{"schema_version":1,"id":"released-lease","lane_id":"released-lane","agent":"agent1","session":"session1","paths":["README.md"],"state":"released","created_at":"2026-08-23T12:00:00Z","updated_at":"2026-08-23T12:00:00Z","expires_at":null,"released_at":"2026-08-23T12:00:00Z"}` + "\n")
	aborted := legacyLane{SchemaVersion: 1, ID: "mcp-broker-9dx-019fcec6", Agent: "agent1", Session: "session1", Mode: store.ModeWorktree, Ref: "refs/heads/wip/agent1/mcp-broker-9dx-019fcec6", BaseRef: "HEAD", BaseSHA: head, CurrentSHA: head, Worktree: "", State: "aborted", CreatedAt: now, UpdatedAt: now}
	_ = released
	writeTestJSON(t, filepath.Join(repo.CommonDir, "ndev-wip", "v1", "lanes", releasedLane.ID+".json"), releasedLane, 0o600)
	writeTestFile(t, filepath.Join(repo.CommonDir, "ndev-wip", "v1", "leases", "released-lease.json"), legacyNullExpiry, 0o600)
	writeTestJSON(t, filepath.Join(repo.CommonDir, "ndev-wip", "v1", "lanes", aborted.ID+".json"), aborted, 0o600)

	plan := mustPreview(t, repo)
	if plan.State != "ready" || importAction(plan.LaneImports, releasedLane.ID) != "archive-only" || importAction(plan.LeaseImports, "released-lease") != "archive-only" || importAction(plan.LaneImports, aborted.ID) != "archive-only" {
		t.Fatalf("terminal dispositions were not archive-only: lanes=%#v leases=%#v conflicts=%#v", plan.LaneImports, plan.LeaseImports, plan.Conflicts)
	}
	if len(plan.Dispositions) < 3 {
		t.Fatalf("missing typed archive-only dispositions: %#v", plan.Dispositions)
	}
	if _, err := applyForTest(context.Background(), repo, plan, plan.PlanDigest, ApplyOptions{}); err != nil {
		t.Fatal(err)
	}
	opened, initialized, err := store.Inspect(repo)
	if err != nil || !initialized {
		t.Fatalf("canonical store cannot open after apply: initialized=%v err=%v", initialized, err)
	}
	if _, err := opened.Load("lane1"); err != nil {
		t.Fatalf("imported lane is unreadable: %v", err)
	}
	if _, err := opened.LoadLease("lease1"); err != nil {
		t.Fatalf("imported lease is unreadable: %v", err)
	}
	imported, err := opened.Load("lane1")
	if err != nil {
		t.Fatal(err)
	}
	if err := opened.ValidateCapture(context.Background(), imported, []string{"README.md"}); err != nil {
		t.Fatalf("post-apply canonical relationship read failed: %v", err)
	}
	if _, err := opened.Load(releasedLane.ID); fail.Code(err) != "LANE_NOT_FOUND" {
		t.Fatalf("archive-only lane leaked into canonical state: %v", err)
	}
	archive := filepath.Join(archiveMigrationRoot(repo.CommonDir, plan.MigrationID), "source", "v1")
	if _, err := os.Stat(filepath.Join(archive, "leases", "released-lease.json")); err != nil {
		t.Fatalf("archive-only lease is missing from sealed source: %v", err)
	}
}

func TestIncompatibleActiveRecordsBlockMigration(t *testing.T) {
	repo, head := migrationTestRepo(t)
	writeLegacyFixture(t, repo, head)
	var lease legacyLease
	if _, err := readJSONFile(filepath.Join(repo.CommonDir, "ndev-wip", "v1", "leases", "lease1.json"), &lease); err != nil {
		t.Fatal(err)
	}
	lease.ExpiresAt = nil
	writeTestJSON(t, filepath.Join(repo.CommonDir, "ndev-wip", "v1", "leases", "lease1.json"), lease, 0o600)
	plan := mustPreview(t, repo)
	if plan.State != "blocked" || importAction(plan.LeaseImports, "lease1") != "conflict" || !hasConflict(plan, "DESTINATION_READER_REJECTED") {
		t.Fatalf("incompatible active lease did not block: %#v %#v", plan.LeaseImports, plan.Conflicts)
	}

	// A reader-valid active lease must also have the exact lane reverse link.
	lease.ExpiresAt = timePointer(time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC))
	writeTestJSON(t, filepath.Join(repo.CommonDir, "ndev-wip", "v1", "leases", "lease1.json"), lease, 0o600)
	var lane legacyLane
	if _, err := readJSONFile(filepath.Join(repo.CommonDir, "ndev-wip", "v1", "lanes", "lane1.json"), &lane); err != nil {
		t.Fatal(err)
	}
	lane.LeaseIDs = nil
	writeTestJSON(t, filepath.Join(repo.CommonDir, "ndev-wip", "v1", "lanes", "lane1.json"), lane, 0o600)
	plan = mustPreview(t, repo)
	if plan.State != "blocked" || !hasConflict(plan, "ACTIVE_LEASE_RELATION_INVALID") {
		t.Fatalf("missing reverse relation did not block: %#v", plan.Conflicts)
	}
}

func TestCrossStoreRelationshipAndScopeConflictsBlockMigration(t *testing.T) {
	t.Run("cross-lane portable lease", func(t *testing.T) {
		repo, head := migrationTestRepo(t)
		if _, err := store.Open(repo); err != nil {
			t.Fatal(err)
		}
		writeLegacyFixture(t, repo, head)
		now := time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)
		expires := time.Date(2030, 8, 24, 12, 0, 0, 0, time.UTC)
		portableLane := legacyLane{SchemaVersion: 1, ID: "portable", Agent: "agent2", Session: "session2", Mode: store.ModeWorktree, Ref: "refs/heads/wip/agent2/portable", BaseRef: "HEAD", BaseSHA: head, CurrentSHA: head, Worktree: filepath.Join(repo.Root, "portable"), LeaseIDs: []string{"shared"}, State: "active", CreatedAt: now, UpdatedAt: now}
		portableLease := legacyLease{SchemaVersion: 1, ID: "shared", LaneID: portableLane.ID, Agent: portableLane.Agent, Session: portableLane.Session, Paths: []string{"portable.txt"}, State: "active", CreatedAt: now, UpdatedAt: now, ExpiresAt: &expires}
		writeTestJSON(t, filepath.Join(repo.CommonDir, "wip", "v1", "lanes", portableLane.ID+".json"), portableLane, 0o600)
		writeTestJSON(t, filepath.Join(repo.CommonDir, "wip", "v1", "leases", portableLease.ID+".json"), portableLease, 0o600)
		runGit(t, repo.Root, "update-ref", portableLane.Ref, head)
		var lane legacyLane
		if _, err := readJSONFile(filepath.Join(repo.CommonDir, "ndev-wip", "v1", "lanes", "lane1.json"), &lane); err != nil {
			t.Fatal(err)
		}
		lane.LeaseIDs = []string{"shared"}
		writeTestJSON(t, filepath.Join(repo.CommonDir, "ndev-wip", "v1", "lanes", "lane1.json"), lane, 0o600)
		if err := os.Remove(filepath.Join(repo.CommonDir, "ndev-wip", "v1", "leases", "lease1.json")); err != nil {
			t.Fatal(err)
		}
		plan := mustPreview(t, repo)
		if plan.State != "blocked" || !hasConflict(plan, "ACTIVE_LANE_LEASE_INVALID") {
			t.Fatalf("cross-lane portable lease did not block: %#v", plan.Conflicts)
		}
	})

	t.Run("merged active scope overlap", func(t *testing.T) {
		repo, head := migrationTestRepo(t)
		if _, err := store.Open(repo); err != nil {
			t.Fatal(err)
		}
		writeLegacyFixture(t, repo, head)
		now := time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)
		expires := time.Date(2030, 8, 24, 12, 0, 0, 0, time.UTC)
		portableLane := legacyLane{SchemaVersion: 1, ID: "portable", Agent: "agent2", Session: "session2", Mode: store.ModeWorktree, Ref: "refs/heads/wip/agent2/portable", BaseRef: "HEAD", BaseSHA: head, CurrentSHA: head, Worktree: filepath.Join(repo.Root, "portable"), LeaseIDs: []string{"portable-lease"}, State: "active", CreatedAt: now, UpdatedAt: now}
		portableLease := legacyLease{SchemaVersion: 1, ID: "portable-lease", LaneID: portableLane.ID, Agent: portableLane.Agent, Session: portableLane.Session, Paths: []string{"README.md"}, State: "active", CreatedAt: now, UpdatedAt: now, ExpiresAt: &expires}
		writeTestJSON(t, filepath.Join(repo.CommonDir, "wip", "v1", "lanes", portableLane.ID+".json"), portableLane, 0o600)
		writeTestJSON(t, filepath.Join(repo.CommonDir, "wip", "v1", "leases", portableLease.ID+".json"), portableLease, 0o600)
		runGit(t, repo.Root, "update-ref", portableLane.Ref, head)
		plan := mustPreview(t, repo)
		if plan.State != "blocked" || !hasConflict(plan, "PATH_LEASE_CONFLICT") {
			t.Fatalf("merged active path overlap did not block: %#v", plan.Conflicts)
		}
	})
}

func TestExpiredActiveLeaseImportsWithoutGrantingPaths(t *testing.T) {
	repo, head := migrationTestRepo(t)
	writeLegacyFixture(t, repo, head)
	var lease legacyLease
	if _, err := readJSONFile(filepath.Join(repo.CommonDir, "ndev-wip", "v1", "leases", "lease1.json"), &lease); err != nil {
		t.Fatal(err)
	}
	lease.ExpiresAt = timePointer(time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC))
	writeTestJSON(t, filepath.Join(repo.CommonDir, "ndev-wip", "v1", "leases", "lease1.json"), lease, 0o600)
	plan := mustPreview(t, repo)
	if plan.State != "ready" || importAction(plan.LeaseImports, "lease1") != "import" || !hasDisposition(plan, "EXPIRED_ACTIVE_LEASE") {
		t.Fatalf("expired active lease plan=%#v dispositions=%#v", plan.LeaseImports, plan.Dispositions)
	}
	if _, err := applyForTest(context.Background(), repo, plan, plan.PlanDigest, ApplyOptions{}); err != nil {
		t.Fatal(err)
	}
	opened, _, err := store.Inspect(repo)
	if err != nil {
		t.Fatal(err)
	}
	paths, err := opened.ActivePaths("lane1")
	if err != nil || len(paths) != 0 {
		t.Fatalf("expired imported lease grants paths: paths=%v err=%v", paths, err)
	}
	lane, err := opened.Load("lane1")
	if err != nil {
		t.Fatal(err)
	}
	if err := opened.ValidateCapture(context.Background(), lane, nil); err != nil {
		t.Fatalf("expired lease destination relationship is invalid: %v", err)
	}
}

func TestPreviewRejectsUnsafeAndMovingSources(t *testing.T) {
	t.Run("symlink", func(t *testing.T) {
		repo, _ := migrationTestRepo(t)
		if err := os.Symlink(repo.Root, filepath.Join(repo.CommonDir, "ndev-wip")); err != nil {
			t.Fatal(err)
		}
		_, err := previewForTest(context.Background(), repo, PreviewOptions{})
		if fail.Code(err) != "MIGRATION_SOURCE_UNSAFE" {
			t.Fatalf("expected unsafe source, got %v (%s)", err, fail.Code(err))
		}
	})
	t.Run("nonregular", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("named pipes are not portable to Windows")
		}
		repo, head := migrationTestRepo(t)
		writeLegacyFixture(t, repo, head)
		fifo := filepath.Join(repo.CommonDir, "ndev-wip", "v1", "queue.pipe")
		if err := exec.Command("mkfifo", fifo).Run(); err != nil {
			t.Fatal(err)
		}
		_, err := previewForTest(context.Background(), repo, PreviewOptions{})
		if fail.Code(err) != "MIGRATION_SOURCE_UNSAFE" {
			t.Fatalf("expected unsafe source, got %v (%s)", err, fail.Code(err))
		}
	})
	t.Run("moving", func(t *testing.T) {
		repo, head := migrationTestRepo(t)
		writeLegacyFixture(t, repo, head)
		_, err := previewForTest(context.Background(), repo, PreviewOptions{AfterFirstSnapshot: func() error {
			return os.WriteFile(filepath.Join(repo.CommonDir, "ndev-wip", "v1", "queue.json"), []byte("{\"changed\":true}\n"), 0o600)
		}})
		if fail.Code(err) != "MIGRATION_SOURCE_MOVED" {
			t.Fatalf("expected moving source, got %v (%s)", err, fail.Code(err))
		}
	})
}

func TestApplyRequiresExactDigestPrestateAndCommonLock(t *testing.T) {
	t.Run("digest", func(t *testing.T) {
		repo, head := migrationTestRepo(t)
		writeLegacyFixture(t, repo, head)
		plan := mustPreview(t, repo)
		before := testTreeDigest(t, repo.CommonDir)
		_, err := applyForTest(context.Background(), repo, plan, "sha256:"+strings.Repeat("0", 64), ApplyOptions{})
		if fail.Code(err) != "MIGRATION_PLAN_DIGEST_MISMATCH" || testTreeDigest(t, repo.CommonDir) != before {
			t.Fatalf("digest refusal mutated state or returned wrong code: %v (%s)", err, fail.Code(err))
		}
	})
	t.Run("prestate", func(t *testing.T) {
		repo, head := migrationTestRepo(t)
		writeLegacyFixture(t, repo, head)
		plan := mustPreview(t, repo)
		writeTestFile(t, filepath.Join(repo.CommonDir, "ndev-wip", "v1", "queue.json"), []byte("{\"moved\":true}\n"), 0o600)
		_, err := applyForTest(context.Background(), repo, plan, plan.PlanDigest, ApplyOptions{})
		if fail.Code(err) != "MIGRATION_PRESTATE_CHANGED" {
			t.Fatalf("expected prestate refusal, got %v (%s)", err, fail.Code(err))
		}
		for _, path := range []string{writerFencePath(repo.CommonDir), bootstrapPlanPath(repo.CommonDir, plan.MigrationID), migrationJournalPath(repo.CommonDir)} {
			if _, statErr := os.Lstat(path); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("prestate refusal published recovery state at %s: %v", path, statErr)
			}
		}
	})
	t.Run("lock", func(t *testing.T) {
		repo, head := migrationTestRepo(t)
		writeLegacyFixture(t, repo, head)
		plan := mustPreview(t, repo)
		lock, err := filelock.Acquire(filepath.Join(repo.CommonDir, "wip-coordination.lock"), 0)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = lock.Release() }()
		_, err = applyForTest(context.Background(), repo, plan, plan.PlanDigest, ApplyOptions{})
		if fail.Code(err) != "MIGRATION_LOCKED" {
			t.Fatalf("expected common-lock refusal, got %v (%s)", err, fail.Code(err))
		}
	})
}

func TestEveryDurablePhaseResumesForward(t *testing.T) {
	phases := []string{phaseJournalCreated, phaseCanonicalPrepared, phaseRecordsPublished, phaseSourceMoved, phaseArchiveSealed, phaseComplete}
	for _, phase := range phases {
		t.Run(phase, func(t *testing.T) {
			repo, head := migrationTestRepo(t)
			writeLegacyFixture(t, repo, head)
			plan := mustPreview(t, repo)
			_, err := applyForTest(context.Background(), repo, plan, plan.PlanDigest, ApplyOptions{AfterPhase: func(actual string) error {
				if actual == phase {
					return errors.New("injected crash")
				}
				return nil
			}})
			if fail.Code(err) != "MIGRATION_INTERRUPTED" {
				t.Fatalf("expected injected interruption, got %v (%s)", err, fail.Code(err))
			}
			result, err := applyForTest(context.Background(), repo, plan, plan.PlanDigest, ApplyOptions{Resume: true})
			if err != nil {
				t.Fatal(err)
			}
			if !result.Applied || result.Status.State != "complete" {
				t.Fatalf("resume did not complete: %#v", result)
			}
		})
	}
}

func TestCrashWindowsInsideDurablePhasesResumeForward(t *testing.T) {
	operations := []string{
		"record-published:wip/v1/lanes/lane1.json",
		"source-renamed",
		"archive-seal-written",
		"receipt-written",
	}
	for _, operation := range operations {
		t.Run(strings.ReplaceAll(operation, "/", "_"), func(t *testing.T) {
			repo, head := migrationTestRepo(t)
			writeLegacyFixture(t, repo, head)
			plan := mustPreview(t, repo)
			_, err := applyForTest(context.Background(), repo, plan, plan.PlanDigest, ApplyOptions{AfterOperation: func(actual string) error {
				if actual == operation {
					return errors.New("injected operation crash")
				}
				return nil
			}})
			if fail.Code(err) != "MIGRATION_INTERRUPTED" {
				t.Fatalf("expected interruption after %s, got %v (%s)", operation, err, fail.Code(err))
			}
			result, err := applyForTest(context.Background(), repo, plan, plan.PlanDigest, ApplyOptions{Resume: true})
			if err != nil {
				t.Fatal(err)
			}
			if !result.Applied || result.Status.State != "complete" {
				t.Fatalf("resume after %s did not complete: %#v", operation, result)
			}
		})
	}
}

func TestChangedMovedSourceBlocksResumeAndPreservesArchive(t *testing.T) {
	repo, head := migrationTestRepo(t)
	writeLegacyFixture(t, repo, head)
	plan := mustPreview(t, repo)
	_, err := applyForTest(context.Background(), repo, plan, plan.PlanDigest, ApplyOptions{AfterOperation: func(operation string) error {
		if operation != "source-renamed" {
			return nil
		}
		queue := filepath.Join(archiveMigrationRoot(repo.CommonDir, plan.MigrationID), "source", "v1", "queue.json")
		if writeErr := os.WriteFile(queue, []byte("{\"changed_after_move\":true}\n"), 0o600); writeErr != nil {
			return writeErr
		}
		return errors.New("injected writer after source move")
	}})
	if fail.Code(err) != "MIGRATION_INTERRUPTED" {
		t.Fatalf("expected interruption after source move, got %v (%s)", err, fail.Code(err))
	}
	_, err = applyForTest(context.Background(), repo, plan, plan.PlanDigest, ApplyOptions{Resume: true})
	if fail.Code(err) != "MIGRATION_ARCHIVE_MOVED" {
		t.Fatalf("changed moved source was not rejected: %v (%s)", err, fail.Code(err))
	}
	if _, err := os.Lstat(filepath.Join(repo.CommonDir, "ndev-wip")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("legacy source unexpectedly reappeared: %v", err)
	}
	if _, err := os.Stat(filepath.Join(archiveMigrationRoot(repo.CommonDir, plan.MigrationID), "source", "v1", "queue.json")); err != nil {
		t.Fatalf("changed source was not preserved for recovery: %v", err)
	}
}

func TestStatusKeepsReceiptCrashInProgressAndBindsArchiveSeal(t *testing.T) {
	repo, head := migrationTestRepo(t)
	writeLegacyFixture(t, repo, head)
	plan := mustPreview(t, repo)
	_, err := applyForTest(context.Background(), repo, plan, plan.PlanDigest, ApplyOptions{AfterOperation: func(operation string) error {
		if operation == "receipt-written" {
			return errors.New("injected receipt crash")
		}
		return nil
	}})
	if fail.Code(err) != "MIGRATION_INTERRUPTED" {
		t.Fatalf("expected receipt interruption, got %v (%s)", err, fail.Code(err))
	}
	status, err := InspectStatus(repo, plan.MigrationID)
	if err != nil || status.State != "in-progress" || status.Receipt == nil || status.Phase != phaseArchiveSealed {
		t.Fatalf("receipt crash status=%#v err=%v", status, err)
	}
	if _, err := applyForTest(context.Background(), repo, plan, plan.PlanDigest, ApplyOptions{Resume: true}); err != nil {
		t.Fatal(err)
	}
	sealPath := filepath.Join(archiveMigrationRoot(repo.CommonDir, plan.MigrationID), "seal.json")
	var seal Seal
	if _, err := readJSONFile(sealPath, &seal); err != nil {
		t.Fatal(err)
	}
	seal.PlanDigest = "sha256:" + strings.Repeat("0", 64)
	if err := writeJSON(sealPath, seal, 0o400, true); err != nil {
		t.Fatal(err)
	}
	if _, err := InspectStatus(repo, plan.MigrationID); fail.Code(err) != "MIGRATION_ARCHIVE_INVALID" {
		t.Fatalf("tampered archive seal was accepted: %v (%s)", err, fail.Code(err))
	}
}

func TestRollbackBeforeMoveLeavesMarkerAndSource(t *testing.T) {
	repo, head := migrationTestRepo(t)
	writeLegacyFixture(t, repo, head)
	plan := mustPreview(t, repo)
	_, err := applyForTest(context.Background(), repo, plan, plan.PlanDigest, ApplyOptions{AfterPhase: func(phase string) error {
		if phase == phaseRecordsPublished {
			return errors.New("stop before move")
		}
		return nil
	}})
	if fail.Code(err) != "MIGRATION_INTERRUPTED" {
		t.Fatal(err)
	}
	result, err := applyForTest(context.Background(), repo, plan, plan.PlanDigest, ApplyOptions{Rollback: true})
	if err != nil {
		t.Fatal(err)
	}
	if !result.RolledBack || result.Status.Rollback == nil || !result.Status.LegacyExists {
		t.Fatalf("unexpected rollback: %#v", result)
	}
	if _, err := os.Lstat(filepath.Join(repo.CommonDir, "wip", "v1", "lanes", "lane1.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("import survived rollback: %v", err)
	}
	if err := activateWriterFence(repo.CommonDir, plan); err != nil {
		t.Fatal(err)
	}
	idempotent, err := applyForTest(context.Background(), repo, plan, plan.PlanDigest, ApplyOptions{Rollback: true})
	if err != nil || !idempotent.RolledBack || idempotent.Status.State != "rolled-back" {
		t.Fatalf("idempotent rollback = %#v err=%v", idempotent, err)
	}
	if _, err := os.Lstat(writerFencePath(repo.CommonDir)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("idempotent rollback left the recovery fence active: %v", err)
	}

	fresh := mustPreview(t, repo)
	if fresh.MigrationID == plan.MigrationID || fresh.PlanDigest == plan.PlanDigest {
		t.Fatalf("fresh preview reused rolled-back identity: old=%s/%s new=%s/%s", plan.MigrationID, plan.PlanDigest, fresh.MigrationID, fresh.PlanDigest)
	}
	completed, err := applyForTest(context.Background(), repo, fresh, fresh.PlanDigest, ApplyOptions{})
	if err != nil || !completed.Applied || completed.Status.State != "complete" {
		t.Fatalf("fresh migration after rollback = %#v err=%v", completed, err)
	}
	retired := retiredJournalPath(repo.CommonDir, plan.MigrationID)
	if info, err := os.Lstat(retired); err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o400 {
		t.Fatalf("retired rollback journal mode=%v err=%v", infoMode(info), err)
	}
	current, exists, err := readJournal(migrationJournalPath(repo.CommonDir))
	if err != nil || !exists || current.MigrationID != fresh.MigrationID || current.Phase != phaseComplete {
		t.Fatalf("active journal after fresh migration = %#v exists=%v err=%v", current, exists, err)
	}
	oldStatus, err := InspectStatus(repo, plan.MigrationID)
	if err != nil || oldStatus.State != "rolled-back" || oldStatus.Rollback == nil {
		t.Fatalf("historical rollback status = %#v err=%v", oldStatus, err)
	}
}

func TestInterruptedRollbackResumesOnlyRollback(t *testing.T) {
	repo, head := migrationTestRepo(t)
	writeLegacyFixture(t, repo, head)
	plan := mustPreview(t, repo)
	_, err := applyForTest(context.Background(), repo, plan, plan.PlanDigest, ApplyOptions{AfterPhase: func(phase string) error {
		if phase == phaseRecordsPublished {
			return errors.New("stop before move")
		}
		return nil
	}})
	if fail.Code(err) != "MIGRATION_INTERRUPTED" {
		t.Fatal(err)
	}

	interrupted := false
	_, err = applyForTest(context.Background(), repo, plan, plan.PlanDigest, ApplyOptions{Rollback: true, AfterOperation: func(operation string) error {
		if !interrupted && strings.HasPrefix(operation, "rollback-removed:") {
			interrupted = true
			return errors.New("stop after exact rollback removal")
		}
		return nil
	}})
	if fail.Code(err) != "MIGRATION_INTERRUPTED" || !interrupted {
		t.Fatalf("rollback interruption = %v (%s), interrupted=%v", err, fail.Code(err), interrupted)
	}
	journal, exists, err := readJournal(migrationJournalPath(repo.CommonDir))
	if err != nil || !exists || journal.Phase != phaseRollbackStarted {
		t.Fatalf("rollback journal = %#v exists=%v err=%v", journal, exists, err)
	}
	if _, err := applyForTest(context.Background(), repo, plan, plan.PlanDigest, ApplyOptions{Resume: true}); fail.Code(err) != "MIGRATION_ROLLBACK_REQUIRED" {
		t.Fatalf("forward resume after rollback start was accepted: %v (%s)", err, fail.Code(err))
	}

	result, err := applyForTest(context.Background(), repo, plan, plan.PlanDigest, ApplyOptions{Rollback: true})
	if err != nil || !result.RolledBack || result.Status.State != "rolled-back" {
		t.Fatalf("rollback retry = %#v err=%v", result, err)
	}
	if _, err := os.Lstat(filepath.Join(repo.CommonDir, "wip", "v1", "lanes", "lane1.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("import survived rollback retry: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(repo.CommonDir, "ndev-wip")); err != nil {
		t.Fatalf("legacy source did not survive rollback retry: %v", err)
	}
}

func TestMigrationIdentityIsDeterministicAndConfined(t *testing.T) {
	repo, head := migrationTestRepo(t)
	writeLegacyFixture(t, repo, head)
	plan := mustPreview(t, repo)

	for _, invalid := range []string{"../../config", "migration-aaaaaaaaaaaaaaaaaaaaaaa", "migration-AAAAAAAAAAAAAAAAAAAAAAAA"} {
		t.Run(invalid, func(t *testing.T) {
			tampered := plan
			tampered.MigrationID = invalid
			var err error
			tampered.PlanDigest, err = planDigest(tampered)
			if err != nil {
				t.Fatal(err)
			}
			_, err = applyForTest(context.Background(), repo, tampered, tampered.PlanDigest, ApplyOptions{})
			if fail.Code(err) != "MIGRATION_PLAN_DIGEST_MISMATCH" {
				t.Fatalf("tampered migration identity was accepted: %v (%s)", err, fail.Code(err))
			}
			if _, err := InspectStatus(repo, invalid); fail.Code(err) != "MIGRATION_STATUS_INVALID" {
				t.Fatalf("status accepted migration identity %q: %v (%s)", invalid, err, fail.Code(err))
			}
		})
	}

	tampered := plan
	tampered.MigrationID = "migration-aaaaaaaaaaaaaaaaaaaaaaaa"
	var err error
	tampered.PlanDigest, err = planDigest(tampered)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := applyForTest(context.Background(), repo, tampered, tampered.PlanDigest, ApplyOptions{}); fail.Code(err) != "MIGRATION_PLAN_DIGEST_MISMATCH" {
		t.Fatalf("well-formed but non-deterministic migration identity was accepted: %v (%s)", err, fail.Code(err))
	}
}

func TestResumeModeAndCanonicalBindings(t *testing.T) {
	repo, head := migrationTestRepo(t)
	writeLegacyFixture(t, repo, head)
	plan := mustPreview(t, repo)
	_, err := applyForTest(context.Background(), repo, plan, plan.PlanDigest, ApplyOptions{AfterPhase: func(phase string) error {
		if phase == phaseRecordsPublished {
			return errors.New("stop after records")
		}
		return nil
	}})
	if fail.Code(err) != "MIGRATION_INTERRUPTED" {
		t.Fatalf("expected interruption, got %v", err)
	}
	result, err := applyForTest(context.Background(), repo, plan, plan.PlanDigest, ApplyOptions{Resume: true})
	if err != nil {
		t.Fatal(err)
	}
	if result.Mode != "resume" || !result.Resumed || !result.Applied || result.Receipt == nil || result.Receipt.CanonicalManifest != plan.PlannedCanonicalData.Digest {
		t.Fatalf("resume protocol or manifest binding is invalid: %#v", result)
	}
}

func TestProducerIdentityIsDigestBoundAndRevalidated(t *testing.T) {
	t.Run("preview requires complete identity", func(t *testing.T) {
		repo, _ := migrationTestRepo(t)
		if _, err := Preview(context.Background(), repo, PreviewOptions{}); fail.Code(err) != "MIGRATION_PRODUCER_INVALID" {
			t.Fatalf("preview accepted an absent producer: %v (%s)", err, fail.Code(err))
		}
	})
	t.Run("identity is part of plan digest", func(t *testing.T) {
		repo, head := migrationTestRepo(t)
		writeLegacyFixture(t, repo, head)
		plan := mustPreview(t, repo)
		changed := plan
		changed.Producer.ExecutableSHA256 = "sha256:" + strings.Repeat("d", 64)
		if err := changed.ValidateDigest(plan.PlanDigest); err == nil {
			t.Fatal("producer change did not invalidate the reviewed plan digest")
		}
	})
	t.Run("apply rejects different running executable", func(t *testing.T) {
		repo, head := migrationTestRepo(t)
		writeLegacyFixture(t, repo, head)
		plan := mustPreview(t, repo)
		producer := plan.Producer
		producer.ExecutableSHA256 = "sha256:" + strings.Repeat("d", 64)
		_, err := Apply(context.Background(), repo, plan, plan.PlanDigest, ApplyOptions{Producer: producer})
		if fail.Code(err) != "MIGRATION_PRODUCER_MISMATCH" {
			t.Fatalf("apply accepted a different executable: %v (%s)", err, fail.Code(err))
		}
	})
	t.Run("resume rejects different portable bundle", func(t *testing.T) {
		repo, head := migrationTestRepo(t)
		writeLegacyFixture(t, repo, head)
		plan := mustPreview(t, repo)
		_, err := applyForTest(context.Background(), repo, plan, plan.PlanDigest, ApplyOptions{AfterPhase: func(phase string) error {
			if phase == phaseJournalCreated {
				return errors.New("stop after journal")
			}
			return nil
		}})
		if fail.Code(err) != "MIGRATION_INTERRUPTED" {
			t.Fatal(err)
		}
		producer := plan.Producer
		producer.PortableBundleSHA256 = "sha256:" + strings.Repeat("d", 64)
		_, err = Apply(context.Background(), repo, plan, plan.PlanDigest, ApplyOptions{Resume: true, Producer: producer})
		if fail.Code(err) != "MIGRATION_PRODUCER_MISMATCH" {
			t.Fatalf("resume accepted a different portable bundle: %v (%s)", err, fail.Code(err))
		}
	})
	t.Run("forged producer cannot name an invalid tag", func(t *testing.T) {
		producer := testProducerIdentity()
		producer.ReleaseTagged = true
		producer.ReleaseTag = "v9.9.9"
		if err := producer.Validate(); err == nil {
			t.Fatal("producer accepted a tag that does not match its version")
		}
	})
}

func TestResumeAndStatusRejectCanonicalDrift(t *testing.T) {
	t.Run("resume", func(t *testing.T) {
		repo, head := migrationTestRepo(t)
		writeLegacyFixture(t, repo, head)
		plan := mustPreview(t, repo)
		_, err := applyForTest(context.Background(), repo, plan, plan.PlanDigest, ApplyOptions{AfterPhase: func(phase string) error {
			if phase == phaseRecordsPublished {
				return errors.New("stop after records")
			}
			return nil
		}})
		if fail.Code(err) != "MIGRATION_INTERRUPTED" {
			t.Fatal(err)
		}
		writeTestFile(t, filepath.Join(repo.CommonDir, "wip", "v1", "lanes", "lane1.json"), []byte("{}\n"), 0o600)
		_, err = applyForTest(context.Background(), repo, plan, plan.PlanDigest, ApplyOptions{Resume: true})
		if fail.Code(err) != "MIGRATION_CANONICAL_MOVED" {
			t.Fatalf("canonical drift was accepted on resume: %v (%s)", err, fail.Code(err))
		}
	})
	t.Run("status", func(t *testing.T) {
		repo, head := migrationTestRepo(t)
		writeLegacyFixture(t, repo, head)
		plan := mustPreview(t, repo)
		if _, err := applyForTest(context.Background(), repo, plan, plan.PlanDigest, ApplyOptions{}); err != nil {
			t.Fatal(err)
		}
		writeTestFile(t, filepath.Join(repo.CommonDir, "wip", "v1", "lanes", "lane1.json"), []byte("{}\n"), 0o600)
		if _, err := InspectStatus(repo, plan.MigrationID); fail.Code(err) != "MIGRATION_CANONICAL_MOVED" {
			t.Fatalf("status accepted canonical drift: %v (%s)", err, fail.Code(err))
		}
	})
	t.Run("resume rejects envelope sibling", func(t *testing.T) {
		repo, head := migrationTestRepo(t)
		writeLegacyFixture(t, repo, head)
		plan := mustPreview(t, repo)
		_, err := applyForTest(context.Background(), repo, plan, plan.PlanDigest, ApplyOptions{AfterPhase: func(phase string) error {
			if phase == phaseCanonicalPrepared {
				return errors.New("stop after canonical preparation")
			}
			return nil
		}})
		if fail.Code(err) != "MIGRATION_INTERRUPTED" {
			t.Fatal(err)
		}
		writeTestFile(t, filepath.Join(repo.CommonDir, "wip", "unexpected.json"), []byte("{}\n"), 0o600)
		_, err = applyForTest(context.Background(), repo, plan, plan.PlanDigest, ApplyOptions{Resume: true})
		if fail.Code(err) != "MIGRATION_CANONICAL_MOVED" {
			t.Fatalf("canonical envelope drift was accepted on resume: %v (%s)", err, fail.Code(err))
		}
	})
	t.Run("journal cannot authorize an unplanned sibling", func(t *testing.T) {
		repo, head := migrationTestRepo(t)
		writeLegacyFixture(t, repo, head)
		plan := mustPreview(t, repo)
		_, err := applyForTest(context.Background(), repo, plan, plan.PlanDigest, ApplyOptions{AfterPhase: func(phase string) error {
			if phase == phaseCanonicalPrepared {
				return errors.New("stop after canonical preparation")
			}
			return nil
		}})
		if fail.Code(err) != "MIGRATION_INTERRUPTED" {
			t.Fatal(err)
		}
		journalPath := migrationJournalPath(repo.CommonDir)
		journal, exists, err := readJournal(journalPath)
		if err != nil || !exists {
			t.Fatal(err)
		}
		journal.PublishedPaths = append(journal.PublishedPaths, "wip/unexpected.json")
		if err := writeJSON(journalPath, journal, 0o600, true); err != nil {
			t.Fatal(err)
		}
		writeTestFile(t, filepath.Join(repo.CommonDir, "wip", "unexpected.json"), []byte("{}\n"), 0o600)
		_, err = applyForTest(context.Background(), repo, plan, plan.PlanDigest, ApplyOptions{Resume: true})
		if fail.Code(err) != "MIGRATION_CANONICAL_MOVED" {
			t.Fatalf("unplanned journal path was accepted: %v (%s)", err, fail.Code(err))
		}
	})
	t.Run("rollback verifies prefix before removal", func(t *testing.T) {
		repo, head := migrationTestRepo(t)
		writeLegacyFixture(t, repo, head)
		plan := mustPreview(t, repo)
		_, err := applyForTest(context.Background(), repo, plan, plan.PlanDigest, ApplyOptions{AfterPhase: func(phase string) error {
			if phase == phaseRecordsPublished {
				return errors.New("stop after records")
			}
			return nil
		}})
		if fail.Code(err) != "MIGRATION_INTERRUPTED" {
			t.Fatal(err)
		}
		writeTestFile(t, filepath.Join(repo.CommonDir, "wip", "unexpected.json"), []byte("{}\n"), 0o600)
		_, err = applyForTest(context.Background(), repo, plan, plan.PlanDigest, ApplyOptions{Rollback: true})
		if fail.Code(err) != "MIGRATION_CANONICAL_MOVED" {
			t.Fatalf("rollback accepted an unverified canonical prefix: %v (%s)", err, fail.Code(err))
		}
		if _, statErr := os.Lstat(filepath.Join(repo.CommonDir, "wip", "v1", "lanes", "lane1.json")); statErr != nil {
			t.Fatalf("rollback mutated outputs before verification: %v", statErr)
		}
	})
}

func TestInterruptedMigrationFenceBlocksCurrentWriters(t *testing.T) {
	repo, head := migrationTestRepo(t)
	writeLegacyFixture(t, repo, head)
	plan := mustPreview(t, repo)
	_, err := applyForTest(context.Background(), repo, plan, plan.PlanDigest, ApplyOptions{AfterPhase: func(phase string) error {
		if phase == phaseJournalCreated {
			return errors.New("stop with durable fence")
		}
		return nil
	}})
	if fail.Code(err) != "MIGRATION_INTERRUPTED" {
		t.Fatal(err)
	}
	if _, err := store.AcquireWriterFence(repo, 0); fail.Code(err) != "MIGRATION_IN_PROGRESS" {
		t.Fatalf("current writer ignored migration fence: %v (%s)", err, fail.Code(err))
	}
	if _, err := applyForTest(context.Background(), repo, plan, plan.PlanDigest, ApplyOptions{Resume: true}); err != nil {
		t.Fatal(err)
	}
	fence, err := store.AcquireWriterFence(repo, 0)
	if err != nil {
		t.Fatalf("completed migration left writer fence active: %v", err)
	}
	_ = fence.Release()
}

func TestPreviewRejectsNestedLegacyRecordAndPlanBound(t *testing.T) {
	repo, head := migrationTestRepo(t)
	writeLegacyFixture(t, repo, head)
	if err := os.MkdirAll(filepath.Join(repo.CommonDir, "ndev-wip", "v1", "lanes", "nested"), 0o700); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(repo.CommonDir, "ndev-wip", "v1", "lanes", "nested", "lane1.json"), []byte("{}\n"), 0o600)
	if _, err := previewForTest(context.Background(), repo, PreviewOptions{}); fail.Code(err) != "MIGRATION_SOURCE_INVALID" {
		t.Fatalf("nested legacy record was accepted: %v (%s)", err, fail.Code(err))
	}
	if _, err := DecodePlan(make([]byte, MaxPlanBytes+1)); err == nil {
		t.Fatal("oversized plan was accepted")
	}
}

func TestConfinedPublicationRejectsNestedSymlinkParent(t *testing.T) {
	common := t.TempDir()
	outside := t.TempDir()
	if err := os.MkdirAll(filepath.Join(common, "wip", "review", "v1"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(common, "wip", "review", "v1", "archive")); err != nil {
		t.Fatal(err)
	}
	err := createConfinedExact(common, "wip/review/v1/archive/receipt.json", []byte("protected\n"), 0o600)
	if err == nil {
		t.Fatal("nested symlink parent was accepted")
	}
	if _, err := os.Lstat(filepath.Join(outside, "receipt.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("publication escaped through symlink: %v", err)
	}
}

func TestMigrationGitFixtureIgnoresInheritedRepositoryEnvironment(t *testing.T) {
	decoy := t.TempDir()
	runGit(t, decoy, "init", "-q")
	decoyConfig := filepath.Join(decoy, ".git", "config")
	before, err := os.ReadFile(decoyConfig)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_DIR", filepath.Join(decoy, ".git"))
	t.Setenv("GIT_WORK_TREE", decoy)
	t.Setenv("GIT_INDEX_FILE", filepath.Join(decoy, ".git", "foreign-index"))
	fixture := t.TempDir()
	runGit(t, fixture, "init", "-q")
	runGit(t, fixture, "config", "user.name", "Confined Fixture")
	after, err := os.ReadFile(decoyConfig)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("fixture Git command mutated the inherited repository")
	}
}

func migrationTestRepo(t *testing.T) (gitx.Repo, string) {
	t.Helper()
	root := t.TempDir()
	runGit(t, root, "init", "-q")
	runGit(t, root, "config", "user.name", "Migration Test")
	runGit(t, root, "config", "user.email", "migration@example.invalid")
	writeTestFile(t, filepath.Join(root, "README.md"), []byte("test\n"), 0o600)
	runGit(t, root, "add", "README.md")
	runGit(t, root, "commit", "-qm", "initial")
	repo, err := gitx.Discover(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	head := strings.TrimSpace(runGit(t, root, "rev-parse", "HEAD"))
	t.Cleanup(func() {
		archive := filepath.Join(repo.CommonDir, "wip", "archive")
		_ = filepath.Walk(archive, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return nil
			}
			if info.IsDir() {
				_ = os.Chmod(path, 0o700)
			} else {
				_ = os.Chmod(path, 0o600)
			}
			return nil
		})
	})
	return repo, head
}

func writeLegacyFixture(t *testing.T, repo gitx.Repo, head string) {
	t.Helper()
	for _, directory := range []string{"lanes", "leases", "locks", "landing-intents", "archive"} {
		if err := os.MkdirAll(filepath.Join(repo.CommonDir, "ndev-wip", "v1", directory), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)
	expires := time.Date(2030, 8, 24, 12, 0, 0, 0, time.UTC)
	lane := legacyLane{SchemaVersion: 1, ID: "lane1", Agent: "agent1", Session: "session1", Mode: "worktree", Ref: "refs/heads/wip/agent1/lane1", BaseRef: "HEAD", BaseSHA: head, CurrentSHA: head, Worktree: repo.Root, LeaseIDs: []string{"lease1"}, State: "active", CreatedAt: now, UpdatedAt: now}
	lease := legacyLease{SchemaVersion: 1, ID: "lease1", LaneID: "lane1", Agent: "agent1", Session: "session1", Paths: []string{"README.md"}, State: "active", CreatedAt: now, UpdatedAt: now, ExpiresAt: &expires}
	writeTestJSON(t, filepath.Join(repo.CommonDir, "ndev-wip", "v1", "lanes", "lane1.json"), lane, 0o600)
	writeTestJSON(t, filepath.Join(repo.CommonDir, "ndev-wip", "v1", "leases", "lease1.json"), lease, 0o600)
	writeTestFile(t, filepath.Join(repo.CommonDir, "ndev-wip", "v1", "queue.json"), []byte("{\"schema_version\":1,\"entries\":[]}\n"), 0o600)
	writeTestFile(t, filepath.Join(repo.CommonDir, "ndev-wip", "v1", "landing-intents", "intent.json"), []byte("{\"id\":\"intent\"}\n"), 0o600)
	runGit(t, repo.Root, "update-ref", lane.Ref, head)
}

func testManifestEntry(manifest TreeManifest, path string) (ManifestEntry, bool) {
	for _, entry := range manifest.Entries {
		if entry.Path == path {
			return entry, true
		}
	}
	return ManifestEntry{}, false
}

func writeTestJSON(t *testing.T, path string, value any, mode os.FileMode) {
	t.Helper()
	body, err := normalizedJSON(value)
	if err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, path, body, mode)
}

func writeTestFile(t *testing.T, path string, body []byte, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, body, mode); err != nil {
		t.Fatal(err)
	}
}

func runGit(t *testing.T, directory string, args ...string) string {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = directory
	command.Env = confinedGitEnvironment(os.Environ())
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
	return string(output)
}

func testProducerIdentity() ProducerIdentity {
	return ProducerIdentity{
		CommandProtocol:      contract.Protocol,
		Product:              "github.com/nstranquist/wip-commit",
		Version:              "0.2.0-beta.1",
		SourceRevision:       strings.Repeat("a", 40),
		SourceState:          "clean",
		PortableBundleSHA256: "sha256:" + strings.Repeat("b", 64),
		ExecutableSHA256:     "sha256:" + strings.Repeat("c", 64),
	}
}

func previewForTest(ctx context.Context, repo gitx.Repo, options PreviewOptions) (Plan, error) {
	options.Producer = testProducerIdentity()
	return Preview(ctx, repo, options)
}

func applyForTest(ctx context.Context, repo gitx.Repo, plan Plan, expectedDigest string, options ApplyOptions) (Result, error) {
	options.Producer = plan.Producer
	return Apply(ctx, repo, plan, expectedDigest, options)
}

func confinedGitEnvironment(environment []string) []string {
	blocked := map[string]bool{
		"GIT_ALTERNATE_OBJECT_DIRECTORIES": true,
		"GIT_CEILING_DIRECTORIES":          true,
		"GIT_COMMON_DIR":                   true,
		"GIT_CONFIG":                       true,
		"GIT_CONFIG_COUNT":                 true,
		"GIT_CONFIG_GLOBAL":                true,
		"GIT_CONFIG_NOSYSTEM":              true,
		"GIT_CONFIG_SYSTEM":                true,
		"GIT_DIR":                          true,
		"GIT_INDEX_FILE":                   true,
		"GIT_OBJECT_DIRECTORY":             true,
		"GIT_WORK_TREE":                    true,
	}
	result := make([]string, 0, len(environment)+2)
	for _, item := range environment {
		key, _, _ := strings.Cut(item, "=")
		if blocked[key] || strings.HasPrefix(key, "GIT_CONFIG_KEY_") || strings.HasPrefix(key, "GIT_CONFIG_VALUE_") {
			continue
		}
		result = append(result, item)
	}
	return append(result, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull)
}

func mustPreview(t *testing.T, repo gitx.Repo) Plan {
	t.Helper()
	plan, err := previewForTest(context.Background(), repo, PreviewOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

func testTreeDigest(t *testing.T, commonDir string) string {
	t.Helper()
	manifest, err := manifestTree(commonDir, ".")
	if err != nil {
		t.Fatal(err)
	}
	return manifest.Digest
}

func assertDigest(t *testing.T, path, want string) {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := digest(body); got != want {
		t.Fatalf("digest %s = %s, want %s", path, got, want)
	}
}

func findCopy(t *testing.T, copies []CopyRecord, suffix string) CopyRecord {
	t.Helper()
	for _, copy := range copies {
		if strings.HasSuffix(copy.Destination, suffix) {
			return copy
		}
	}
	t.Fatalf("copy ending in %s not found", suffix)
	return CopyRecord{}
}

func infoMode(info os.FileInfo) os.FileMode {
	if info == nil {
		return 0
	}
	return info.Mode().Perm()
}

func importAction(records []ImportRecord, id string) string {
	for _, record := range records {
		if record.ID == id {
			return record.Action
		}
	}
	return ""
}

func hasConflict(plan Plan, code string) bool {
	for _, conflict := range plan.Conflicts {
		if conflict.Code == code {
			return true
		}
	}
	return false
}

func hasDisposition(plan Plan, code string) bool {
	for _, disposition := range plan.Dispositions {
		if disposition.Code == code {
			return true
		}
	}
	return false
}

func timePointer(value time.Time) *time.Time { return &value }
