package filelock

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestExclusiveLockWaitAndRelease(t *testing.T) {
	path := filepath.Join(t.TempDir(), "locks", "lane.lock")
	first, err := Acquire(path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	if _, err := Acquire(path, 100*time.Millisecond); err == nil {
		t.Fatal("second owner acquired a held lock")
	}
	if time.Since(started) < 90*time.Millisecond {
		t.Fatal("lock wait returned before its budget")
	}
	if err := first.Release(); err != nil {
		t.Fatal(err)
	}
	second, err := Acquire(path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := second.Release(); err != nil {
		t.Fatal(err)
	}
	if err := second.Release(); err != nil {
		t.Fatalf("idempotent release: %v", err)
	}
}

func TestZeroWaitIsFailFast(t *testing.T) {
	path := filepath.Join(t.TempDir(), "locks", "lane.lock")
	first, err := Acquire(path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = first.Release() }()

	started := time.Now()
	if _, err := Acquire(path, 0); err == nil {
		t.Fatal("zero-wait acquisition bypassed the current owner")
	}
	if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
		t.Fatalf("zero-wait acquisition blocked for %s", elapsed)
	}

	started = time.Now()
	if _, err := AcquireExisting(path, 0); err == nil {
		t.Fatal("zero-wait existing acquisition bypassed the current owner")
	}
	if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
		t.Fatalf("zero-wait existing acquisition blocked for %s", elapsed)
	}
}

func TestAcquireExistingPreservesLockFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "locks", "lane.lock")
	created, err := Acquire(path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := created.Release(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	observed, err := AcquireExisting(path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := observed.Release(); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatalf("existing lock changed bytes: before=%q after=%q", before, after)
	}

	missingRoot := t.TempDir()
	missingParent := filepath.Join(missingRoot, "missing")
	missing := filepath.Join(missingParent, "lane.lock")
	if _, err := AcquireExisting(missing, time.Second); err == nil {
		t.Fatal("existing lock acquisition created a missing lock")
	}
	if _, err := os.Lstat(missing); !os.IsNotExist(err) {
		t.Fatalf("missing lock path changed: %v", err)
	}
	if _, err := os.Lstat(missingParent); !os.IsNotExist(err) {
		t.Fatalf("missing lock parent changed: %v", err)
	}

	if _, err := AcquireExisting(t.TempDir(), time.Second); err == nil {
		t.Fatal("existing lock acquisition accepted a directory")
	}
	if runtime.GOOS != "windows" {
		target := filepath.Join(t.TempDir(), "target.lock")
		if err := os.WriteFile(target, []byte("target\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(t.TempDir(), "link.lock")
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
		if _, err := AcquireExisting(link, time.Second); err == nil {
			t.Fatal("existing lock acquisition followed a symlink")
		}
	}
}

func TestValidateExistingPathRejectsReplacement(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "lane.lock")
	if err := os.WriteFile(path, []byte("original\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	opened, err := openExisting(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = opened.Close() }()
	if err := os.Rename(path, filepath.Join(directory, "old.lock")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("replacement\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := validateExistingPath(path, opened); err == nil || !strings.Contains(err.Error(), "changed while acquiring") {
		t.Fatalf("replacement validation error = %v", err)
	}
}

func TestAcquireExistingWaitsForOwner(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lane.lock")
	first, err := Acquire(path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = first.Release() }()
	if _, err := AcquireExisting(path, 100*time.Millisecond); err == nil {
		t.Fatal("existing lock acquisition bypassed the current owner")
	}
}

func TestLockWaitBounds(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lane.lock")
	if _, err := Acquire(path, -time.Second); err == nil {
		t.Fatal("negative wait passed")
	}
	if _, err := Acquire(path, MaxWait+time.Nanosecond); err == nil {
		t.Fatal("oversized wait passed")
	}
}
