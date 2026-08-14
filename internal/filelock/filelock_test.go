package filelock

import (
	"path/filepath"
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

func TestLockWaitBounds(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lane.lock")
	if _, err := Acquire(path, -time.Second); err == nil {
		t.Fatal("negative wait passed")
	}
	if _, err := Acquire(path, MaxWait+time.Nanosecond); err == nil {
		t.Fatal("oversized wait passed")
	}
}
