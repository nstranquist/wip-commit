package store

import (
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/nstranquist/wip-commit/internal/fail"
	"github.com/nstranquist/wip-commit/internal/filelock"
	"github.com/nstranquist/wip-commit/internal/gitx"
)

const migrationWriterFenceName = "wip-coordination-migration-v1.fence.json"

const DefaultWriterFenceWait = filelock.DefaultWait

// WriterFence is the exclusive repository-wide coordination writer token.
// Command dispatch keeps it until every mutation and verification has returned.
type WriterFence struct {
	commonDir string
	lock      *filelock.Lock
}

func AcquireWriterFence(repo gitx.Repo, wait time.Duration) (*WriterFence, error) {
	lock, err := filelock.Acquire(filepath.Join(repo.CommonDir, "wip-coordination.lock"), wait)
	if err != nil {
		return nil, fail.Wrap("LOCK_TIMEOUT", err)
	}
	fence := &WriterFence{commonDir: filepath.Clean(repo.CommonDir), lock: lock}
	if err := refuseActiveMigrationFence(repo.CommonDir); err != nil {
		_ = fence.Release()
		return nil, err
	}
	return fence, nil
}

func (fence *WriterFence) Release() error {
	if fence == nil || fence.lock == nil {
		return nil
	}
	err := fence.lock.Release()
	fence.lock = nil
	return err
}

func (fence *WriterFence) validFor(repo gitx.Repo) bool {
	return fence != nil && fence.lock != nil && fence.commonDir == filepath.Clean(repo.CommonDir)
}

// WriterFenced reports whether command dispatch retained a live exact token.
func (store Store) WriterFenced() bool {
	return store.writerFence != nil && store.writerFence.validFor(store.Repo)
}

func withWriterFence[T any](store Store, operation func(Store) (T, error)) (T, error) {
	var zero T
	if store.WriterFenced() {
		return operation(store)
	}
	fence, err := AcquireWriterFence(store.Repo, DefaultWriterFenceWait)
	if err != nil {
		return zero, err
	}
	defer func() { _ = fence.Release() }()
	store.writerFence = fence
	return operation(store)
}

func (store Store) requireWriterFence() error {
	if !store.WriterFenced() {
		return fail.New("WRITER_FENCE_REQUIRED", "store mutation requires the exact repository writer fence")
	}
	return nil
}

func refuseActiveMigrationFence(commonDir string) error {
	info, err := os.Lstat(filepath.Join(commonDir, migrationWriterFenceName))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fail.Wrap("MIGRATION_IN_PROGRESS", err)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return fail.New("MIGRATION_IN_PROGRESS", "coordination migration writer fence is unsafe; refuse all writes")
	}
	return fail.New("MIGRATION_IN_PROGRESS", "coordination migration holds the repository writer fence")
}
